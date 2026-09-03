/* Servers — the managed fleet.
 *
 * One of the panel's two sections, beside the tunnels, so it is a page in #view
 * rather than a dialog over one. It has a job the rest of the panel does not:
 * it hands the operator a line to run on another machine and then has to tell
 * them whether it worked. So the command is shown once, in full, and the page
 * watches for the server to appear rather than leaving them to reload and guess.
 *
 * CLI: `backpack node …` on the managed server. There is no menu equivalent
 * here, because there is nothing to do on this side but wait.
 */

import { $, el } from '../lib/dom.js';
import { toast, oops } from '../ui/toast.js';
import * as api from '../api.js';

const ago = ts => {
  if (!ts) return 'never';
  const s = Math.max(0, Math.floor(Date.now() / 1000) - ts);
  if (s < 90) return 'just now';
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 172800) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
};

/* Said at the moment of removal, because "its tunnels keep running" is vague
   about which tunnels, and the answer is the reason to hesitate. */
function builtThere(n) {
  const c = (n.tunnels || []).length;
  if (!c) return 'Nothing was built there from this panel';
  return c === 1
    ? `The tunnel built there from this panel (<b>${n.tunnels[0]}</b>) keeps running`
    : `The ${c} tunnels built there from this panel keep running`;
}

/* The mark behind a server card.
 *
 * A rack, drawn in the same line style as every icon in the panel and scaled up
 * until it is architecture rather than iconography. It sits under the address
 * and bleeds off the right edge, faint enough to be texture — the card has to
 * read exactly as well with it as without, so it is never the thing the eye
 * lands on.
 *
 * The top unit's light is the only part that means anything: it takes the
 * connected colour, so the card's state is legible from its shape alone.
 */
const RACK_SVG = `<svg class="sv-bg" viewBox="0 0 120 150" aria-hidden="true" focusable="false">
  <g vector-effect="non-scaling-stroke">
    <rect x="14" y="10" width="92" height="36" rx="7"/>
    <rect x="14" y="56" width="92" height="36" rx="7"/>
    <rect x="14" y="102" width="92" height="36" rx="7"/>
    <path d="M74 22h20M74 28h14"/>
    <path d="M74 68h20M74 74h14"/>
    <path d="M74 114h20M74 120h14"/>
    <path d="M28 34h30M28 80h30M28 126h30"/>
    <circle class="led" cx="28" cy="25" r="3.4"/>
    <circle cx="28" cy="71" r="3.4"/>
    <circle cx="28" cy="117" r="3.4"/>
  </g>
</svg>`;

const WARN_SVG = `<svg class="x" viewBox="0 0 24 24"><path d="M12 9v5"/>
<circle cx="12" cy="17.5" r=".6"/><path d="M10.3 3.9L2.6 17.4A2 2 0 004.3 20.4h15.4a2 2 0
 001.7-3L13.7 3.9a2 2 0 00-3.4 0z"/></svg>`;

const SHELL = `
<div class="np7">
  <div class="sech2">
    <h2>Servers</h2>
    <span class="cnt" id="nCount">0</span>
    <span class="sp"></span>
    <button class="sb primary" id="naddb">Add a server</button>
  </div>

  <div class="row7">
    <div class="t7">
      <b>Accept servers</b>
      <span id="lisub">Off. Servers have nothing to connect to yet.</span>
    </div>
    <button class="sw7" id="nsw" aria-pressed="false" aria-label="Accept servers"></button>
  </div>

  <div id="fleet" class="grid3"></div>

  <div class="empty7" id="nempty" hidden>
    <svg class="x" viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="14" width="18" height="7" rx="2"/></svg>
    <b>No servers yet</b>
    <span>Add one above.</span>
  </div>
</div>`;

export function serversView(ctx) {
  const view = $('#view');
  view.innerHTML = SHELL;

  const root    = view;
  let suggested = 0;
  const sw      = $('#nsw', root);
  const addRow  = $('#addrow', root);
  const nameIn  = $('#nname', root);
  const addB    = $('#naddb', root);
  const goB     = $('#ngo', root);
  const cmdBox  = $('#cmdbox', root);
  const fleet   = $('#fleet', root);
  let watch = null;

  const stopWatch = () => { if (watch) { clearInterval(watch); watch = null; } };
  ctx.setTeardown(stopWatch);

  /* ---- painting ---- */
  function paint(state) {
    const on = !!state.enabled;
    sw.classList.toggle('on', on);
    sw.setAttribute('aria-pressed', String(on));
    // Held for the next draft card: a free, random, five-digit port the
    // operator has no basis to choose better than the machine can.
    if (state.suggestPort) suggested = state.suggestPort;
    addB.disabled = !on;
    addB.title = on ? '' : 'Turn the listener on first';

    const nodes = state.nodes || [];
    const pending = state.pending || [];

    $('#lisub', root).textContent = on
      ? (nodes.length
          ? `Accepting ${nodes.length === 1 ? '1 server' : nodes.length + ' servers'}.`
          : 'On.')
      : 'Off.';

    $('#nempty', root).hidden = !!(nodes.length || pending.length || draft);
    $('#nCount', root).textContent = String(nodes.length);

    reconcile(nodes, pending);

    const dock = $('#dock-s');
    if (dock) dock.textContent = nodes.length ? String(nodes.length) : '';

    countTunnels(nodes);
  }

  /* One server, as a card.
   *
   * A card rather than a row because a server is not a line item: it has a
   * name, an address, a machine underneath it and a job, and a row makes the
   * operator read all of that sideways. The four facts sit in a block the eye
   * takes in at once, and the address is the largest of them — it is the thing
   * that gets copied into a tunnel. */
  function nodeCard(n) {
    const i = n.info || {};
    const built = (n.tunnels || []).length;
    const card = el('div', { class: 'sv7' + (n.online ? ' on7' : ''), html: RACK_SVG }, [
      el('div', { class: 'sv-h' }, [
        el('span', { class: 'sv-dot' }),
        el('div', { class: 'sv-id' }, [
          el('b', { text: n.name }),
          el('em', { text: i.hostname || 'no hostname yet' }),
        ]),
        el('span', { class: 'sv-tag', text: n.online ? 'connected' : 'offline' }),
      ]),
      el('div', { class: 'sv-addr' }, [
        el('span', { text: i.ipv4 && i.ipv4 !== '-' ? i.ipv4 : 'address not reported yet' }),
        i.ipv6 && i.ipv6 !== '-' ? el('em', { text: i.ipv6 }) : null,
      ]),
      el('div', { class: 'sv-facts' }, [
        fact7('Its port', n.port ? String(n.port) : '—', n.listening === false ? 'not open' : ''),
        fact7('Tunnels', built ? String(built) : '—'),
        fact7('System', i.os && i.arch ? `${i.os}/${i.arch}` : '—'),
        fact7(n.online ? 'Enrolled' : 'Last seen', n.online ? ago(n.enrolled) : ago(n.lastSeen)),
      ]),
      el('div', { class: 'sv-f' }, [
        el('span', { class: 'sp' }),
        el('button', { class: 'btn7 warn', text: 'Remove' }),
      ]),
    ]);
    const confirm = el('div', { class: 'cf7' }, [
      el('p', { html: `Stop managing <b>${n.name}</b>? ${builtThere(n)} — this panel just loses the way to change them.` }),
      el('button', { class: 'btn7 warn', text: 'Remove' }),
      el('button', { class: 'btn7', text: 'Cancel' }),
    ]);
    card.append(confirm);

    card.querySelector('.sv-f .btn7.warn').addEventListener('click', () => card.classList.add('arm7'));
    const [go, cancel] = confirm.querySelectorAll('button');
    cancel.addEventListener('click', () => card.classList.remove('arm7'));
    go.addEventListener('click', async () => {
      go.disabled = true;
      try {
        paint(await api.nodeRemove(n.name));
        toast(`${n.name} is no longer managed.`);
      } catch (e) { oops(e); go.disabled = false; }
    });
    return card;
  }

  /* warn is set when the fact is true and also a problem — a port a server was
     given that this panel could not actually open, which is a state nothing
     else on the screen would explain. */
  const fact7 = (k, v, warn) => el('div', { class: 'sv-fact' + (warn ? ' bad7' : '') }, [
    el('span', { text: k }), el('b', { text: v }),
    warn ? el('i', { text: warn }) : null,
  ]);

  /* A server that was issued a command and has not used it yet. Shown because
     it is the difference between "I never pressed the button" and "I pressed it
     and nothing happened over there". */
  function pendingCard(p) {
    const card = el('div', { class: 'sv7 pend7', html: RACK_SVG }, [
      el('div', { class: 'sv-h' }, [
        el('span', { class: 'sv-dot' }),
        el('div', { class: 'sv-id' }, [
          el('b', { text: p.name }),
          el('em', { text: `port ${p.port} · set up ${ago(p.created)}` }),
        ]),
        el('span', { class: 'sv-tag', text: 'waiting' }),
      ]),
      el('div', { class: 'sv-wait' }, 'Has not run the command yet.'),
      el('div', { class: 'sv-f' }, [
        el('span', { class: 'sp' }),
        el('button', { class: 'btn7 warn', text: 'Withdraw' }),
      ]),
    ]);
    card.querySelector('.btn7.warn').addEventListener('click', async () => {
      try {
        paint(await api.nodeRemove(p.name));
        toast(`The command for ${p.name} was withdrawn.`);
      } catch (e) { oops(e); }
    });
    return card;
  }

  /* Painting without rebuilding.
   *
   * The obvious paint clears the grid and makes every card again. It is also
   * wrong: while a server is being added this runs every two seconds, so every
   * card is destroyed and recreated on a timer — the animations replay, the
   * text reflows, and the whole page looks like it is reloading under the
   * operator while they are trying to read a command off it.
   *
   * So the grid is reconciled instead. A card whose content has not changed is
   * left alone — not re-rendered, not re-animated, not moved — and only the
   * ones that actually differ are rebuilt. A signature per card is enough to
   * tell: everything drawn on it is in there, and nothing that changes on its
   * own is.
   */
  const cards = new Map();   // name -> { el, sig }

  const sigOf = (kind, d) => kind + '|' + JSON.stringify(kind === 'node'
    ? [d.online, d.port, d.listening, d.enrolled, d.lastSeen, d.tunnels || [], d.info || {}]
    : [d.port, d.created]);

  function reconcile(nodes, pending) {
    const want = [
      ...nodes.map(n => ({ name: n.name, kind: 'node', data: n })),
      // The server being set up is already on screen as the draft card; drawing
      // it again as a pending one would show it twice.
      ...pending
        .filter(p => !draft || draft.dataset.for !== p.name)
        .map(p => ({ name: p.name, kind: 'pend', data: p })),
    ];
    const keep = new Set(want.map(w => w.name));

    for (const [name, held] of cards) {
      if (!keep.has(name)) { held.el.remove(); cards.delete(name); }
    }

    // The draft always leads: it is the one the operator is working on.
    let at = draft && draft.isConnected ? draft : null;
    if (draft && !draft.isConnected) { fleet.prepend(draft); at = draft; }

    for (const w of want) {
      const sig = sigOf(w.kind, w.data);
      let held = cards.get(w.name);
      if (!held || held.sig !== sig) {
        const fresh = w.kind === 'node' ? nodeCard(w.data) : pendingCard(w.data);
        // A card that is only being updated does not arrive again.
        if (held) fresh.style.animation = 'none';
        if (held) held.el.replaceWith(fresh);
        else if (at) at.after(fresh);
        else fleet.prepend(fresh);
        held = { el: fresh, sig };
        cards.set(w.name, held);
      } else if (at ? held.el.previousElementSibling !== at : fleet.firstElementChild !== held.el) {
        // Order changed — move it rather than rebuild it.
        if (at) at.after(held.el); else fleet.prepend(held.el);
      }
      at = held.el;
    }
  }

  /* What each connected server is actually running.
   *
   * Asked for after the rows are on screen, and one request per server rather
   * than one for the fleet: a server that has gone quiet between the list and
   * this call should cost its own count and nothing else. A failure leaves the
   * row as it was — the tag already says something true. */
  async function countTunnels(nodes) {
    // Not while the add-a-server poll is running: that repaints every two
    // seconds to watch for one new row, and counting the whole fleet each time
    // would be a request per server per tick for an answer nobody is waiting on.
    if (watch) return;
    await Promise.all(nodes.filter(n => n.online).map(async n => {
      try {
        const r = await api.nodeTunnels(n.name);
        const card = [...fleet.querySelectorAll('.sv7')]
          .find(c => c.querySelector('.sv-id b')?.textContent === n.name);
        // By label, not by position: the facts have been reordered once
        // already, and the first time it wrote the tunnel count into the port.
        const cell = [...(card?.querySelectorAll('.sv-fact') || [])]
          .find(f => f.querySelector('span')?.textContent === 'Tunnels')
          ?.querySelector('b');
        if (cell) cell.textContent = String((r.tunnels || []).length);
      } catch (e) { /* the row keeps saying "connected", which is still true */ }
    }));
  }

  /* ---- the listener ---- */
  sw.addEventListener('click', async () => {
    sw.disabled = true;
    try {
      if (sw.classList.contains('on')) {
        paint(await api.nodeListenerOff());
        dropDraft();
        stopWatch();
      } else {
        const st = await api.nodeListenerOn();
        paint(st);
        if (st.warning) toast(st.warning, true);
      }
    } catch (e) { oops(e); } finally { sw.disabled = false; }
  });

  /* ---- adding one ------------------------------------------------------------
   * The new server is built where it will live: a card at the front of the grid
   * that starts as a form, becomes the command to run, and is replaced by the
   * real thing the moment that server calls in. One place, three states, so the
   * operator never loses track of which server they are setting up.
   */
  let draft = null;              // the card, while there is one
  const dropDraft = () => { draft?.remove(); draft = null; stopWatch(); };

  addB.addEventListener('click', () => {
    if (draft) { dropDraft(); return; }
    draft = draftCard();
    fleet.prepend(draft);
    $('#nempty', root).hidden = true;
    draft.querySelector('#dfName').focus();
  });

  function draftCard() {
    const card = el('div', { class: 'sv7 draft7' });
    card.innerHTML = `
      <div class="sv-h">
        <span class="sv-dot"></span>
        <div class="sv-id"><b>New server</b></div>
        <button class="sv-x" aria-label="Cancel">&times;</button>
      </div>
      <div class="df-body">
        <label class="df-f">
          <span>Name</span>
          <input id="dfName" maxlength="40" placeholder="kharej-de" autocomplete="off">
        </label>
        <label class="df-f">
          <span>Port</span>
          <input id="dfPort" inputmode="numeric" maxlength="5" autocomplete="off">
        </label>
        <p class="df-hint">Open this port in the firewall.</p>
      </div>
      <div class="sv-f">
        <span class="sp"></span>
        <button class="btn7 solid" id="dfGo">Get the command</button>
      </div>`;
    card.querySelector('#dfPort').value = suggested || '';
    card.querySelector('.sv-x').addEventListener('click', dropDraft);
    card.querySelector('#dfGo').addEventListener('click', () => submitDraft(card));
    card.querySelectorAll('input').forEach(i =>
      i.addEventListener('keydown', ev => { if (ev.key === 'Enter') submitDraft(card); }));
    return card;
  }

  async function submitDraft(card) {
    const name = card.querySelector('#dfName').value.trim();
    const port = Number(card.querySelector('#dfPort').value.trim());
    if (!name) { toast('Give the server a name first.', true); card.querySelector('#dfName').focus(); return; }
    if (!port || port < 1 || port > 65535) {
      toast('Choose a port between 1 and 65535.', true); card.querySelector('#dfPort').focus(); return;
    }
    const go = card.querySelector('#dfGo');
    go.disabled = true;
    try {
      const res = await api.nodeAdd(name, port);
      showCommand(card, name, res);
      paint(await api.nodes());
      watchFor(name);
    } catch (e) { oops(e); go.disabled = false; }
  }

  function showCommand(card, name, res) {
    card.classList.add('cmd7c');
    card.dataset.for = name;
    card.innerHTML = `
      <div class="sv-h">
        <span class="sv-dot"></span>
        <div class="sv-id"><b>${name}</b><em>port ${res.port} · waiting for it to call in</em></div>
        <button class="sv-x" aria-label="Cancel">&times;</button>
      </div>
      <div class="df-cmd">
        <div class="df-ch"><b>Run this on that server, as root</b>
          <span class="sp"></span><button class="btn7" id="dfCopy">Copy</button></div>
        <code id="dfVal"></code>
        <div class="alt7">
          <span>Already has Backpack? <button class="lnk7" id="dfAlt">use the short line</button></span>
          <code id="dfVals" hidden></code>
        </div>
      </div>
      <div class="wait7" id="dfWait"><i></i><span>Waiting for it to connect…</span></div>`;
    card.querySelector('#dfVal').textContent = res.command;
    card.querySelector('#dfVals').textContent = res.commandShort || '';
    card.querySelector('.sv-x').addEventListener('click', async () => {
      // Cancelling here withdraws the token, not just the card: a command that
      // has been handed out is live until it is taken back.
      try { paint(await api.nodeRemove(name)); } catch (e) { /* it may never have landed */ }
      dropDraft();
    });
    card.querySelector('#dfAlt').addEventListener('click', () => {
      const alt = card.querySelector('#dfVals');
      alt.hidden = !alt.hidden;
      card.querySelector('#dfAlt').textContent = alt.hidden ? 'use the short line' : 'hide it';
    });
    card.querySelector('#dfCopy').addEventListener('click', async () => {
      const alt = card.querySelector('#dfVals');
      const text = (alt.hidden ? card.querySelector('#dfVal') : alt).textContent.trim();
      try {
        await navigator.clipboard.writeText(text);
        const b = card.querySelector('#dfCopy');
        b.textContent = 'Copied';
        setTimeout(() => { b.textContent = 'Copy'; }, 1400);
      } catch (e) {
        /* The clipboard is refused on an insecure origin, which is how a panel
           on a bare IP is reached. The line is on screen and selectable. */
        toast('Select the line and copy it — this browser will not do it for us.', true);
      }
    });
  }

  /* Watch for a freshly added server to call in. It stops on its own: an
     operator who walked away should not leave a poll running all night. */
  function watchFor(name) {
    if (watch) clearInterval(watch);
    let left = 60;
    watch = setInterval(async () => {
      if (--left <= 0) {
        stopWatch();
        const w = draft?.querySelector('#dfWait span');
        if (w) w.textContent =
          'Still not connected. The command is good for a day — check that port is open.';
        return;
      }
      try {
        const state = await api.nodes();
        if ((state.nodes || []).some(n => n.name === name)) {
          stopWatch();
          // The card it was set up in is replaced by the server itself.
          draft?.remove();
          draft = null;
          toast(`${name} is connected.`);
        }
        paint(state);
      } catch (e) { /* a blip is not worth a toast on a poll */ }
    }, 2000);
  }

  /* Failures are reported, not swallowed. A paint that throws used to reach
     oops() and become a toast that looked like a server problem, while the
     screen sat on its defaults saying the listener was off — so the one place
     the bug was visible said the opposite of what was true. */
  return api.nodes().then(paint).catch(e => { console.error('servers:', e); oops(e); });
}

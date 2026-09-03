/* Servers — the managed fleet.
 *
 * One of the panel's two sections, beside the tunnels, so it is a page in #view
 * rather than a dialog over one.
 *
 * It used to hand the operator a line to run on the other machine and then
 * watch for that machine to appear. It does not any more: the panel logs into
 * the server over its own SSH, which is already running and already how that
 * machine is administered. So adding a server is a form and an answer, the way
 * everything else in the panel is, and there is nothing to carry anywhere.
 *
 * CLI: nothing. There is no Backpack state on a managed server to configure.
 */

import { $, el, esc } from '../lib/dom.js';
import { toast, oops } from '../ui/toast.js';
import { confirmBox } from '../ui/confirm.js';
import * as api from '../api.js';
import * as store from '../store.js';

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
    ? `The tunnel built there from this panel (<b>${esc(n.tunnels[0])}</b>) keeps running`
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
      <b>Managed servers</b>
      <span id="lisub">Off.</span>
    </div>
    <button class="sw7" id="nsw" aria-pressed="false" aria-label="Managed servers"></button>
  </div>

  <form class="addsv" id="addform" hidden autocomplete="off">
    <div class="asv-h"><b>Add a server</b>
      <span>The panel logs in over SSH. Nothing has to be run on that machine.</span></div>
    <div class="asv-g">
      <label>Name<input name="name" placeholder="kharej" autocomplete="off" required></label>
      <label>Address<input name="host" placeholder="203.0.113.9" autocomplete="off" required></label>
      <label>SSH port<input name="sshPort" type="number" min="1" max="65535" value="22"></label>
      <label>Username<input name="user" value="root" autocomplete="off"></label>
      <label class="wide">Password<input name="password" type="password"
        placeholder="the root password for that server" autocomplete="new-password" required></label>
    </div>
    <div class="asv-f">
      <span class="asv-note" id="asvnote">The password is kept on this server only, readable by root.</span>
      <span class="sp"></span>
      <button type="button" class="btn7" id="asvcancel">Cancel</button>
      <button type="submit" class="btn7 solid" id="asvgo">Add it</button>
    </div>
  </form>

  <div class="behind7" id="behind" hidden>
    <div class="t7">
      <b id="behindt">Servers behind this panel</b>
      <span id="behinds"></span>
    </div>
    <button class="btn7 solid" id="upall">Upgrade them</button>
  </div>

  <div id="fleet" class="grid3"></div>

  <div class="empty7" id="nempty" hidden>
    <svg class="x" viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="14" width="18" height="7" rx="2"/></svg>
    <b>No servers yet</b>
    <span>Add one above, and the panel will manage its tunnels from here.</span>
  </div>
</div>`;

export function serversView(ctx) {
  const view = $('#view');
  view.innerHTML = SHELL;

  const root   = view;
  const sw     = $('#nsw', root);
  const addB   = $('#naddb', root);
  const form   = $('#addform', root);
  const note   = $('#asvnote', root);
  const goB    = $('#asvgo', root);
  const fleet  = $('#fleet', root);

  /* ---- painting ---- */
  function paint(state) {
    const on = !!state.enabled;
    sw.classList.toggle('on', on);
    sw.setAttribute('aria-pressed', String(on));
    addB.disabled = !on;
    addB.title = on ? '' : 'Turn managed servers on first';
    if (!on) { form.hidden = true; }

    const nodes = state.nodes || [];
    $('#lisub', root).textContent = on
      ? (nodes.length
          ? `Managing ${nodes.length === 1 ? '1 server' : nodes.length + ' servers'}.`
          : 'On.')
      : 'Off.';
    $('#nempty', root).hidden = !!(nodes.length || !form.hidden);
    $('#nCount', root).textContent = String(nodes.length);

    reconcile(nodes);
    behind(nodes);

    const dock = $('#dock-s');
    if (dock) dock.textContent = nodes.length ? String(nodes.length) : '';
  }

  /* One server, as a card.
   *
   * A card rather than a row because a server is not a line item: it has a
   * name, an address, a machine underneath it and a job, and a row makes the
   * operator read all of that sideways.
   *
   * What it says is what the far machine reports about itself — its Backpack
   * version, its system, how long it has been up — because that is the reason
   * to look at this page when nothing is wrong. There is no port on it any
   * more; nothing here listens.
   */
  function nodeCard(n) {
    const i = n.info || {};
    const built = (n.tunnels || []).length;
    const dash = v => (v && v !== '-' ? v : '—');

    const card = el('div', { class: 'sv7' + (n.online ? ' on7' : ''), html: RACK_SVG }, [
      el('div', { class: 'sv-h' }, [
        el('span', { class: 'sv-dot' }),
        el('div', { class: 'sv-id' }, [
          el('b', { text: n.name }),
          el('em', { text: i.hostname || n.host }),
        ]),
        el('span', { class: 'sv-tag', text: n.online ? 'reachable' : 'unreachable' }),
      ]),
      el('div', { class: 'sv-addr' }, [
        el('span', { text: dash(i.ipv4) !== '—' ? i.ipv4 : n.host }),
        i.ipv6 && i.ipv6 !== '-' ? el('em', { text: i.ipv6 }) : null,
      ]),
      /* Why it cannot be reached, when it cannot. A server that is down and a
         server whose password was changed are both "unreachable" and are not
         the same problem, and this is the only place that can say which. */
      n.online ? null : el('div', { class: 'sv-why' }, n.why || 'It did not answer.'),
      el('div', { class: 'sv-facts' }, [
        fact7('Backpack', dash(i.version)),
        fact7('System', i.distro || (i.os && i.arch ? `${i.os}/${i.arch}` : dash(i.os))),
        fact7('Tunnels', built ? String(built) : '—'),
        fact7('Uptime', dash(i.uptime)),
      ]),
      el('div', { class: 'sv-f' }, [
        el('span', { class: 'sub7', text: `${n.user}@${n.host}${n.sshPort && n.sshPort !== 22 ? ':' + n.sshPort : ''}` }),
        el('span', { class: 'sp' }),
        el('button', { class: 'btn7', text: 'Upgrade', title: 'Install the current release on this server' }),
        el('button', { class: 'btn7', text: 'Login' }),
        el('button', { class: 'btn7 warn', text: 'Remove' }),
      ]),
    ]);

    const confirm = el('div', { class: 'cf7' }, [
      el('p', { html: `Stop managing <b>${esc(n.name)}</b>? ${builtThere(n)} — this panel just loses the way to change them.` }),
      el('button', { class: 'btn7 warn', text: 'Remove' }),
      el('button', { class: 'btn7', text: 'Cancel' }),
    ]);
    card.append(confirm);

    const [upB, loginB, rmB] = card.querySelectorAll('.sv-f button');

    upB.addEventListener('click', async () => {
      if (!await confirmBox({
        title: `Upgrade ${n.name}?`,
        body: 'The current release is installed there and its tunnels restart once. '
            + 'It takes a couple of minutes, and this page waits for it.',
        go: 'Upgrade' })) return;
      upB.disabled = true; upB.textContent = 'Upgrading…';
      try {
        paint(await api.nodeUpgrade(n.name));
        toast(`${n.name} is on the current release.`);
      } catch (e) { oops(e); upB.disabled = false; upB.textContent = 'Upgrade'; }
    });

    loginB.addEventListener('click', () => openCredentials(n));

    rmB.addEventListener('click', () => card.classList.add('arm7'));
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

  /* A release lands on this panel and every managed server is then a version
     behind. Said once, above the fleet, with the one action that fixes it —
     rather than as a badge on each card that has to be acted on one at a time.
     Only for servers that answered: one that is unreachable is a different
     problem and upgrading it is not the fix. */
  function behind(nodes) {
    const mine = (store.get().stats?.version || '').trim();
    const old = nodes.filter(n => n.online && n.info?.version && mine && n.info.version !== mine);
    const bar = $('#behind', root);
    bar.hidden = !old.length;
    if (!old.length) return;
    $('#behindt', root).textContent = old.length === 1
      ? `${old[0].name} is not on ${mine}`
      : `${old.length} servers are not on ${mine}`;
    $('#behinds', root).textContent = old.map(n => `${n.name} ${n.info.version}`).join(' · ');
  }

  $('#upall', root).addEventListener('click', async () => {
    const b = $('#upall', root);
    if (!await confirmBox({
      title: 'Upgrade every server behind this panel?',
      body: 'Each one installs the current release and its tunnels restart once. '
          + 'They run together, and one that fails does not stop the others.',
      go: 'Upgrade them' })) return;
    b.disabled = true; b.textContent = 'Upgrading…';
    try {
      const state = await api.nodeUpgradeAll();
      paint(state);
      toast(state.warning ? state.warning : 'Every server is on the current release.');
    } catch (e) { oops(e); } finally { b.disabled = false; b.textContent = 'Upgrade them'; }
  });

  const fact7 = (k, v, warn) => el('div', { class: 'sv-fact' + (warn ? ' bad7' : '') }, [
    el('span', { text: k }), el('b', { text: v }),
    warn ? el('i', { text: warn }) : null,
  ]);

  /* Changing how a server is reached.
   *
   * The password is never sent back to the browser, so this asks for it again
   * rather than showing a field that looks filled in and is not. Leaving it
   * empty keeps the one that is stored, which is what an operator changing only
   * the address means. */
  function openCredentials(n) {
    const box = el('form', { class: 'addsv open7', autocomplete: 'off' }, [
      el('div', { class: 'asv-h' }, [
        el('b', { text: `How the panel reaches ${n.name}` }),
        el('span', { text: 'Leave the password blank to keep the one already stored.' }),
      ]),
      el('div', { class: 'asv-g', html:
        `<label>Address<input name="host" value="${esc(n.host)}" autocomplete="off"></label>
         <label>SSH port<input name="sshPort" type="number" min="1" max="65535" value="${n.sshPort || 22}"></label>
         <label>Username<input name="user" value="${esc(n.user)}" autocomplete="off"></label>
         <label class="wide">New password<input name="password" type="password"
           placeholder="unchanged" autocomplete="new-password"></label>` }),
      el('div', { class: 'asv-f' }, [
        el('span', { class: 'asv-note', text: 'Changing the address forgets the host key, as a new machine is entitled to a new one.' }),
        el('span', { class: 'sp' }),
        el('button', { type: 'button', class: 'btn7', text: 'Cancel' }),
        el('button', { type: 'submit', class: 'btn7 solid', text: 'Save' }),
      ]),
    ]);
    const held = cards.get(n.name);
    if (!held) return;
    held.el.after(box);
    box.querySelector('button').addEventListener('click', () => box.remove());
    box.addEventListener('submit', async ev => {
      ev.preventDefault();
      const f = Object.fromEntries(new FormData(box));
      const btn = box.querySelector('button[type=submit]');
      btn.disabled = true; btn.textContent = 'Saving…';
      try {
        const state = await api.nodeCredentials({ name: n.name, ...f });
        box.remove();
        paint(state);
        toast(`${n.name} updated.`);
      } catch (e) { oops(e); btn.disabled = false; btn.textContent = 'Save'; }
    });
  }

  /* ---- the add form ---- */
  addB.addEventListener('click', () => {
    form.hidden = false;
    $('#nempty', root).hidden = true;
    form.querySelector('input[name=name]').focus();
  });
  $('#asvcancel', root).addEventListener('click', () => { form.hidden = true; form.reset(); });

  form.addEventListener('submit', async ev => {
    ev.preventDefault();
    const fields = Object.fromEntries(new FormData(form));
    goB.disabled = true;
    goB.textContent = 'Reaching it…';
    /* Adding can take minutes rather than seconds, because a server with no
       Backpack on it gets one. Said plainly while it happens: a button that sits
       there for two minutes with no explanation is one people press again. */
    note.textContent = 'Logging in, and installing Backpack if that server has none. '
                     + 'This can take a couple of minutes.';
    try {
      const state = await api.nodeAdd(fields);
      form.hidden = true; form.reset();
      paint(state);
      toast(`${fields.name} is managed from here now.`);
    } catch (e) {
      oops(e);
    } finally {
      goB.disabled = false;
      goB.textContent = 'Add it';
      note.textContent = 'The password is kept on this server only, readable by root.';
    }
  });

  /* ---- the toggle ---- */
  sw.addEventListener('click', async () => {
    const on = sw.classList.contains('on');
    sw.disabled = true;
    try {
      paint(await (on ? api.nodeListenerOff() : api.nodeListenerOn()));
    } catch (e) { oops(e); } finally { sw.disabled = false; }
  });

  /* ---- keeping the grid still ----
   *
   * The page polls, and rebuilding the grid on every answer meant every card
   * arriving again: the entrance animation replayed, text reflowed, and the
   * whole page looked like it was reloading under the operator.
   *
   * So the grid is reconciled instead. A card whose content has not changed is
   * left alone — not re-rendered, not re-animated, not moved — and only the
   * ones that actually differ are rebuilt. A signature per card is enough to
   * tell: everything drawn on it is in there, and nothing that changes on its
   * own is.
   */
  const cards = new Map();   // name -> { el, sig }

  const sigOf = d => JSON.stringify([d.online, d.why, d.host, d.user, d.sshPort,
    d.lastSeen, d.tunnels || [], d.info || {}]);

  function reconcile(nodes) {
    const keep = new Set(nodes.map(n => n.name));
    for (const [name, held] of cards) {
      if (!keep.has(name)) { held.el.remove(); cards.delete(name); }
    }
    let at = null;
    for (const n of nodes) {
      const sig = sigOf(n);
      let held = cards.get(n.name);
      if (!held || held.sig !== sig) {
        const fresh = nodeCard(n);
        // A card that is only being updated does not arrive again.
        if (held) { fresh.style.animation = 'none'; held.el.replaceWith(fresh); }
        else if (at) at.after(fresh);
        else fleet.prepend(fresh);
        held = { el: fresh, sig };
        cards.set(n.name, held);
      } else if (at ? held.el.previousElementSibling !== at : fleet.firstElementChild !== held.el) {
        // Order changed — move it rather than rebuild it.
        if (at) at.after(held.el); else fleet.prepend(held.el);
      }
      at = held.el;
    }
  }

  /* ---- the poll ---- */
  let timer = null;
  const tick = async () => {
    try { paint(await api.nodes()); } catch (e) { /* the page keeps what it has */ }
  };
  tick();
  timer = setInterval(tick, 6000);
  ctx.setTeardown(() => clearInterval(timer));
}

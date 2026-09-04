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

/* The ground every server card sits on.
 *
 * Two wide roads across, two down, a scatter of smaller streets, some blocks
 * and a pin in the middle. The roads are drawn on rather than faded in — the
 * same stroke-dashoffset trick the sparklines use — so the card is surveyed
 * once as it arrives rather than switched on.
 *
 * It is not this server's map. It is texture behind an address; see nodeCard.
 *
 * Drawn at a fixed size and cropped by the card, the way a background image
 * sits at its natural size rather than being stretched to the box.
 *
 * It was scaled to the card before, which was wrong twice. Stretched to the
 * card's aspect it skewed every angle and thickened the roads along one axis;
 * scaled uniformly to cover, it zoomed — the same street plan came out twice
 * the size on a wide card as on a narrow one, so two cards side by side had
 * visibly different ground. At a fixed size neither happens: every card shows
 * the same streets at the same scale, and a wider one simply shows more of
 * them. It is drawn larger than any card gets, so there is always more to
 * show. */
const MAP_SVG = `<svg width="680" height="340" viewBox="0 0 680 340" aria-hidden="true">
  <g class="rd">
    <line x1="0" y1="118" x2="680" y2="118" stroke-width="4" style="--i:0"/>
    <line x1="0" y1="222" x2="680" y2="222" stroke-width="4" style="--i:1"/>
    <line x1="204" y1="0" x2="204" y2="340" stroke-width="3" style="--i:2"/>
    <line x1="476" y1="0" x2="476" y2="340" stroke-width="3" style="--i:3"/>
  </g>
  <g class="st">
    <line x1="0" y1="66" x2="680" y2="66" style="--i:4"/>
    <line x1="0" y1="170" x2="680" y2="170" style="--i:5"/>
    <line x1="0" y1="274" x2="680" y2="274" style="--i:6"/>
    <line x1="96"  y1="0" x2="96"  y2="340" style="--i:7"/>
    <line x1="286" y1="0" x2="286" y2="340" style="--i:8"/>
    <line x1="394" y1="0" x2="394" y2="340" style="--i:9"/>
    <line x1="574" y1="0" x2="574" y2="340" style="--i:10"/>
  </g>
  <g class="bl">
    <rect x="118" y="136" width="62" height="60" rx="3" style="--i:0"/>
    <rect x="226" y="30"  width="46" height="46" rx="3" style="--i:1"/>
    <rect x="500" y="240" width="60" height="52" rx="3" style="--i:2"/>
    <rect x="512" y="76"  width="40" height="66" rx="3" style="--i:3"/>
    <rect x="26"  y="188" width="34" height="38" rx="3" style="--i:4"/>
    <rect x="304" y="248" width="54" height="30" rx="3" style="--i:5"/>
    <rect x="596" y="150" width="44" height="44" rx="3" style="--i:6"/>
    <rect x="42"  y="26"  width="38" height="26" rx="3" style="--i:7"/>
  </g>
  <g class="pin">
    <path d="M340 128c-13 0-23 10-23 23 0 17 23 43 23 43s23-26 23-43c0-13-10-23-23-23z"/>
    <circle cx="340" cy="151" r="8"/>
  </g>
</svg>`;

const MAPICON_SVG = `<svg viewBox="0 0 24 24" aria-hidden="true">
  <polygon points="3 6 9 3 15 6 21 3 21 18 15 21 9 18 3 21"/>
  <line x1="9" y1="3" x2="9" y2="18"/><line x1="15" y1="6" x2="15" y2="21"/></svg>`;

const SHELL = `
<div class="np7">
  <div class="sech2">
    <h2>Servers</h2>
    <span class="cnt" id="nCount">0</span>
    <span class="sp"></span>
    <button class="sb primary" id="naddb">Add a server</button>
  </div>

  <form class="addsv" id="addform" hidden autocomplete="off">
    <div class="asv-map">${MAP_SVG}</div>
    <div class="asv-body">
      <div class="asv-h">
        <span class="asv-ic">${MAPICON_SVG}</span>
        <div>
          <b>Add a server</b>
          <span>The panel logs in over SSH — the four things ssh itself asks for.
                Nothing has to be run on that machine.</span>
        </div>
      </div>

      <div class="asv-g">
        <label class="f1"><span>Name</span>
          <input name="name" placeholder="kharej" autocomplete="off" required></label>
        <label class="f2"><span>Address</span>
          <input name="host" placeholder="203.0.113.9" autocomplete="off" required></label>
        <label class="f3"><span>SSH port</span>
          <input name="sshPort" type="number" min="1" max="65535" value="22"></label>
        <label class="f4"><span>Username</span>
          <input name="user" value="root" autocomplete="off"></label>
        <label class="f5"><span>Password</span>
          <input name="password" type="password" placeholder="that user's password"
                 autocomplete="new-password" required></label>
      </div>

      <div class="asv-f">
        <span class="asv-note" id="asvnote">Kept on this server only, readable by root.</span>
        <span class="sp"></span>
        <button type="button" class="btn7" id="asvcancel">Cancel</button>
        <button type="submit" class="btn7 solid" id="asvgo">Add it</button>
      </div>
    </div>
  </form>

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
  const addB   = $('#naddb', root);
  const form   = $('#addform', root);
  const note   = $('#asvnote', root);
  const goB    = $('#asvgo', root);
  const fleet  = $('#fleet', root);

  /* ---- painting ---- */
  function paint(state) {
    const nodes = state.nodes || [];
    $('#nempty', root).hidden = !!(nodes.length || !form.hidden);
    $('#nCount', root).textContent = String(nodes.length);

    reconcile(nodes);
    behind(nodes);

    const dock = $('#dock-s');
    if (dock) dock.textContent = nodes.length ? String(nodes.length) : '';
  }

  /* One server, as a card.
   *
   * The same card as a tunnel's, because they sit in the same grid and a
   * fleet page whose cards are a different size and shape from the tunnels
   * page reads as a different product. Same shell, same height, same bottom
   * band of actions.
   *
   * What differs is what is behind it. A tunnel card has its chart there; a
   * server card has a map, because the thing a managed server has that a
   * tunnel does not is a place. Clicking draws it — graph paper gives way to
   * roads, blocks and a pin.
   *
   * The map is a drawing, not a map. It has no idea where the server is and
   * does not pretend to: no tiles are fetched, nothing is geocoded, and the
   * roads are the same roads on every card. It is texture behind an address,
   * and saying so here is cheaper than somebody later believing it.
   */
  function nodeCard(n) {
    const i = n.info || {};
    const built = (n.tunnels || []).length;
    const dash = v => (v && v !== '-' ? v : '—');
    const login = `${n.user}@${n.host}${n.sshPort && n.sshPort !== 22 ? ':' + n.sshPort : ''}`;
    const mine = (store.get().stats?.version || '').trim();
    const behind = n.online && i.version && mine && i.version !== mine;

    const card = el('div', {
      class: 'mp7' + (n.online ? ' live' : ''),
      'data-name': n.name,
    }, [
      el('div', { class: 'mp-field' }, [
        el('div', { class: 'mp-map', html: MAP_SVG }),
        el('div', { class: 'mp-wash' }),
      ]),

      el('div', { class: 'mp-in' }, [
        el('div', { class: 'mp-top' }, [
          el('span', { class: 'mp-ic', html: MAPICON_SVG }),
          el('div', { class: 'mp-id' }, [
            el('b', { text: n.name }),
            el('small', { text: i.hostname || n.host }),
          ]),
          el('span', { class: 'mp-pill' }, [
            el('i'), el('span', { text: n.online ? 'Reachable' : 'Unreachable' }),
          ]),
        ]),

        el('div', { class: 'mp-addr' }, [
          el('b', { text: dash(i.ipv4) !== '—' ? i.ipv4 : n.host }),
          i.ipv6 && i.ipv6 !== '-' ? el('em', { text: i.ipv6 }) : null,
        ]),

        n.online ? null : el('div', { class: 'mp-why', text: n.why || 'It did not answer.' }),

        el('div', { class: 'sp' }),

        /* Two facts, in one strip above the actions: which Backpack is on that
           machine, and how long it has been up. The system name and the tunnel
           count were here too — the first is a thing you learn once, and the
           second is on the tunnels page beside the tunnels it counts. */
        el('div', { class: 'mp-facts' }, [
          fact7('Backpack', dash(i.version)),
          fact7('Uptime', dash(i.uptime)),
        ]),

        el('div', { class: 'mp-rule' }),
      ]),

      el('div', { class: 'mp-foot' }, [
        el('span', { class: 'mp-login', text: login }),
        el('span', { class: 'sp' }),
        /* Upgrade only when there is something to upgrade to. A button that is
           always there and usually does nothing is a button people stop
           reading; this one appears when the panel has moved on and the server
           has not, and says which version it would install. */
        behind ? el('button', { class: 'btn7 solid', text: `Upgrade to ${mine}`,
                                title: `That server is on ${i.version || 'an older build'}` }) : null,
        el('button', { class: 'btn7', text: 'Refresh', title: 'Ask it again, now' }),
        el('button', { class: 'btn7', text: 'Edit', title: 'Address, port, username, password' }),
        el('button', { class: 'btn7 warn', text: 'Remove' }),
      ]),
    ]);

    const confirm = el('div', { class: 'cf7' }, [
      el('p', { html: `Stop managing <b>${esc(n.name)}</b>? ${builtThere(n)} — this panel just loses the way to change them.` }),
      el('button', { class: 'btn7 warn', text: 'Remove' }),
      el('button', { class: 'btn7', text: 'Cancel' }),
    ]);
    card.append(confirm);

    /* The tilt follows the pointer and springs back when it leaves. A surface
       effect only: it moves nothing that has to be read or clicked. */
    const clamp = v => Math.max(-1, Math.min(1, v));
    card.addEventListener('mousemove', ev => {
      const r = card.getBoundingClientRect();
      /* Clamped, because a pointer just outside the card still fires this on
         the way past, and an unclamped ratio turned eight degrees into forty. */
      const dx = clamp((ev.clientX - (r.left + r.width / 2)) / (r.width / 2));
      const dy = clamp((ev.clientY - (r.top + r.height / 2)) / (r.height / 2));
      card.style.setProperty('--ry', `${(dx * 4).toFixed(2)}deg`);
      card.style.setProperty('--rx', `${(-dy * 4).toFixed(2)}deg`);
    });
    card.addEventListener('mouseleave', () => {
      card.style.setProperty('--rx', '0deg');
      card.style.setProperty('--ry', '0deg');
    });

    const btns = [...card.querySelectorAll('.mp-foot button')];
    const upB = behind ? btns.shift() : null;
    const [refreshB, editB, rmB] = btns;

    upB?.addEventListener('click', async () => {
      if (!await confirmBox({
        title: `Upgrade ${n.name} to ${mine}?`,
        body: `It is on ${i.version}. The release is installed there and its tunnels `
            + 'restart once. It takes a couple of minutes, and this page waits for it.',
        go: 'Upgrade' })) return;
      upB.disabled = true; upB.textContent = 'Upgrading…';
      try {
        paint(await api.nodeUpgrade(n.name));
        toast(`${n.name} is on ${mine}.`);
      } catch (e) { oops(e); upB.disabled = false; upB.textContent = `Upgrade to ${mine}`; }
    });

    /* Ask it again, now. The fleet is polled and each answer stands for a
       short while, so after changing something on that machine there is a gap
       where the card still shows what it said before. */
    refreshB.addEventListener('click', async () => {
      refreshB.disabled = true; refreshB.textContent = 'Asking…';
      try {
        paint(await api.nodeRefresh(n.name));
      } catch (e) { oops(e); } finally {
        refreshB.disabled = false; refreshB.textContent = 'Refresh';
      }
    });

    editB.addEventListener('click', () => openCredentials(n));

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

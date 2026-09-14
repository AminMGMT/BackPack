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
import { bytes } from '../lib/format.js';

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

/* The ground the add-a-server form sits on.
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

    const dock = $('#dock-s');
    if (dock) dock.textContent = nodes.length ? String(nodes.length) : '';
  }

  /* One server, as a card.
   *
   * It used to carry a street map behind it — texture rather than a place, and
   * honest in its own note about knowing nothing of where the server was. What
   * it cost was height: the card stood as tall as a tunnel's, and a fleet of
   * four would not fit on a screen. Comparing four servers at a glance is what
   * this page is for, so the drawing went and the card closed up around what it
   * actually says.
   *
   * The shell stays the tunnel card's — same material, same corner, same bottom
   * band — because the two are what this panel is made of. Only the height
   * differs now, and they are never on screen together.
   *
   * Two of the things on it move on every poll: the processor and memory
   * readings, and the loss and round trip measured to it. Those are written
   * into the card that already exists rather than drawn by rebuilding it; see
   * paintLive and the signature below.
   */
  function nodeCard(n) {
    const i = n.info || {};
    const dash = v => (v && v !== '-' ? v : '—');
    const mine = (store.get().stats?.version || '').trim();
    const behind = n.online && i.version && mine && i.version !== mine;

    /* One line of identity under the name: the address, where it is, which
       Backpack it runs, how long it has been up. They were four separate
       blocks — an oversized address, a two-cell facts grid, a decorative map
       behind all of it — on a card tall enough that a fleet of four did not
       fit on a screen. They are facts of one or two words each; a row of them
       reads faster than a layout of them. */
    const meta = [
      dash(i.ipv4) !== '—' ? i.ipv4 : n.host,
      i.country || i.city || '',
      i.version ? 'v' + String(i.version).replace(/^v/, '') : '',
    ].filter(Boolean);

    const card = el('div', {
      class: 'mp7' + (n.online ? ' live' : '') + (n.pending ? ' pend' : ''),
      'data-name': n.name,
    }, [
      el('div', { class: 'mp-in' }, [
        el('div', { class: 'mp-top' }, [
          el('div', { class: 'mp-id' }, [
            el('b', { text: n.name }),
            el('small', { text: i.hostname || n.host }),
          ]),
          el('span', { class: 'mp-pill' }, [
            el('i'),
            el('span', { text: n.pending ? 'Checking' : (n.online ? 'Reachable' : 'Unreachable') }),
          ]),
        ]),

        el('div', { class: 'mp-meta' },
          meta.map((v, k) => el('span', { class: k ? 'q' : 'a', text: v }))
            .concat([el('span', { class: 'q', 'data-up': '', text: dash(i.uptime) })])),

        n.online || n.pending ? null
          : el('div', { class: 'mp-why', text: n.why || 'It did not answer.' }),

        el('div', { class: 'sp' }),

        /* What the machine is doing, not only what it is. Read on that machine
           when the panel asks it; nothing here can see another server's
           processor.

           The values are written into these elements on every poll rather than
           the card being rebuilt around them — see paintLive. Drawn for a
           pending row too, from the last reading, so the card does not change
           shape a second later when the live answer lands. */
        el('div', { class: 'mp-load' }, [
          meter7('cpu', 'Processor', i.cpuPercent,
            i.cpuCores ? `${i.cpuCores} core${i.cpuCores === 1 ? '' : 's'}` : ''),
          meter7('mem', 'Memory', i.memPercent,
            i.memTotal ? `${bytes(i.memUsed || 0)} / ${bytes(i.memTotal)}` : ''),
        ]),

        /* The path between this panel and that server.
         *
         * This space used to repeat the login the panel connects with — the
         * user, an at sign and the address — all of which is already on the
         * card or in the form behind it. The panel runs on the Iran side and
         * every managed server sits at the far end of the route that matters,
         * so the one thing worth saying here is how that route behaves. */
        el('div', { class: 'mp-net', 'data-net': '' }, netText(n)),

        el('div', { class: 'mp-rule' }),
      ]),

      el('div', { class: 'mp-foot' }, [
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

    /* Editing happens inside the card.
     *
     * It used to insert a separate form after it: a second, differently shaped
     * card that broke the row and took the server's own context away from the
     * thing being edited. Worse, every press of Edit inserted another one, so a
     * server could end up with four open forms disagreeing about its address.
     *
     * It is built once, with the card, and shown by a class — the same way the
     * remove confirmation beside it works, which is what makes the two read as
     * one surface rather than two features. */
    const editor = editPanel(n);
    card.append(editor);

    const confirm = el('div', { class: 'cf7' }, [
      el('p', { html: `Stop managing <b>${esc(n.name)}</b>? ${builtThere(n)} — this panel just loses the way to change them.` }),
      el('button', { class: 'btn7 warn', text: 'Remove' }),
      el('button', { class: 'btn7', text: 'Cancel' }),
    ]);
    card.append(confirm);

    const btns = [...card.querySelectorAll('.mp-foot button')];
    const upB = behind ? btns.shift() : null;
    const [refreshB, editB, rmB] = btns;

    upB?.addEventListener('click', async () => {
      if (!await confirmBox({
        title: `Upgrade ${esc(n.name)} to ${esc(mine)}?`,
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


    editB?.addEventListener('click', () => {
      /* A toggle, so pressing Edit twice closes what it opened rather than
         opening a second one. */
      const open = card.classList.toggle('ed7');
      card.classList.remove('arm7');
      if (open) editor.querySelector('input')?.focus();
    });

    rmB.addEventListener('click', () => {
      card.classList.add('arm7');
      card.classList.remove('ed7');
    });
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

  /* One resource, as a labelled bar.
   *
   * The bar is the reading and the number beside it is the same reading said
   * exactly; the caption underneath is what the percentage is a percentage of,
   * which is the part a bare "78%" leaves out. Above 90 it takes the warning
   * colour — the point at which a server is about to become somebody's
   * evening.
   *
   * `key` is how paintLive finds it again: these are the two figures that move
   * on every poll, and they are written into the elements already on the card
   * rather than the card being made again around them. */
  const meter7 = (key, label, pct, caption) => {
    const v = pctOf(pct);
    return el('div', { class: 'mp-m' + hotness(v), 'data-m': key }, [
      el('div', { class: 'mp-mh' }, [
        el('span', { text: label }),
        el('b', { text: v.toFixed(0) + '%' }),
      ]),
      el('div', { class: 'mp-bar' }, el('i', { style: `width:${v.toFixed(1)}%` })),
      el('em', { text: caption || '' }),
    ]);
  };

  const pctOf = v => Math.max(0, Math.min(100, Number(v) || 0));
  const hotness = v => (v >= 90 ? ' hot' : v >= 75 ? ' warm' : '');

  /* What to say about the path to a server.
   *
   * A probe that got no reply at all is reported as unknown rather than as
   * total loss. ICMP is blocked outright on plenty of hosts and inside plenty
   * of containers, and ping cannot tell that from a server dropping every
   * packet — printing "100% loss" over a server that is working perfectly
   * would be the card stating something false with confidence. */
  function netText(n) {
    const net = n.net || {};
    if (!net.measured) return 'Packet loss —';
    const loss = Number(net.lossPct) || 0;
    const rtt = Number(net.rttMs) || 0;
    return `Packet loss ${loss.toFixed(1)}% · RTT ${rtt.toFixed(0)} ms`;
  }

  /* The figures that move, written into the card that is already there.
   *
   * This is the other half of keeping the grid still. The signature below
   * leaves these out on purpose, so a card whose processor ticked from 31% to
   * 33% is not thrown away and built again — it is the same card with two
   * numbers changed, which is what actually happened. The bar widths animate
   * from where they were because the element they belong to never went away.
   */
  function paintLive(card, n) {
    const i = n.info || {};
    card.classList.toggle('live', !!n.online);
    card.classList.toggle('pend', !!n.pending);

    const up = card.querySelector('[data-up]');
    if (up) up.textContent = (i.uptime && i.uptime !== '-') ? i.uptime : '—';

    const set = (key, pct, caption) => {
      const m = card.querySelector(`[data-m="${key}"]`);
      if (!m) return;
      const v = pctOf(pct);
      m.className = 'mp-m' + hotness(v);
      const b = m.querySelector('.mp-mh b');
      if (b) b.textContent = v.toFixed(0) + '%';
      const bar = m.querySelector('.mp-bar i');
      if (bar) bar.style.width = v.toFixed(1) + '%';
      const cap = m.querySelector('em');
      if (cap) cap.textContent = caption || '';
    };
    set('cpu', i.cpuPercent,
      i.cpuCores ? `${i.cpuCores} core${i.cpuCores === 1 ? '' : 's'}` : '');
    set('mem', i.memPercent,
      i.memTotal ? `${bytes(i.memUsed || 0)} / ${bytes(i.memTotal)}` : '');

    const net = card.querySelector('[data-net]');
    if (net) net.textContent = netText(n);
  }

  /* Changing how a server is reached, inside the card it is about.
   *
   * The password is never sent back to the browser, so this asks for it again
   * rather than showing a field that looks filled in and is not. Leaving it
   * empty keeps the one that is stored, which is what an operator changing only
   * the address means.
   *
   * Built with the card and shown by a class, so there is exactly one of these
   * per server however many times Edit is pressed — and it sits over that
   * server's own card, which is the context the change is being made in.
   */
  function editPanel(n) {
    const box = el('form', { class: 'ed-l', autocomplete: 'off' }, [
      el('div', { class: 'ed-h' }, [
        el('b', { text: 'How the panel reaches this server' }),
        el('span', { text: 'Leave the password blank to keep the one already stored.' }),
      ]),
      el('div', { class: 'ed-g', html:
        `<label>Address<input name="host" value="${esc(n.host)}" autocomplete="off"></label>
         <label>SSH port<input name="sshPort" type="number" min="1" max="65535" value="${n.sshPort || 22}"></label>
         <label>Username<input name="user" value="${esc(n.user)}" autocomplete="off"></label>
         <label class="wide">New password<input name="password" type="password"
           placeholder="unchanged" autocomplete="new-password"></label>` }),
      el('div', { class: 'ed-n', text:
        'Changing the address forgets the host key — a different machine is entitled to a different one.' }),
      el('div', { class: 'ed-f' }, [
        el('button', { type: 'button', class: 'btn7', text: 'Cancel' }),
        el('button', { type: 'submit', class: 'btn7 solid', text: 'Save' }),
      ]),
    ]);

    const shut = () => box.closest('.mp7')?.classList.remove('ed7');
    box.querySelector('.ed-f button').addEventListener('click', shut);
    box.addEventListener('submit', async ev => {
      ev.preventDefault();
      const save = box.querySelector('button[type=submit]');
      save.disabled = true; save.textContent = 'Saving…';
      const f = new FormData(box);
      try {
        paint(await api.nodeCredentials({
          name: n.name,
          host: String(f.get('host') || '').trim(),
          sshPort: String(f.get('sshPort') || '').trim(),
          user: String(f.get('user') || '').trim(),
          password: String(f.get('password') || ''),
        }));
        toast(`${n.name} updated.`);
        shut();
      } catch (e) {
        oops(e);
        save.disabled = false; save.textContent = 'Save';
      }
    });
    return box;
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
      /* A server joining the fleet often already holds the far end of tunnels
         this panel has been managing alone — every tunnel built before there
         was a fleet is in that position. The panel can demonstrate which ones,
         and offers them; it links nothing on its own, because a pairing decides
         where the operator's next edit is sent. */
      if (state.pairSuggestions && state.pairSuggestions.length) {
        await offerPairs(state.pairSuggestions);
      }
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

  /* What a card draws, minus the things that move.
   *
   * The readings are left out on purpose, and this is the whole fix for cards
   * that looked like they were reloading: the signature used to be built from
   * `d.info` whole and from `d.lastSeen`. Both change on every successful poll
   * — the processor by a percent, the timestamp by six seconds — so no
   * signature ever matched, every card was replaced four times a minute, and
   * the entrance animation ran again each time. The reconcile below was
   * written to prevent exactly that and was defeated by what it was comparing.
   *
   * Everything here is a fact that holds between polls. The readings are
   * written into the card that already exists; see paintLive. It is the same
   * split the tunnel cards make for their charts.
   */
  const sigOf = d => {
    const i = d.info || {};
    return JSON.stringify([
      d.online, d.why, d.host, d.user, d.sshPort, d.pending, d.tunnels || [],
      i.hostname, i.version, i.os, i.distro, i.ipv4, i.ipv6,
      i.country, i.city, i.isp, i.cpuCores, i.memTotal,
    ]);
  };

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
      } else {
        if (at ? held.el.previousElementSibling !== at : fleet.firstElementChild !== held.el) {
          // Order changed — move it rather than rebuild it.
          if (at) at.after(held.el); else fleet.prepend(held.el);
        }
        // Nothing structural changed, so only the readings are written.
        paintLive(held.el, n);
      }
      at = held.el;
    }
  }

  /* ---- the poll ----
   *
   * The first paint comes from what the panel already knows, because the live
   * listing opens a connection to every server and the page cannot draw until
   * the slowest of them has answered — four or five seconds on a fleet of
   * four, for a name, an address and a version that were all on disk. The
   * cached answer puts the cards on the screen in one round trip, marked as
   * not yet checked, and the live pass a moment later fills in reachability
   * and the readings without rebuilding them.
   */
  let timer = null;
  const tick = async () => {
    try { paint(await api.nodes()); } catch (e) { /* the page keeps what it has */ }
  };

  (async () => {
    try { paint(await api.nodesCached()); } catch (e) { /* the live pass follows */ }
    tick();
  })();

  timer = setInterval(tick, 6000);
  ctx.setTeardown(() => clearInterval(timer));
}

/* Offering the pairings a new server made possible.
 *
 * One question per tunnel rather than one for all of them: they are separate
 * facts, the operator may know one to be wrong, and "link 3 tunnels" is not
 * something anybody can check before pressing it. Declining is free — the link
 * stays available from the tunnel's own menu afterwards.
 */
async function offerPairs(suggestions) {
  for (const s of suggestions) {
    const ok = await confirmBox({
      title: `Is <q>${esc(s.name)}</q> the same tunnel as <q>${esc(s.peerName)}</q>?`,
      body: `${s.why}. Linking them lets this panel carry edits across, start and `
          + `stop both halves together, read that server's journal for it, and run the `
          + `speed test end to end. Nothing is changed on either machine.`,
      lines: [{ text: `${s.node}: ${s.peerName}` }],
      go: 'Link them', icon: 'check',
    });
    if (!ok) continue;
    try {
      await api.adoptTunnel(s.name, s.node, s.peerName);
      toast(`${s.name} is linked to ${s.peerName} on ${s.node}.`);
    } catch (e) { oops(e); }
  }
}

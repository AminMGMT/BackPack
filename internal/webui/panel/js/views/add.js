/* Add a tunnel: pick the side, then the transport family, then the transport,
 * then the settings that side actually has.
 *
 * The families and the presets are served rather than written into the page, so
 * a transport added to the CLI menu appears here on its own and the two can
 * never describe different things.
 *
 * CLI: 1 Setup Iran, 2 Setup Kharej.
 */

import { $$, esc } from '../lib/dom.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { oops, toast } from '../ui/toast.js';
import { go } from '../router.js';

export function addView(ctx) {
  openScreen('add', {
    pick: '.dlg',
    bind: async (root, close) => {
      let opts = { families: [], presets: [] };
      try { opts = await api.tunnelOptions(); } catch (e) { oops(e); }

      /* families */
      /* The family list, the variants and the presets are all painted from
         /api/tunnel/options below, once the shape is known. */

      /* Steps and the pick-one groups are the preview's own handlers, rebound
         in screen.js. What is chosen is remembered here, and the form follows:
         reverse and direct are two different shapes, and each side of a tunnel
         is asked for different things — the Iran side owns the forwarded ports,
         the kharej side owns the address that is dialled. */
      const chosen = { side: 'server', direction: 'reverse', transport: null,
                       family: 0, carrier: 'pck', preset: null };

      const show = (sel, on) => root.querySelectorAll(sel)
        .forEach(n => { n.hidden = !on; });

      /* What each transport actually has, taken from the predicates in
         internal/manage/config.go rather than guessed:
           isMux   tcpmux, wsmux, wssmux, kcp, xdi, spoof, pck  -> the mux_* knobs
           isKCP   kcp, xdi, spoof, pck                          -> the kcp_* knobs and FEC
           needsTLS wss, wssmux                                  -> a certificate
         and the two raw carriers own their own drawer. A field that does not
         belong to the chosen transport is not shown, because writing it would
         put a key in the config that transport never reads. */
      const isMux = t => ['tcpmux', 'wsmux', 'wssmux', 'kcp', 'xdi', 'spoof', 'pck'].includes(t);
      const isKCP = t => ['kcp', 'xdi', 'spoof', 'pck'].includes(t);
      const needsTLS = t => ['wss', 'wssmux'].includes(t);

      /* dw-ft fine tune · dw-sp spoof · dw-pk packet carrier · dw-cn connection */
      const drawer = id => root.querySelector('#dw-' + id);

      function applyFields() {
        const t = chosen.transport || '';
        const on = (sel, yes) => root.querySelectorAll(sel).forEach(n => {
          const row = n.closest('.f3, .tg3, .fhead') || n;
          row.hidden = !yes;
        });
        on('[name^="tune.mux"]', isMux(t));
        on('[name^="tune.kcp"]', isKCP(t));
        on('[data-when="tls"]', needsTLS(t));

        /* The spoof and packet-carrier drawers only mean anything on their own
           transport — on plain TCP they were offering settings nothing reads. */
        const sp = drawer('sp');
        if (sp) { sp.hidden = t !== 'spoof'; if (t !== 'spoof') sp.classList.remove('open'); }
        const pk = drawer('pk');
        if (pk) { pk.hidden = t !== 'pck'; if (t !== 'pck') pk.classList.remove('open'); }
      }

      /* Throughput is offered only where it applies — presetSuitsTransport. */
      function applyPresets() {
        const t = chosen.transport || '';
        root.querySelectorAll('.rp').forEach(b => {
          const key = b.querySelector('.key2')?.textContent.trim();
          const hide = key === 'throughput' && t !== 'kcp';
          b.hidden = hide;
          if (hide && b.classList.contains('on')) {
            b.classList.remove('on');
            root.querySelector('.rp:not([hidden])')?.classList.add('on');
          }
        });
      }

      /* The variants come from /api/tunnel/options, so a transport added to the
         CLI menu appears here on its own — and picking a family actually
         changes the list, which it did not before. */
      function paintTransports() {
        const list = root.querySelector('#trlist');
        const fam = opts.families?.[chosen.family ?? 0];
        if (!list || !fam) return;
        list.innerHTML = fam.entries.map((e, i) => {
          const root2 = /needs root/i.test(e.desc || '') ? '<span class="root2">needs root</span>' : '';
          return `<button class="tr${i === 0 ? ' on' : ''}" data-fn="setTr" data-args="'${e.value}'">
            <span class="tn2"><b>${esc(e.label)}</b>
            <span class="key2">${esc(e.value)}</span>${root2}</span></button>`;
        }).join('');
        chosen.transport = fam.entries[0]?.value || null;
        applyFields();
        applyPresets();
      }

      function applyShape() {
        const direct = chosen.direction === 'direct';
        show('.step3rev', !direct);
        show('.step3direct', direct);

        /* "server" is the Iran side, "client" the kharej side — the words the
           create endpoints use. */
        show('[data-when="server"]', chosen.side === 'server');
        show('[data-when="client"]', chosen.side === 'client');

        /* The forged source cannot be learned from the traffic, so the kharej
           side of a spoof carrier has to be told where its peer is. */
        const spoofField = root.querySelector('[name="spoofPeerIp"]')?.closest('.f3, div');
        if (spoofField) spoofField.hidden = !(direct && chosen.carrier === 'spoof'
                                              && chosen.side === 'client');
        if (!direct) { applyFields(); applyPresets(); }
      }

      /* The last step is not a summary.
         A reverse tunnel is set up on Iran first and finished on kharej; a
         direct one waits on kharej and is finished from Iran. On the side that
         finishes it there is nothing left to copy anywhere — what you want to
         know is whether it came up. So that side watches it come up, and the
         side that still has work to do gets the handoff instead. */
      /* The last step is not a summary.
         A reverse tunnel is set up on Iran first and finished on kharej; a
         direct one waits on kharej and is finished from Iran. On the side that
         finishes it there is nothing left to carry anywhere — the only question
         is whether it came up, so that side watches it come up. The side that
         still has work to do gets the handoff instead. */
      function finish() {
        const step = root.querySelector('.step[data-s="2"]');
        if (!step) return;
        const direct = chosen.direction === 'direct';
        const finishing = direct ? chosen.side === 'server' : chosen.side === 'client';

        const hand = step.querySelector('.handpane');
        const conn = step.querySelector('.connpane');
        if (hand) hand.hidden = finishing;
        if (conn) conn.hidden = !finishing;
        if (!finishing) { paintHandoff(hand); return; }
        if (!conn) return;

        const name = root.querySelector('[name="name"]')?.value?.trim();
        const addr = root.querySelector('[name="peerAddr"], [name="serverAddr"]')?.value?.trim();
        const third = conn.querySelectorAll('.sg .tx6')[2];
        if (third && addr) third.textContent = 'Reaching ' + addr;

        const rows = [...conn.querySelectorAll('.sg')];
        const title = conn.querySelector('#connTitle');
        const sub = conn.querySelector('#connSub');
        const result = conn.querySelector('#connResult');
        const t0 = Date.now();
        let i = 0;

        /* Three of the four are things this side just did, so they land as they
           happen; the last one is the only thing the server can confirm. */
        const tick = async () => {
          if (i >= rows.length) return;
          rows[i].classList.add('doing');
          const stamp = () => {
            const d = rows[i].querySelector('.dur');
            if (d) d.textContent = ((Date.now() - t0) / 1000).toFixed(1) + 's';
          };
          if (i < rows.length - 1) {
            rows[i].classList.add('done'); stamp(); i++;
            setTimeout(tick, 420);
            return;
          }
          await store.loadTunnels();
          const made = name ? store.tunnel(name) : null;
          const up = made && made.state === 'running';
          rows[i].classList.add(up ? 'done' : 'failed');
          stamp();
          conn.querySelector('.spin')?.setAttribute('hidden', '');
          if (title) title.textContent = up ? 'Connected' : 'Not up yet';
          if (sub) sub.textContent = up
            ? 'Both ends are talking. Nothing else to do on this server.'
            : 'The config is written and the service is running, but the control channel has not come up yet.';
          if (result) {
            result.innerHTML = up
              ? `<div class="doneline"><span class="tick">✓</span><div>
                   <b id="doneName">${esc(made.name)} is running on this server</b>
                   <span>${esc(made.peerLocation || '')} · ${esc(made.addr || '')}</span>
                 </div></div>`
              : `<div class="doneline warn"><span class="tick">!</span><div>
                   <b>Give it a moment</b>
                   <span>Check the other side is set up with the same port and token.
                         The tunnel card will turn green on its own when it connects.</span>
                 </div></div>`;
          }
          store.refresh();
        };
        tick();
      }

      /* The numbered header moved with the step in the preview; it is wired to
         the same event the Back and Continue buttons raise. */
      root.addEventListener('step', ev => {
        const { at } = ev.detail;
        root.querySelectorAll('.steps .st2').forEach(x =>
          x.classList.toggle('on', Number(x.dataset.s) === at));
        if (at === 2) finish();
      });

      root.addEventListener('pick', ev => {
        const { fn, value, el } = ev.detail;
        if (fn === 'setSide') chosen.side = value;
        if (fn === 'mode3') chosen.direction = value === 'dir' ? 'direct' : 'reverse';
        if (fn === 'setFam') { chosen.family = Number(value); paintTransports(); }
        if (fn === 'setTr') { chosen.transport = value; applyFields(); applyPresets(); }
        if (fn === 'setCar') chosen.carrier = value;
        if (fn === 'setPre') chosen.preset = (el.querySelector('.key2, .k3')?.textContent
          || el.textContent).trim().toLowerCase().split(/\s+/)[0];
        applyShape();
      });

      /* Setup Iran and Setup Kharej are two entries in the CLI menu, so they are
         two links here; ?step= opens the wizard part-way, which is what a
         "now do the other side" link needs. */
      const q = ctx.query;
      if (q.get('side')) chosen.side = q.get('side') === 'kharej' ? 'client' : 'server';
      if (q.get('kind')) chosen.direction = q.get('kind') === 'direct' ? 'direct' : 'reverse';
      const markGroup = (fn, value) => {
        const b = [...root.querySelectorAll(`[data-fn="${fn}"]`)]
          .find(x => (x.dataset.args || '').includes(value));
        if (b) [...b.parentElement.children].forEach(x => x.classList.toggle('on', x === b));
      };
      markGroup('setSide', chosen.side);
      markGroup('mode3', chosen.direction === 'direct' ? 'dir' : 'rev');

      paintTransports();
      applyShape();

      const stepTo = Number(q.get('step') || 0);
      if (stepTo) {
        const steps = [...root.querySelectorAll('.step[data-s]')];
        const at = Math.min(stepTo, steps.length - 1);
        steps.forEach((x, i) => { x.hidden = i !== at; });
        /* the same event Continue raises, so the header and the last step
           behave identically however the step was reached */
        setTimeout(() => root.dispatchEvent(
          new CustomEvent('step', { detail: { at, of: steps.length } })), 0);
      }

      /* Random asks the server for a free port; the CLI line is built from what
         has been chosen so the other side can be set up over SSH. */
      [...root.querySelectorAll('button')]
        .filter(b => /^random$/i.test(b.textContent.trim()))
        .forEach(b => b.addEventListener('click', async () => {
          try {
            const r = await api.tunnelSuggest();
            const field = b.closest('div')?.querySelector('input');
            if (field && r.port) field.value = r.port;
          } catch (e) { oops(e); }
        }));

      [...root.querySelectorAll('button')]
        .filter(b => /show as a cli command/i.test(b.textContent.trim()))
        .forEach(b => b.addEventListener('click', async () => {
          const get = n => root.querySelector(`[name="${n}"], #${n}`)?.value?.trim() || '';
          const line = ['sudo backpack',
            chosen.direction === 'direct' ? 'direct' : 'reverse',
            chosen.side === 'server' ? '--iran' : '--kharej',
            chosen.transport ? '--transport ' + chosen.transport : '',
            get('aname') ? '--name ' + get('aname') : '',
          ].filter(Boolean).join(' ');
          try { await navigator.clipboard.writeText(line); toast('Command copied.'); }
          catch (e) { toast(line); }
        }));

      /* The setup link: everything the two ends must agree on, in one string.
         It is asked for by name, because the server builds it from the tunnel
         that was just written rather than from what is on screen — the file is
         the truth, and a link built from the form would describe a tunnel that
         might not be the one running. */
      async function paintHandoff(hand) {
        if (!hand) return;
        const pane = hand.querySelector('#linkPane');
        const val = hand.querySelector('#linkVal');
        const name = root.querySelector('[name="name"]')?.value?.trim();
        if (!pane || !val || !name) return;
        try {
          const r = await api.shareLink(name);
          val.textContent = r.link;
          pane.hidden = false;
        } catch (e) {
          /* No link is not a failure of the tunnel: the four values below still
             set the other side up by hand. So the pane simply stays away. */
          pane.hidden = true;
        }
      }

      root.querySelector('#linkCopy')?.addEventListener('click', async () => {
        const link = root.querySelector('#linkVal')?.textContent?.trim();
        if (!link) return;
        try { await navigator.clipboard.writeText(link); toast('Setup link copied.'); }
        catch (e) { toast(link); }
      });

      /* Pasting a link from the other server. The mirroring is the server's, so
         this only paints what comes back — and remembers which fields came from
         the link, because changing one of those is what silently breaks a pair. */
      const fromLink = new Map();

      function fillFromPeerForm(f) {
        fromLink.clear();
        const put = (sel, v) => {
          if (v === undefined || v === null || v === '') return;
          const el = root.querySelector(sel);
          if (!el) return;
          if (el.type === 'checkbox') el.checked = !!v; else el.value = v;
        };
        /* Named fields first: the form is read by name on submit, so filling by
           name is filling exactly what will be sent. */
        for (const [k, v] of Object.entries(f)) {
          if (k === 'paired' || k === 'spoof' || typeof v === 'object') continue;
          put(`[name="${k}"]`, v);
        }
        if (f.spoof) {
          for (const [k, v] of Object.entries(f.spoof)) put(`[name="spoof.${k}"]`, v);
        }
        /* Which side and which kind the link is for are choices, not fields. */
        if (f.side) { chosen.side = f.side === 'iran' ? 'server' : 'client'; markGroup('setSide', chosen.side); }
        if (f.kind) {
          chosen.direction = f.kind;
          markGroup('mode3', f.kind === 'direct' ? 'dir' : 'rev');
        }
        if (f.transport) { chosen.transport = f.transport; }
        if (f.carrier) { chosen.carrier = f.carrier; }
        applyShape();

        /* Remember the value each paired field arrived with, so an edit can be
           recognised as a divergence rather than merely as typing. */
        (f.paired || []).forEach(name => {
          const el = root.querySelector(`[name="${name}"]`);
          if (el) fromLink.set(name, el.type === 'checkbox' ? el.checked : el.value);
        });
      }

      /* The warning the operator asked for: it appears at the moment a paired
         field is edited, names what was changed, and offers the way back. It is
         a warning and not a lock — the field stays editable, because there are
         real reasons to change one (both ends at once), and a form that refuses
         is a form people work around. */
      const pasteMsg = root.querySelector('#pasteMsg');
      function warnIfDiverged() {
        if (!pasteMsg || !fromLink.size) return;
        const changed = [];
        for (const [name, was] of fromLink) {
          const el = root.querySelector(`[name="${name}"]`);
          if (!el) continue;
          const now = el.type === 'checkbox' ? el.checked : el.value;
          if (now !== was) changed.push(name.replace(/^spoof\./, ''));
        }
        if (!changed.length) { pasteMsg.hidden = true; return; }
        pasteMsg.querySelector('span:last-child').textContent =
          'You have changed ' + changed.join(', ') + ', which came from the other server. '
          + 'Both ends must agree on these — change the other side to match, or the tunnel '
          + 'will connect and carry nothing.';
        pasteMsg.hidden = false;
      }
      root.addEventListener('input', warnIfDiverged);
      root.addEventListener('change', warnIfDiverged);

      async function applyPastedLink() {
        const box = root.querySelector('#apaste');
        if (!box || !pasteMsg) return;
        const link = box.value.trim();
        if (!link) return;
        try {
          const f = await api.shareLinkDecode(link);
          fillFromPeerForm(f);
          pasteMsg.hidden = true;
          toast(f.note ? 'Filled in. ' + f.note : 'Filled in from the other server.');
        } catch (e) {
          /* The server's wording, not ours: it knows whether the link was cut
             short, or made by a version this one does not understand. */
          pasteMsg.querySelector('span:last-child').textContent = String(e.message || e);
          pasteMsg.hidden = false;
        }
      }
      root.querySelector('#apasteb')?.addEventListener('click', applyPastedLink);
      root.querySelector('#apaste')?.addEventListener('paste', () => setTimeout(applyPastedLink, 0));

      const create = root.querySelector('[data-create], .create');
      create?.addEventListener('click', async () => {
        /* Only what is on screen: a hidden field belongs to the other side or
           the other kind of tunnel, and sending it would describe a tunnel
           nobody asked for. */
        const payload = {};
        root.querySelectorAll('input[name], select[name]').forEach(n => {
          if (n.closest('[hidden]')) return;
          const v = n.type === 'checkbox' ? n.checked : n.value.trim();
          if (v === '' || v === false) return;
          const keys = n.name.split('.'), last = keys.pop();
          let at = payload;
          for (const k of keys) at = at[k] ??= {};
          at[last] = /^-?\d+$/.test(v) ? Number(v) : v;
        });
        payload.name ||= '';
        if (chosen.direction === 'direct') {
          payload.side = chosen.side === 'server' ? 'iran' : 'kharej';
          payload.carrier = chosen.carrier;
        } else {
          payload.role = chosen.side;
          payload.transport = chosen.transport;
        }
        if (chosen.preset) payload.preset = chosen.preset;

        create.disabled = true;
        try {
          const r = chosen.direction === 'direct'
            ? await api.directCreate(payload)
            : await api.tunnelCreate(payload);
          const done = root.querySelector('#doneName');
          if (done) done.textContent =
            `${payload.name || 'The tunnel'} is running on this server`;
          toast(r.active ? 'Tunnel created and running.' : 'Tunnel created.');
          store.refresh();
          close(); go('/');
        } catch (e) { oops(e); }
        create.disabled = false;
      });

      ctx.setTeardown(close);
    },
  }).catch(oops);
}

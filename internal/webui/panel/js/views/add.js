/* Add a tunnel: pick the side, then the transport family, then the transport,
 * then the settings that side actually has.
 *
 * The families and the presets are served rather than written into the page, so
 * a transport added to the CLI menu appears here on its own and the two can
 * never describe different things.
 *
 * CLI: 1 Setup Iran, 2 Setup Kharej.
 */

import { $$, el, esc, dialogSubtitle } from '../lib/dom.js';
import { isUp } from '../lib/tstate.js';
import { NUMERIC } from '../lib/numeric.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { oops, toast } from '../ui/toast.js';
import { go } from '../router.js';

export function addView(ctx) {
  openScreen('add', {
    pick: '.dlg',
    bind: async (root, close) => {
      dialogSubtitle(root, store.get().stats, 'you will do this on both servers');
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
      /* Both taken from internal/manage/config.go, because the server refuses
         these combinations by name and a form that offers them is a form that
         collects an answer only to have it rejected. */
      const isWS = t => ['ws', 'wss', 'wsmux', 'wssmux'].includes(t);
      const isDatagram = t => ['udp', 'kcp', 'xdi', 'quic', 'pck'].includes(t);

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

        /* The packet-carrier drawer only means anything on its own transport —
           on plain TCP it was offering settings nothing reads. The spoof drawer
           is not here at all any more: spoof is a direct carrier, so applyShape
           decides it. */
        const pk = drawer('pk');
        if (pk) { pk.hidden = t !== 'pck'; if (t !== 'pck') pk.classList.remove('open'); }

        /* The other server's settings follow the transport too.
         *
         * They were dead markup before — conn.edgeIP carried data-when="client-ws",
         * a value nothing matches, so it never appeared at all. Moving them into
         * a section of their own made them appear on every transport instead,
         * which is worse: a CDN edge offered on a plain TCP tunnel is a question
         * with no right answer, and the server rejects it by name if it is
         * filled in.
         *
         * ConnTune.apply is the authority for both of these. */
        on('[name="peerConn.edgeIP"]', isWS(t));
        on('[name="peerConn.proxy"]', !isDatagram(t));
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
      /* The families come from the server too.
       *
       * They were four hard-coded buttons, so removing the Experimental family
       * from the source left the button behind — pointing at a family that no
       * longer exists. Drawn from the same answer as the transports, the two
       * cannot drift apart again. */
      function paintFamilies() {
        const row = root.querySelector('.fam');
        if (!row || !opts.families?.length) return;
        row.innerHTML = opts.families.map((f, i) =>
          `<button class="${i === (chosen.family ?? 0) ? 'on' : ''}" data-fn="setFam" data-args="'${i}'">
            <b>${esc(f.label)}</b></button>`).join('');
      }

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

      /* The drawers' controls were drawings.
       *
       * Every switch in the advanced drawers is a <div class="sw3"> and every
       * dropdown a <div class="sel4"> — the preview drew them that way, and
       * nothing ever made them controls. They have no name, so nothing they
       * showed was ever submitted, and the dropdowns opened no menu at all
       * because there was none to open. Fourteen switches and nine menus, all
       * of them ornamental.
       *
       * The ids already say what each one is: ft-nodelay, pk-flags, sp-profile,
       * cn-simpleAuth — a drawer prefix and the field's own name. So the wiring
       * is derived rather than listed: a hidden input named after the field
       * carries the value, and the drawing in front of it drives that input.
       * Nothing here invents a setting; every name below exists in FineTune,
       * SpoofTune, PckTune or ConnTune.
       */
      const DRAWER_GROUP = { ft: 'tune', sp: 'spoof', pk: 'pck', cn: 'conn' };

      const nameOfControl = (id, explicit) => {
        // A control outside the drawers says what it sets outright: the naming
        // convention only holds where there is a drawer to take the prefix from.
        if (explicit) return explicit;
        const at = id.indexOf('-');
        if (at < 0) return '';
        const group = DRAWER_GROUP[id.slice(0, at)];
        const field = id.slice(at + 1);
        return group && field ? `${group}.${field}` : '';
      };

      /* What each menu can be set to. The lists the server sends are used where
         it sends one, so the panel never offers an interface the machine does
         not have or a profile the engine does not know. */
      function choicesFor(id) {
        const auto = { value: '', label: 'Automatic — let the kernel choose the route' };
        const ifaces = () => [auto, ...(opts.interfaces || []).map(v => ({ value: v, label: v }))];
        switch (id) {
          case 'ft-logLevel':
            return ['debug', 'info', 'warn', 'error'].map(v => ({ value: v, label: v }));
          case 'sp-profile':
            return (opts.spoofProfiles || []).map(v => ({ value: v, label: v }));
          case 'sp-uplink':
          case 'sp-downlink':
            return [{ value: '', label: 'Same as the packet profile' },
              ...(opts.spoofProfiles || []).map(v => ({ value: v, label: v }))];
          case 'pk-flags':
            return [{ value: '', label: 'Default — push+ack, what a connection carrying data sends' },
              ...(opts.pckFlags || []).map(v => ({ value: v, label: v }))];
          default:
            return id.endsWith('interface') || id.endsWith('Iface') ? ifaces() : [];
        }
      }

      function wireDrawerControls() {
        /* Plain inputs in the drawers had ids and no names either — sp-mtu,
           sp-portMin, sp-sockBuf and the rest. Same convention, same fix: the
           id says which field it is, so it gets that name. */
        root.querySelectorAll('input[id]:not([name])').forEach(i => {
          const name = nameOfControl(i.id, i.dataset.name);
          if (name) i.name = name;
        });

        root.querySelectorAll('.sw3[id], .sw3[data-name]').forEach(sw => {
          const name = nameOfControl(sw.id || '', sw.dataset.name);
          if (!name || sw.dataset.wired) return;
          sw.dataset.wired = '1';
          const input = el('input', { type: 'checkbox', name, hidden: true });
          input.checked = sw.classList.contains('on');
          sw.after(input);
          sw.setAttribute('role', 'switch');
          sw.setAttribute('aria-checked', String(input.checked));
          sw.tabIndex = 0;
          const flip = () => {
            sw.classList.toggle('on');
            input.checked = sw.classList.contains('on');
            sw.setAttribute('aria-checked', String(input.checked));
          };
          sw.addEventListener('click', flip);
          sw.addEventListener('keydown', ev => {
            if (ev.key === ' ' || ev.key === 'Enter') { ev.preventDefault(); flip(); }
          });
        });

        root.querySelectorAll('.sel4[id], .sel4[data-name]').forEach(sel => {
          const name = nameOfControl(sel.id || '', sel.dataset.name);
          const choices = choicesFor(sel.id);
          if (!name || !choices.length || sel.dataset.wired) return;
          sel.dataset.wired = '1';

          const input = el('input', { type: 'text', name, hidden: true });
          // The label it was drawn with is the default, so an untouched menu
          // means what it appears to mean.
          const shown = sel.childNodes[0]?.textContent?.trim() || '';
          const start = choices.find(c => c.label === shown) || choices[0];
          input.value = start.value;
          sel.after(input);
          sel.setAttribute('role', 'combobox');
          sel.tabIndex = 0;

          const menu = el('div', { class: 'sel4menu', hidden: true });
          choices.forEach(c => {
            const opt = el('button', { type: 'button', class: 'sel4opt', text: c.label });
            opt.addEventListener('click', ev => {
              ev.stopPropagation();
              input.value = c.value;
              sel.childNodes[0].textContent = c.label;
              close4();
            });
            menu.append(opt);
          });
          sel.append(menu);

          const close4 = () => { menu.hidden = true; sel.classList.remove('open4'); };
          const open4 = () => {
            // One at a time: two menus open at once is two answers to one
            // question, and the second click lands on whichever is on top.
            root.querySelectorAll('.sel4menu').forEach(m => { m.hidden = true; });
            root.querySelectorAll('.sel4.open4').forEach(x => x.classList.remove('open4'));
            menu.hidden = false;
            sel.classList.add('open4');
          };
          sel.addEventListener('click', ev => {
            if (ev.target.closest('.sel4opt')) return;
            menu.hidden ? open4() : close4();
          });
          sel.addEventListener('keydown', ev => {
            if (ev.key === ' ' || ev.key === 'Enter') { ev.preventDefault(); menu.hidden ? open4() : close4(); }
            if (ev.key === 'Escape') close4();
          });
        });

        // A click anywhere else closes whatever is open.
        root.addEventListener('click', ev => {
          if (ev.target.closest('.sel4')) return;
          root.querySelectorAll('.sel4menu').forEach(m => { m.hidden = true; });
          root.querySelectorAll('.sel4.open4').forEach(x => x.classList.remove('open4'));
        });
      }

      function applyShape() {
        const direct = chosen.direction === 'direct';
        show('.step3rev', !direct);
        show('.step3direct', direct);

        /* "server" is the Iran side, "client" the kharej side — the words the
           create endpoints use. */
        show('[data-when="server"]', chosen.side === 'server');
        show('[data-when="client"]', chosen.side === 'client');
        /* Groups that were moved out of .step3rev / .step3direct carry the mode
           they belong to, because the container that used to decide it is no
           longer their parent. */
        root.querySelectorAll('[data-mode]').forEach(g => {
          const wrong = g.dataset.mode !== (direct ? 'dir' : 'rev');
          g.hidden = wrong;
          // A drawer left open in the other mode would spring back open with
          // its own settings when the operator switched away and back.
          if (wrong) g.classList.remove('open');
        });
        // The token and the far end's address are the panel's business now.
        root.querySelectorAll('.tokgone, .addrgone').forEach(n => { n.hidden = true; });
        paintRail();

        /* The spoof settings belong to the spoof carrier. They used to hang off
           a reverse transport that no longer exists, which left every one of
           them unreachable — the drawer was in a section where its condition
           could never be true. */
        const sp = drawer('sp');
        if (sp) {
          const wanted = direct && chosen.carrier === 'spoof';
          sp.hidden = !wanted;
          if (!wanted) sp.classList.remove('open');
        }

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
        const steps = [...root.querySelectorAll('.step[data-s]')];
        const step = steps[steps.length - 1];
        if (!step) return;

        const hand = step.querySelector('.handpane');
        const conn = step.querySelector('.connpane');

        /* With a managed server this step is not a report on something that
           already happened — it is where it happens. */
        if (root.dataset.staged) {
          if (hand) hand.hidden = true;
          if (conn) conn.hidden = false;
          runBuild(conn);
          return;
        }

        const direct = chosen.direction === 'direct';
        const finishing = direct ? chosen.side === 'server' : chosen.side === 'client';
        if (hand) hand.hidden = finishing;
        if (conn) conn.hidden = !finishing;
        if (!finishing) { paintHandoff(hand); return; }
        if (!conn) return;
        watchLocal(conn);
      }

      /* Building both ends, said out loud.
       *
       * Four stages, and each one is a fact from the answer rather than a
       * timer: the config written here, the service up here, the config written
       * there, and both ends agreeing. The panel makes one call — the work is
       * one transaction on the server — so these are not live telemetry and are
       * not dressed up as it. They resolve in order as the answer is read, and a
       * stage that did not happen is marked failed and says which end it was.
       *
       * That last part is the whole reason for showing stages at all. "Could
       * not create the tunnel" is a message with nowhere to go. "Written here,
       * started here, not written on kharej-de" is one that tells you which
       * machine to look at.
       */
      let building = false;
      async function runBuild(conn) {
        if (!conn || building) return;
        building = true;
        const rows = [...conn.querySelectorAll('.sg')];
        const title = conn.querySelector('#connTitle');
        const sub = conn.querySelector('#connSub');
        const result = conn.querySelector('#connResult');
        const spin = conn.querySelector('.spin');
        const onNode = nodeSel?.value || '';
        const direct = chosen.direction === 'direct';
        const t0 = Date.now();

        const labels = [
          'Writing the configuration on this server',
          'Starting it here',
          `Writing the configuration on ${onNode || 'the other server'}`,
          'Both ends reporting up',
        ];
        rows.forEach((r, i) => {
          const tx = r.querySelector('.tx6');
          if (tx && labels[i]) tx.textContent = labels[i];
          r.classList.remove('done', 'doing', 'failed');
          const d = r.querySelector('.dur');
          if (d) d.textContent = '';
        });
        if (title) title.textContent = 'Building…';
        if (sub) sub.textContent = 'Both ends are written from what you filled in.';

        const settle = (i, ok) => {
          if (!rows[i]) return;
          rows[i].classList.remove('doing');
          rows[i].classList.add(ok ? 'done' : 'failed');
          const d = rows[i].querySelector('.dur');
          if (d) d.textContent = ((Date.now() - t0) / 1000).toFixed(1) + 's';
        };
        const start = i => rows[i]?.classList.add('doing');
        const pause = ms => new Promise(r => setTimeout(r, ms));

        start(0);
        let out;
        try {
          out = await submitTunnel();
        } catch (e) {
          settle(0, false);
          spin?.setAttribute('hidden', '');
          if (title) title.textContent = 'Nothing was created';
          if (sub) sub.textContent = 'This server refused the settings, so neither end was written.';
          if (result) {
            result.innerHTML = `<div class="doneline warn"><span class="tick">!</span><div>
              <b>${esc(e.message || 'The panel could not build the tunnel')}</b>
              <span>Go back and change what it names, then press Create again.</span>
            </div></div>`;
          }
          building = false;
          return;
        }

        const { r, name } = out;
        const partial = r.status === 'partial';

        settle(0, !!r.service || !onNode);
        await pause(260);

        start(1); await pause(200);
        settle(1, r.active !== false);

        if (onNode) {
          await pause(260);
          start(2); await pause(240);
          settle(2, !partial);

          await pause(200);
          start(3); await pause(200);
          settle(3, !partial && r.active !== false && r.peer?.active !== false);
        } else {
          rows[2]?.remove();
          rows[3]?.remove();
        }

        spin?.setAttribute('hidden', '');
        store.refresh();
        paired = partial ? false : (onNode || false);

        const good = !partial && r.active !== false;
        if (title) title.textContent = good ? 'Both ends are up' : partial ? 'Only this end was built' : 'Created, not up yet';
        if (sub) {
          sub.textContent = good
            ? 'Nothing else to do on either server.'
            : partial
              ? `This server has it. ${onNode} does not.`
              : 'The config is written and the service is running, but the tunnel has not come up.';
        }
        if (result) {
          result.innerHTML = good
            ? `<div class="doneline"><span class="tick">✓</span><div>
                 <b id="doneName">${esc(name || 'The tunnel')} is running on both servers</b>
                 <span>Written here and on ${esc(onNode)}.</span>
               </div></div>`
            : `<div class="doneline warn"><span class="tick">!</span><div>
                 <b>${esc(partial ? (r.peerError || 'The other end was not written')
                                  : 'Give it a moment')}</b>
                 <span>${esc(partial
                   ? (r.peerHint || `Open the tunnel's setup link and paste it on ${onNode}.`)
                   : 'The tunnel card turns green on its own when it connects.')}</span>
               </div></div>`;
        }
        building = false;
      }

      /* The single-ended flow, unchanged: nothing is created here, the tunnel
         already exists, and this only waits to see it come up. */
      function watchLocal(conn) {
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
          const up = isUp(made);
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
        const { at, of } = ev.detail;
        if (skipEmpty(at)) return;
        lastStep = at;
        root.querySelectorAll('.steps .st2').forEach(x =>
          x.classList.toggle('on', Number(x.dataset.s) === at));
        const back = root.querySelector('#backb');
        if (back) back.disabled = at === 0;
        paintNav(at);
        if (at === of - 1) finish();
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
        if (fn === 'setPre' || fn === 'setTr' || fn === 'setSide') showPresetDefaults();
      });

      /* The Fine Tune drawer calls itself "preset defaults" and showed empty
         boxes: the endpoint that says what a preset produces was never asked.
         The numbers go in as placeholders rather than values, because a value
         would be posted, and a drawer that posts everything it displays is a
         drawer that overrides the preset it is describing. */
      async function showPresetDefaults() {
        let d = {};
        try {
          d = await api.tunnelDefaults({
            preset: chosen.preset || '',
            role: chosen.side || 'server',
            transport: chosen.transport || '',
          }) || {};
        } catch (e) { return; }
        root.querySelectorAll('input[name^="tune."]').forEach(inp => {
          const key = inp.name.slice('tune.'.length);
          const v = d[key];
          inp.placeholder = (v === undefined || v === null || v === '') ? '' : String(v);
        });
      }
      showPresetDefaults();

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

      paintFamilies();
      paintTransports();
      wireDrawerControls();
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
      /* Set to the node's name once both ends have been built here, so the
         handoff pane stops asking for something that has already happened. */
      let paired = false;

      async function paintHandoff(hand) {
        if (!hand) return;
        const pane = hand.querySelector('#linkPane');
        if (paired) { if (pane) pane.hidden = true; return; }
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
      /* The managed servers this tunnel could be built on.
       *
       * Only the connected ones are offered. A server that is enrolled but
       * offline cannot be written to, and listing it would turn a choice the
       * operator makes now into a failure they discover at the end of the form.
       * If none are connected the whole group stays away — an empty picker is
       * a question with no answers. */
      const nodeSel = root.querySelector('#anode');
      const nodeGrp = root.querySelector('#nodeGrp');
      const pasteGrp = root.querySelector('#apaste')?.closest('.grp3');
      /* The form in three parts, which is what it has always been without
       * saying so.
       *
       * A tunnel's settings fall into three kinds and the markup already knows
       * which is which: data-when="server" belongs to this machine,
       * data-when="client" to the other one, and a field with neither is one
       * both ends have to agree on. Until now that only decided what to hide,
       * because you filled in one side and went and filled in the other. When
       * the panel writes both, it is the structure of the form.
       */
      function markSides(root) {
        root.querySelectorAll('.step3rev .grp3, .step3direct .grp3').forEach(g => {
          const label = g.querySelector('.gl3');
          if (!label || label.querySelector('.sidechip')) return;
          const fields = [...g.querySelectorAll('[name]')].filter(f => !f.closest('#nodeGrp'));
          const local = fields.filter(f => f.closest('[data-when]'));

          /* Marked only when the whole group is one kind. A group that mixes
           * them gets nothing: the fields inside already say which is which,
           * and a heading that claims "both servers" over a name that is this
           * machine's alone is worse than a heading that claims nothing.
           *
           * A group with no fields at all — the transport and the preset are
           * chosen with buttons — is shared: those are carried to the other end
           * with everything else, and the form's own convention is that what is
           * not marked as one machine's belongs to both. */
          const chip = !fields.length ? 'both servers'
            : local.length === fields.length ? 'this server'
            : local.length === 0 ? 'both servers'
            : '';
          if (!chip) return;
          label.append(el('span', { class: 'sidechip', text: chip }));
        });
      }

      /* The other server's own settings.
       *
       * These four cannot be worked out from this end — which proxy that
       * machine dials through, which CDN edge it fronts, which interface it
       * leaves by, what it falls back to. Everything else about the far end is
       * derived from this one; these are the only answers that have to be
       * given. They were unreachable before: the form hid them because you were
       * not setting up that side, and you were not setting up that side, so
       * they could only be set by logging into it.
       *
       * The fields are the existing ones, moved and renamed — peerConn.* rather
       * than conn.*, so the submit puts them on the far end instead of this one.
       */
      function buildPeerGroup(root) {
        const rev = root.querySelector('.step3rev');
        if (!rev || rev.querySelector('#peerGrp')) return;
        /* Every ConnTune setting that is the client's own.
         *
         * SimpleAuth is left behind deliberately: it is applied before the role
         * check in ConnTune.apply, so it belongs to both ends and is not the
         * far end's to answer alone. */
        const wanted = ['conn.edgeIP', 'conn.proxy', 'conn.interface', 'conn.localAddr',
          'conn.fallbackAddrs', 'conn.loadBalance', 'conn.healthFailover'];
        const fields = wanted
          .map(n => {
            const el2 = root.querySelector(`.step3rev [name="${n}"]`)
              // The switches and menus are wired to a hidden input beside them,
              // so the row is found from the control when there is no field yet.
              || root.querySelector(`.step3rev [id$="${n.split('.')[1]}"]`);
            return el2?.closest('.f3, .tg3');
          })
          .filter(Boolean);
        if (!fields.length) return;

        const grid = el('div', { class: 'fgrid' });
        fields.forEach(f => {
          // data-when decided which side saw the field; the section it is
          // moving into answers that now, so the marker goes and the field is
          // taken out of the hidden state it was left in. What must survive is
          // the transport rule, and that lives in applyFields.
          f.removeAttribute('data-when');
          f.hidden = false;
          const input = f.querySelector('[name]');
          input.name = 'peerConn.' + input.name.slice('conn.'.length);
          // The labels said "kharej only" when this side was the one being set
          // up. Under a heading that names the server, that is now noise.
          f.querySelectorAll('.sidechip').forEach(c => c.remove());
          grid.append(f);
        });
        const grp = el('div', { class: 'grp3', id: 'peerGrp' }, [
          el('div', { class: 'gl3', text: 'The other server' }, [
            el('span', { class: 'sidechip', text: 'that machine only' }),
          ]),
          grid,
        ]);
        rev.append(grp);
        applyFields();
      }

      /* No token to read, so no token to type.
       *
       * There are four token fields in this form — each side has one it
       * generates and one it pastes, for reverse and for direct — because the
       * secret used to be carried between two machines by a person. One of them
       * has no name attribute at all, so the value it shows has never been
       * submitted by anything.
       *
       * None of that survives contact with a panel that writes both ends. The
       * token is generated here, kept in a variable, and put into the payload
       * on the way out; every field is hidden and none of them is read. That is
       * one place the secret exists instead of four, and nothing depends on
       * which of the four the operator happened to be looking at.
       */
      let autoTok = '';

      function autoToken(root) {
        root.querySelectorAll('[name="token"], #atok').forEach(i => {
          const box = i.closest('.f3');
          // Marked, not just hidden: applyShape sets the same attribute from
          // the side, and would put them back on the next change.
          if (box) box.classList.add('tokgone');
        });
        applyShape();
        api.tunnelToken()
          .then(r => { autoTok = r.token || ''; })
          .catch(() => { /* the create will say the token is missing */ });
      }

      /* Five steps, in the order the work happens.
       *
       * The wizard was built around doing this twice: pick a side, fill it in,
       * then carry the paired values to the other machine. With a managed
       * server there is one pass and it covers both ends, so the steps are the
       * parts of the tunnel rather than the halves of the job — this server,
       * that server, how fast, what else, and then what happened.
       *
       * The panes are the existing ones, moved. Nothing here re-renders a field
       * or re-implements a drawer; the groups keep their markup, their names and
       * their bindings, and only their parent changes.
       */
      const STAGES = ['Iran side', 'Kharej side', 'Performance', 'Optional', 'Done'];

      function restage(root) {
        const body = root.querySelector('.body5');
        const details = root.querySelector('.step[data-s="1"]');
        if (!body || !details || root.dataset.staged) return;
        root.dataset.staged = '1';

        /* A group is about to leave the container that decided whether it is
           shown, so it takes that decision with it.
           
           The drawers are tagged too, and they need it more: Fine Tune,
           Connectivity and the packet carrier fill tune.*, conn.* and pck.*,
           and a direct tunnel has none of those. Left visible, opening one on a
           direct tunnel put keys in the payload that the create handler refuses
           by name — so the whole submission failed because a drawer was open. */
        root.querySelectorAll('.step3rev .grp3, .step3rev .dr2b')
          .forEach(g => { g.dataset.mode = 'rev'; });
        root.querySelectorAll('.step3direct .grp3, .step3direct .dr2b')
          .forEach(g => { g.dataset.mode = 'dir'; });

        const pane = () => el('div', { class: 'step', hidden: true });
        const kharej = pane(), perf = pane(), opt = pane();

        const take = (label, into) => {
          root.querySelectorAll('.grp3').forEach(g => {
            const head = g.querySelector('.gl3')?.childNodes[0]?.textContent?.trim();
            if (head === label) into.append(g);
          });
        };
        take('Performance', perf);
        take('Optional', opt);
        const peer = root.querySelector('#peerGrp');
        if (peer) kharej.append(peer);

        // The side question is gone, not hidden: with a managed server there is
        // no second machine to go and set up, so there is nothing to ask.
        root.querySelector('.step[data-s="0"]')?.remove();

        details.after(kharej);
        kharej.after(perf);
        perf.after(opt);

        [...body.querySelectorAll('.step')].forEach((el2, i) => {
          el2.dataset.s = String(i);
          el2.hidden = i !== 0;
        });

        const rail = root.querySelector('.steps');
        if (rail) {
          rail.innerHTML = '';
          STAGES.forEach((lb, i) => {
            if (i) rail.append(el('span', { class: 'bar4' }));
            rail.append(el('span', { class: 'st2' + (i ? '' : ' on'), dataset: { s: String(i) } }, [
              el('span', { class: 'n3', text: String(i + 1) }),
              el('span', { class: 'lb4', text: lb }),
            ]));
          });
        }
        const back = root.querySelector('#backb');
        if (back) back.disabled = true;
        paintNav(0);
      }

      /* A step with nothing in it is not a step.
       *
       * The five are the parts of a reverse tunnel. A direct one has no
       * connectivity drawer for the far end and no Optional group at all, so
       * two of the panes hold groups that are all hidden for it — and pressing
       * Continue landed on a blank screen with a Continue button, twice.
       *
       * Rather than build filler for them, the wizard skips what is empty and
       * says so in the rail: the steps that exist are the ones that have
       * something to ask. It is recomputed on every change because the answer
       * depends on the mode and the transport, not on the shape of the form.
       */
      const paneList = () => [...root.querySelectorAll('.step[data-s]')];

      function paneEmpty(pane, i, of) {
        // The last one is the result, which is not made of groups.
        if (i === of - 1) return false;
        return ![...pane.querySelectorAll('.grp3, .dr2b')].some(g => !g.hidden);
      }

      function paintRail() {
        if (!root.dataset.staged) return;
        const panes = paneList();
        const rail = root.querySelector('.steps');
        if (!rail) return;
        let n = 0;
        panes.forEach((pane, i) => {
          const entry = rail.querySelector(`.st2[data-s="${i}"]`);
          if (!entry) return;
          const skip = paneEmpty(pane, i, panes.length);
          entry.hidden = skip;
          const bar = entry.nextElementSibling;
          if (bar && bar.classList.contains('bar4')) bar.hidden = skip;
          if (!skip) {
            n += 1;
            const num = entry.querySelector('.n3');
            if (num) num.textContent = String(n);
          }
        });
      }

      /* Travelling on from a step that has nothing to show, in the direction
         the operator was already going. */
      let lastStep = 0;
      function skipEmpty(at) {
        const panes = paneList();
        if (!root.dataset.staged || !panes[at]) return false;
        if (!paneEmpty(panes[at], at, panes.length)) return false;
        const dir = at >= lastStep ? 1 : -1;
        const to = at + dir;
        if (to < 0 || to >= panes.length) return false;
        panes.forEach((x, i) => { x.hidden = i !== to; });
        root.dispatchEvent(new CustomEvent('step', { detail: { at: to, of: panes.length } }));
        return true;
      }

      /* The footer says what the next press does. On the step before the last
         one it is not "continue" — it is the moment the tunnel gets built, and a
         button that does something irreversible should say so. */
      function paintNav(at) {
        const next = root.querySelector('#nextb');
        const note = root.querySelector('#note4');
        if (!next || !root.dataset.staged) return;
        const last = root.querySelectorAll('.step[data-s]').length - 1;
        if (at === last) {
          next.textContent = 'Close';
          next.disabled = false;
          next.onclick = () => { close(); go('/'); };
        } else {
          next.textContent = at === last - 1 ? 'Create the tunnel' : 'Continue';
          next.onclick = null;
        }
        if (note) note.textContent = at === last - 1
          ? 'Both ends are written when you press this.' : '';
      }

      /* The wizard is held back until the fleet has answered.
       *
       * Whether there are managed servers decides how many steps there are and
       * what the first one is, and that answer arrives a moment after the
       * dialog opens. Showing the old first step and then rearranging it under
       * the operator is worse than showing nothing for that moment — they were
       * reading it. The hiding is in add.css, because the markup is on screen
       * before this runs.
       */
      const reveal = () => root.classList.add('ready');
      // Never left hidden by a request that hangs.
      setTimeout(reveal, 2500);

      const nodeMsg = root.querySelector('#nodeMsg');
      const nodeAddr = new Map();   // name -> the address it reported
      let peerIP = '';               // the one for the server that was picked
      api.nodes().then(state => {
        const live = (state.nodes || []).filter(n => n.online);
        if (!live.length || !nodeSel || !nodeGrp) { reveal(); return; }
        live.forEach(n => {
          nodeSel.append(el('option', { value: n.name, text: n.name }));
          const ip = n.info?.ipv4;
          if (ip && ip !== '-') nodeAddr.set(n.name, ip);
        });
        nodeGrp.hidden = false;

        /* With a managed server there is no second pass, so the form stops
         * offering one.
         *
         * "Which machine are you setting up" is a question that only exists
         * because the operator used to have to answer it twice — once here and
         * once over there. A server that is managed from this panel is written
         * from this panel, so this side is Iran and the other side is that
         * server; asking which of the two you are in front of would be asking
         * about a step that no longer happens.
         *
         * The choice comes back the moment it means something again: it is
         * hidden, not removed, and a panel with no managed servers is the form
         * exactly as it was.
         */
        const kharej = root.querySelector('[data-fn="setSide"][data-args*="client"]');
        const iran = root.querySelector('[data-fn="setSide"][data-args*="server"]');
        if (kharej) kharej.hidden = true;
        if (iran) { chosen.side = 'server'; markGroup('setSide', 'server'); }
        const lede = root.querySelector('.step[data-s="0"] .lede2');
        if (lede) {
          lede.innerHTML = 'This panel is the <b>Iran</b> side. The other end is written on the '
            + 'server you pick in the next step — you do not set it up again over there.';
        }
        markSides(root);
        buildPeerGroup(root);
        autoToken(root);
        restage(root);

        // The last step hands the settings over to be typed in somewhere else.
        // Nothing is handed over here.
        const sub = root.querySelector('.dh small, .ttl small');
        if (sub) sub.textContent = sub.textContent.split('·')[0].trim()
          + ' · both ends are written from here';

        // One server is not a choice worth making; it is preselected and can
        // still be changed if another comes along.
        if (live.length === 1) {
          nodeSel.value = live[0].name;
          nodeSel.dispatchEvent(new Event('change'));
        }
        applyShape();

      }).catch(() => { /* the fleet is an offer, not a requirement */ });

      /* Picking a server and pasting a link from one are the same job done two
         ways, so only one of them is ever on screen. */
      nodeSel?.addEventListener('change', () => {
        if (pasteGrp) pasteGrp.hidden = !!nodeSel.value;

        /* The address is not asked for, because it is already known.
         *
         * A managed server dials this panel, and reports what it is when it
         * gets there — hostname, version, addresses. This side of a direct
         * tunnel needs that address, and it is a worse answer coming from a
         * person: it can be mistyped, and it goes stale when the machine's
         * address changes. So the field goes, the value is carried in the
         * payload, and it is shown here as a fact rather than a question.
         */
        peerIP = nodeAddr.get(nodeSel.value) || '';
        const addr = root.querySelector('[name="peerAddr"], [name="serverAddr"]');
        const box = addr?.closest('.f3');
        if (box) box.classList.toggle('addrgone', !!nodeSel.value && !!peerIP);
        if (addr && peerIP) addr.value = peerIP;
        applyShape();

        if (nodeMsg) {
          nodeMsg.hidden = !(nodeSel.value && peerIP);
          if (!nodeMsg.hidden) {
            nodeMsg.querySelector('span:last-child').textContent =
              `${nodeSel.value} reports its address as ${peerIP}. Nothing else about it needs entering.`;
          }
        }
      });

      root.querySelector('#apasteb')?.addEventListener('click', applyPastedLink);
      root.querySelector('#apaste')?.addEventListener('paste', () => setTimeout(applyPastedLink, 0));

      /* Building the tunnel.
       *
       * It used to hang off a button that is not in this markup, so the form
       * collected everything and posted it nowhere. It is a function now, run
       * when the wizard reaches its last step — which is also where the result
       * is shown, so pressing Continue on the step before it is the commit.
       */
      async function submitTunnel() {
        /* Only what applies: a hidden field belongs to the other side or the
           other kind of tunnel, and sending it would describe a tunnel nobody
           asked for.
           
           A step that is not the one on screen is hidden too, and that is a
           different thing entirely. The tunnel is built from the last step, by
           which point every step holding a field is hidden — so testing for any
           hidden ancestor collected nothing at all and posted an empty form.
           The panes are skipped over; everything else still counts. */
        const irrelevant = n => {
          for (let at = n.parentElement; at && at !== root; at = at.parentElement) {
            if (at.hidden && !at.classList.contains('step')) return true;
          }
          return false;
        };
        const payload = {};
        root.querySelectorAll('input[name], select[name]').forEach(n => {
          if (irrelevant(n)) return;
          const v = n.type === 'checkbox' ? n.checked : n.value.trim();
          if (v === '' || v === false) return;
          const keys = n.name.split('.'), last = keys.pop();
          let at = payload;
          for (const k of keys) at = at[k] ??= {};
          at[last] = NUMERIC.has(n.name) ? Number(v) : v;
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

        const onNode = nodeSel?.value || '';
        const direct = chosen.direction === 'direct';
        // Collected by name like everything else, then lifted out: it describes
        // the other machine and must not be written onto this one.
        const peerConn = payload.peerConn;
        delete payload.peerConn;
        // Every token field is hidden in this flow, so the collector skipped
        // them all — correctly. The one the panel generated goes in here, and
        // so does the address the server reported for itself.
        if (onNode && autoTok) payload.token = autoTok;
        if (onNode && peerIP && direct) payload.peerAddr ||= peerIP;

        const r = onNode
          ? await api.nodePair({
              node: onNode,
              kind: direct ? 'direct' : 'reverse',
              [direct ? 'direct' : 'tunnel']: payload,
              ...(peerConn ? { peerConn } : {}),
            })
          : direct ? await api.directCreate(payload)
                   : await api.tunnelCreate(payload);
        return { r, onNode, name: payload.name };
      }

      ctx.setTeardown(close);
    },
  }).catch(oops);
}

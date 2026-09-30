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
import { setupLinkHTML, bindSetupLink } from '../ui/setuplink.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { oops, toast } from '../ui/toast.js';
import { go } from '../router.js';

export function addView(ctx) {
  openScreen('add', {
    pick: '.dlg',
    bind: async (root, close) => {
      dialogSubtitle(root, store.get().stats, 'this server’s end — the other is built from its setup link');
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

      /* The preset the form is already showing counts as chosen.
       *
       * It started as null and was only filled when somebody pressed one of
       * the buttons — but one of them is marked `on` in the markup, so the
       * form opens with Balance visibly selected. Accepting what is on screen
       * therefore sent no preset at all, and the tunnel was written without
       * one: the config had no preset line, the edit dialog read back an empty
       * value, and its menu fell to whichever option the preview was drawn
       * with. What the operator saw when they made the tunnel and what they
       * saw when they edited it disagreed, and neither was wrong about the
       * other — nothing had been recorded either way.
       *
       * Read from the markup rather than hard-coded, so the default is
       * whichever button carries `on`, and it stays right if that changes. */
      /* Reverse and direct each have a row of presets, and both are in the
         page. The first `.rp.on` in the document is the reverse row's, so a
         direct tunnel read Balance from a row it could not see. Only the row
         in the shape on screen counts — a step being hidden is not the row
         being hidden, which is why steps are passed over. */
      const inShape = n => {
        for (let at = n; at && at !== root; at = at.parentElement) {
          if (at.hidden && !at.classList.contains('step')) return false;
        }
        return true;
      };
      const markedPreset = () => {
        const live = [...root.querySelectorAll('.rp')].filter(inShape);
        const b = live.find(x => x.classList.contains('on')) || live[0];
        return (b?.querySelector('.key2, .k3')?.textContent || b?.dataset.pre || '')
          .trim().toLowerCase() || null;
      };

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
        /* Whatever is marked is what will be sent. The `on` class is what the
           operator can see, so it is the one source of truth here — and it is
           re-read after the list changes, because Throughput disappearing moves
           the selection and the payload has to follow the screen. */
        chosen.preset = markedPreset();
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

      /* The direct carriers, painted from the server rather than written into
         the page.
         
         They were a fourth hardcoded list — after the CLI wizard's, the panel's
         options endpoint and the create path's — and the four drifted: a
         carrier added to the others was offered nowhere here, and the endpoint
         behind this screen refused what this screen did not know about. There
         is one list now and this reads it. */
      async function paintCarriers() {
        const grid = root.querySelector('.step3direct .trgrid');
        if (!grid) return;
        let carriers = [];
        try { carriers = (await api.directOptions()).carriers || []; } catch (e) { return; }
        if (!carriers.length) return;
        grid.innerHTML = carriers.map((c, i) => {
          const needsRoot = c.needsRoot ? '<span class="root2">needs root</span>' : '';
          return `<button class="dc${i === 0 ? ' on' : ''}" data-car="${esc(c.value)}"
            data-fn="setCar" data-args="'${esc(c.value)}'" title="${esc(c.desc || '')}">
            <span class="tn2"><b>${esc(c.label)}</b>
            <span class="key2">${esc(c.value)}</span>${needsRoot}</span></button>`;
        }).join('');
        chosen.carrier = carriers[0].value;
        applyShape();
      }
      paintCarriers();

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
          input.dataset.drawn = '1';
          sw.after(input);
          sw.setAttribute('role', 'switch');
          sw.setAttribute('aria-checked', String(input.checked));
          sw.tabIndex = 0;
          const flip = () => {
            sw.classList.toggle('on');
            input.checked = sw.classList.contains('on');
            delete input.dataset.drawn;
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
          input.dataset.drawn = '1';
          sel.after(input);
          sel.setAttribute('role', 'combobox');
          sel.tabIndex = 0;

          const menu = el('div', { class: 'sel4menu', hidden: true });
          choices.forEach(c => {
            const opt = el('button', { type: 'button', class: 'sel4opt', text: c.label });
            opt.addEventListener('click', ev => {
              ev.stopPropagation();
              input.value = c.value;
              delete input.dataset.drawn;
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

      /* What a direct tunnel should start out as.
       *
       * /api/direct/defaults answers with a subnet nothing on this machine is
       * using, a free interface name and a preset. The route and the handler
       * were both registered; the wrapper that calls them was never written, so
       * the panel never asked. The two fields that endpoint exists for are the
       * two an operator is least able to guess — the tunnel's own /30 has to
       * avoid every subnet already on the box, and picking one by hand is how
       * you end up with a tunnel that comes up and blackholes the route it was
       * built for.
       *
       * Only empty fields are filled, and only once per side: this runs from
       * applyShape, which fires on every change, and overwriting what somebody
       * has typed because they clicked something else is worse than not
       * suggesting at all. */
      /* The fields this fills, and only these.
       *
       * The endpoint also answers with a preset and a tunnel port. The preset
       * is a row of buttons rather than a field, and the tunnel port exists on
       * both shapes of this form and already has a Random button beside it —
       * filling either from here would be reaching past what the endpoint is
       * for. The subnet is what it is for: a tunnel's own /30 has to avoid
       * every subnet already on the box, and picking one by hand is how you get
       * a tunnel that comes up and blackholes the route it was built for. */
      const SUGGESTED_FIELDS = ['localIp', 'peerIp'];

      const suggestedFor = {};
      let suggestion = null;

      /* Fetched once per side, applied every time the shape is drawn.
       *
       * Applying it on the fetch alone does not work, and the reason is worth
       * writing down: this bind rearranges the form into five steps after the
       * template loads, so the field that exists when the answer arrives is not
       * the field the operator ends up typing into. Re-applying is free — it
       * only ever writes into a field that is still empty — and it lands
       * whenever the field appears, in whatever order the rest of the bind
       * happens to run. */
      function applySuggestion() {
        if (!suggestion) return;
        for (const name of SUGGESTED_FIELDS) {
          const value = suggestion[name];
          if (!value) continue;
          for (const f of root.querySelectorAll(`[name="${name}"]`)) {
            if (!f.value) f.value = value;
          }
        }
      }

      async function suggestDirect(side) {
        applySuggestion();
        if (suggestedFor[side]) return;
        suggestedFor[side] = true;
        try {
          suggestion = await api.directDefaults(side === 'server' ? 'iran' : 'kharej');
        } catch (e) {
          suggestedFor[side] = false;
          return;
        }
        applySuggestion();
      }

      function applyShape() {
        const direct = chosen.direction === 'direct';
        show('.step3rev', !direct);
        show('.step3direct', direct);
        if (direct) suggestDirect(chosen.side);

        /* "server" is the Iran side, "client" the kharej side — the words the
           create endpoints use. */
        show('[data-when="server"]', chosen.side === 'server');
        show('[data-when="client"]', chosen.side === 'client');
        /* A field that belongs to one carrier, or to one side of one carrier.
           These read as conditions joined by "-": "client-spoof" is the kharej
           side of the spoof carrier, "sni" is the sni carrier whichever side
           this is. Nothing evaluated them before, so a row marked for one
           carrier was shown on every direct tunnel, on both sides — including
           the one telling the operator it was required. */
        root.querySelectorAll('[data-when]').forEach(n => {
          const when = n.dataset.when;
          if (when === 'server' || when === 'client' || when === 'tls') return;
          const row = n.closest('.f3, .tg3, .f, .row') || n;
          const ok = when.split('-').every(part => {
            if (part === 'server' || part === 'client') return chosen.side === part;
            return chosen.carrier === part;
          });
          row.hidden = !ok;
        });
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
        /* Direct has no applyPresets, so without this an untouched direct
           form sent no preset and the server took its default: Turbo, under a
           row showing Balance. */
        else chosen.preset = markedPreset();
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

        /* One shape, because there is only one way to make a tunnel here now.
         *
         * This step used to have two: a pane that watched the tunnel come up,
         * and a pane that handed the operator a setup link and four values to
         * type into a second panel on the other server. The second one is gone.
         * The panel writes both ends over SSH, so there is nothing to carry
         * anywhere and nothing to type twice — and a form that still offered to
         * would be offering a way to build half a tunnel. */
        const hand = step.querySelector('.handpane');
        const conn = step.querySelector('.connpane');
        if (hand) hand.remove();
        if (conn) conn.hidden = false;
        if (conn) runBuild(conn);
      }

      /* Building this end, said out loud.
       *
       * Two stages, each a fact from the answer rather than a timer: the config
       * written, and the service up. Then the setup link — the other end is
       * the operator's to build, and the line that does it is what this screen
       * owes them, the way the menu's wizard prints it. */
      let building = false;
      async function runBuild(conn) {
        if (!conn || building) return;
        building = true;
        const rows = [...conn.querySelectorAll('.sg')];
        const title = conn.querySelector('#connTitle');
        const sub = conn.querySelector('#connSub');
        const result = conn.querySelector('#connResult');
        const spin = conn.querySelector('.spin');
        const t0 = Date.now();

        rows.slice(2).forEach(r => r.remove());
        ['Writing the configuration', 'Starting it here'].forEach((lb, i) => {
          const tx = rows[i]?.querySelector('.tx6');
          if (tx) tx.textContent = lb;
          rows[i]?.classList.remove('done', 'doing', 'failed');
        });
        if (title) title.textContent = 'Building…';
        if (sub) sub.textContent = 'This server’s end, from what you filled in.';

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
          if (sub) sub.textContent = 'This server refused the settings.';
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
        settle(0, true);
        await pause(240);
        start(1); await pause(200);
        const up = r.active !== false;
        settle(1, up);
        spin?.setAttribute('hidden', '');
        store.refresh();

        if (title) title.textContent = up ? 'Built on this server' : 'Created, not up yet';
        if (sub) sub.textContent = up ? 'Now build the other end with the line below.'
          : 'The config is written but the service did not start — its log says why.';
        if (result) {
          result.innerHTML = `<div class="sl-wait">Building the setup link…</div>`;
          bindSetupLink(result);
          try {
            result.innerHTML = setupLinkHTML(await api.tunnelLink(name));
          } catch (e) {
            result.innerHTML = `<div class="doneline warn"><span class="tick">!</span><div>
              <b>${esc(e.message)}</b><span>Open the tunnel's menu → Setup link to try again.</span></div></div>`;
          }
        }
        building = false;
      }

      /* The single-ended flow, unchanged: nothing is created here, the tunnel
         already exists, and this only waits to see it come up. */

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
        if (fn === 'setPre' || fn === 'setTr' || fn === 'setSide' || fn === 'mode3') showPresetDefaults();
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
        paintPresetSets(d);
      }

      /* Under the preset cards: what the chosen one actually sets on this
         transport, read from the same answer the Fine Tune drawer shows. The
         step was three buttons and a screen of nothing; this is the thing an
         operator comparing them wants to see. */
      const DIRECT_SETS = {
        turbo: [['Socket buffer', '8 MB'], ['Queue', 'fq_codel'], ['Suits', 'most links']],
        balance: [['Socket buffer', 'smallest'], ['Queue', 'fq_codel'], ['Suits', 'small VPS']],
        aggressive: [['Socket buffer', '32 MB'], ['Queue', 'deep'], ['Suits', 'fast, bursty links']],
      };
      function paintPresetSets(d) {
        root.querySelectorAll('.rpgrid').forEach(grid => {
          let box = grid.nextElementSibling;
          if (!box || !box.classList.contains('rp-sets')) {
            box = el('div', { class: 'rp-sets' });
            grid.after(box);
          }
          const direct = !!grid.closest('[data-mode="dir"], .step3direct');
          const p = chosen.preset || 'turbo';
          let cells;
          if (direct) cells = DIRECT_SETS[p] || [];
          else {
            const n = (k, unit = '') => (d[k] ? [d[k] + unit] : []);
            cells = [
              ['Keepalive', ...n('keepAlive', ' s')], ['Heartbeat', ...n('heartbeat', ' s')],
              ['Channel', ...n('channelSize')],
              ...(isMux(chosen.transport) ? [['Mux streams', ...n('muxCon')]] : []),
              ...(isKCP(chosen.transport) ? [
                ['KCP window', ...(d.kcpSndWnd ? [`${d.kcpSndWnd}/${d.kcpRcvWnd}`] : [])],
                ['FEC', ...(d.kcpDataShards ? [`${d.kcpDataShards}+${d.kcpParityShards}`] : [])]] : []),
              ['No-delay', d.nodelay ? 'on' : 'off'],
            ].filter(c => c.length === 2);
          }
          const label = p[0].toUpperCase() + p.slice(1);
          box.innerHTML = `<div class="rp-sets-h"><b>What ${esc(label)} sets</b>
              <small>${direct ? 'on this direct tunnel' : `on ${esc((chosen.transport || '').toUpperCase())}`} · change any of it under Optional → Fine Tune</small></div>
            <div class="rp-sets-g">${cells.map(([k, v], i) =>
              `<div style="--d:${i * 35}ms"><span>${esc(k)}</span><b>${esc(String(v))}</b></div>`).join('')}</div>`;
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

      /* Random asks the server for a port that is free on THIS machine, which
         is the right answer for exactly one field: the tunnel port, which this
         side binds.
       *
       * It used to be bound to every button labelled Random, and one of those
       * sat beside Forwarded ports — a field whose own hint says the value is
       * forwarded to the same port on the kharej machine. A port chosen for
       * being free here is, by construction, one nothing is listening on
       * there, so the button could only ever produce a tunnel that comes up,
       * reports a peer, and refuses every connection at the last hop. */
      [...root.querySelectorAll('.withb')]
        .filter(w => w.querySelector('input[name="tunnelPort"]'))
        .forEach(wrap => wrap.querySelectorAll('button').forEach(b => {
          if (!/^random$/i.test(b.textContent.trim())) return;
          b.addEventListener('click', async () => {
            try {
              const r = await api.tunnelSuggest();
              const field = wrap.querySelector('input[name="tunnelPort"]');
              if (field && r.port) field.value = r.port;
            } catch (e) { oops(e); }
          });
        }));

      /* Forwarded ports get a Random of their own, asked for by name: a port
         free on this server, added to the list. It is a port for users to
         reach this server on, and the kharej hands it to the same port there —
         which the hint under the field now says in as many words, because the
         service on the kharej has to be listening on it. */
      root.querySelectorAll('input[name="ports"]').forEach(inp => {
        if (inp.closest('.withb')) return;
        const wrap = el('div', { class: 'withb' });
        inp.before(wrap);
        wrap.append(inp);
        const b = el('button', { type: 'button', class: 'mini3 rnd', text: 'Random' });
        wrap.append(b);
        b.addEventListener('click', async () => {
          try {
            const r = await api.tunnelSuggest();
            if (!r.port) return;
            const have = inp.value.split(',').map(x => x.trim()).filter(Boolean);
            if (!have.includes(String(r.port))) have.push(String(r.port));
            inp.value = have.join(', ');
            inp.dispatchEvent(new Event('input', { bubbles: true }));
          } catch (e) { oops(e); }
        });
        const hint = wrap.parentElement?.querySelector('.hint');
        if (hint) hint.textContent = 'Users connect to these ports on this server. A bare port is handed to the same '
          + 'port on the kharej, so the service there has to listen on it — 443=8080 sends this server’s 443 to '
          + 'the kharej’s 8080. Random adds a port that is free here. Separate several with commas.';
      });

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

      /* Neither the setup link nor the pane that pasted one back is here any
         more. Both existed for a second panel on the other server, typing the
         same values in again; the panel writes that end itself now. */

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
        if (pane.querySelector('.choices')) return false;
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
          ? 'This server’s end is written when you press this.' : '';
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

      /* One server: this one.
       *
       * The wizard used to need a managed server to write the far end on, and
       * refused to build anything without one. Servers is out of the panel, so
       * the form is what the CLI wizard is — Setup Iran or Setup Kharej, on the
       * machine in front of you — and the other end is built from the setup
       * link this screen hands over at the end. Six steps, in the order the
       * menu asks: which side, which kind, the tunnel, how fast, what else,
       * and what happened. */
      const SINGLE = ['Type', 'Tunnel', 'Performance', 'Optional', 'Done'];

      /* The preset cards: the three (four, on udp+kcp) profiles as cards that
         fill the row, each with its own mark and what it costs — they were
         three small buttons in an empty step. The button keeps its class, its
         data-pre and its .key2, because those are what the rest of the form
         reads. */
      const PRESET_ART = {
        balance: { icon: '<svg viewBox="0 0 24 24"><path d="M12 3v18"/><path d="M5 7h14"/><path d="M5 7l-3 7a3.5 3.5 0 006 0z"/><path d="M19 7l-3 7a3.5 3.5 0 006 0z"/><path d="M8 21h8"/></svg>',
          line: 'Light on CPU and memory', fits: 'Small or shared VPS · several tunnels on one box', load: 1 },
        turbo: { icon: '<svg viewBox="0 0 24 24"><path d="M13 2L4.5 13.5H11L10 22l8.5-11.5H12z"/></svg>',
          line: 'The tuned default', fits: 'Most Iran → abroad links', load: 2, rec: true },
        aggressive: { icon: '<svg viewBox="0 0 24 24"><path d="M12 22c4.4 0 7-2.9 7-6.8 0-3.6-2.4-6-4.2-8.2-.4 2.2-1.6 3.4-2.8 4C12.3 7.6 10.4 4.6 8 2c.2 3.5-3 6-3 11.2C5 18.9 7.6 22 12 22z"/><path d="M12 22c-1.7 0-3-1.3-3-3.2 0-1.8 1.4-2.9 3-4.8 1.6 1.9 3 3 3 4.8 0 1.9-1.3 3.2-3 3.2z"/></svg>',
          line: 'Maximum headroom', fits: 'Strong servers · gaming · fast links with bursts', load: 3 },
        throughput: { icon: '<svg viewBox="0 0 24 24"><path d="M4 18a8 8 0 1116 0"/><path d="M12 18l4.5-6"/><circle cx="12" cy="18" r="1.4"/></svg>',
          line: 'Bandwidth over steady ping', fits: 'udp + kcp + fec only', load: 3 },
      };
      function dressPresets() {
        root.querySelectorAll('.rp').forEach(b => {
          const v = (b.dataset.pre || b.querySelector('.key2')?.textContent || '').trim().toLowerCase();
          const a = PRESET_ART[v];
          if (!a || b.dataset.dressed) return;
          b.dataset.dressed = '1';
          const label = v[0].toUpperCase() + v.slice(1);
          b.classList.add('rp2');
          b.innerHTML = `<span class="rp-ic">${a.icon}</span>
            <span class="rp-tx"><b>${label}${a.rec ? '<em>recommended</em>' : ''}</b>
              <span class="rp-line">${a.line}</span><small>${a.fits}</small></span>
            <span class="rp-load" title="CPU and memory it takes">${[1, 2, 3].map(i => `<i class="${i <= a.load ? 'on' : ''}"></i>`).join('')}</span>
            <span class="key2" hidden>${v}</span>`;
        });
        root.querySelectorAll('.rp2').forEach(b => b.parentElement?.classList.add('rpgrid'));
      }

      function stageSingle() {
        const body = root.querySelector('.body5');
        const side = root.querySelector('.step[data-s="0"]');
        const details = root.querySelector('.step[data-s="1"]');
        if (!body || !side || !details || root.dataset.staged) return;
        root.dataset.staged = '1';
        root.querySelector('#nodeGrp')?.remove();

        root.querySelectorAll('.step3rev .grp3, .step3rev .dr2b')
          .forEach(g => { g.dataset.mode = 'rev'; });
        root.querySelectorAll('.step3direct .grp3, .step3direct .dr2b')
          .forEach(g => { g.dataset.mode = 'dir'; });

        /* This server is the Iran end.
         *
         * The panel runs where tunnels are started from, and that is Iran: the
         * kharej end is made from the setup link at the end, with one line, and
         * a kharej-side form here was a second way to do the same thing that
         * nobody needed. The side question is gone; the side buttons stay in
         * the markup, hidden, because the rest of the form listens to them. */
        chosen.side = 'server';
        root.querySelector('[data-fn="setSide"][data-args*="server"]')?.click();
        side.querySelector('.choices')?.setAttribute('hidden', '');
        const lede = side.querySelector('.lede2');
        if (lede) lede.hidden = true;

        /* Reverse or direct, in cards under that. It was a small switch above
           the transport list, easy to miss and the one choice that changes
           every field after it. The switch is kept, hidden, because it is what
           the rest of the form listens to. */
        const kind = el('div', { class: 'typegrp' });
        kind.innerHTML = `<div class="here-card">
            <span class="here-flag">🇮🇷</span>
            <div><b>Iran — this server</b><small>This end is built here. The kharej is set up from the one line
              you get at the end: paste it there and the tunnel comes up.</small></div>
            <span class="here-pill"><i></i>this server</span></div>
          <div class="lede2 kindq">How do the two servers reach each other?</div>
          <div class="grp3 kindgrp"><div class="choices">
            <button type="button" class="ch4" data-kind="rev">
              <span class="ic4"><svg viewBox="0 0 24 24"><path d="M20 12H4"/><path d="M10 6l-6 6 6 6"/></svg></span>
              <b>Reverse<span class="rec">usual</span></b>
              <i>The kharej dials in to this server. Ten transports, from plain TCP to WebSocket behind a CDN — what to try first.</i>
              <div class="diagram">🌍 kharej <b>→</b> 🇮🇷 Iran</div></button>
            <button type="button" class="ch4" data-kind="dir">
              <span class="ic4"><svg viewBox="0 0 24 24"><path d="M4 12h16"/><path d="M14 6l6 6-6 6"/></svg></span>
              <b>Direct</b>
              <i>This server dials out to the kharej over a layer-3 carrier. For paths where reverse is filtered.</i>
              <div class="diagram">🇮🇷 Iran <b>→</b> 🌍 kharej</div></button>
          </div></div>`;
        side.append(kind);
        const swap = root.querySelector('.modeswap');
        const markKind = () => kind.querySelectorAll('[data-kind]').forEach(b =>
          b.classList.toggle('on', b.dataset.kind === (chosen.direction === 'direct' ? 'dir' : 'rev')));
        kind.addEventListener('click', ev => {
          const b = ev.target.closest('[data-kind]');
          if (!b) return;
          swap?.querySelector(`[data-args*="${b.dataset.kind}"]`)?.click();
          markKind();
        });
        if (swap) swap.hidden = true;
        markKind();
        dressPresets();

        const pane = () => el('div', { class: 'step', hidden: true });
        const perf = pane(), opt = pane();
        root.querySelectorAll('.grp3').forEach(g => {
          const head = g.querySelector('.gl3')?.childNodes[0]?.textContent?.trim();
          if (head === 'Performance') perf.append(g);
          if (head === 'Optional') opt.append(g);
        });
        details.after(perf);
        perf.after(opt);

        [...body.querySelectorAll('.step')].forEach((x, i) => {
          x.dataset.s = String(i);
          x.hidden = i !== 0;
        });
        const rail = root.querySelector('.steps');
        if (rail) {
          rail.innerHTML = '';
          SINGLE.forEach((lb, i) => {
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
        applyShape();
      }
      stageSingle();
      reveal();

      /* The token, made here.
       *
       * The form still has the two-pass wording: one side "creates" the secret
       * with a Copy button and the other "pastes" it. With the setup link that
       * split is gone — whichever end is built first makes the token and the
       * link carries it to the other one. So every token field starts filled
       * with a fresh one from the server and stays editable, for the case where
       * the other end already exists and its token is the one to use. The
       * creating field on the reverse side was drawn with no name at all, which
       * is why a create from here used to be refused for having no token. */
      const tokenFields = [...root.querySelectorAll('#atok, [name="token"]')];
      tokenFields.forEach(i => {
        i.name = 'token';
        i.removeAttribute('data-unwired');
        const hint = i.closest('.f3')?.querySelector('.hint');
        if (hint) hint.textContent = 'Made for this tunnel — the setup link carries it to the other server. '
          + 'If that server is already set up, paste its token here instead.';
      });
      api.tunnelToken()
        .then(r => tokenFields.forEach(i => { if (!i.value || i.defaultValue === i.value) i.value = r.token || ''; }))
        .catch(() => tokenFields.forEach(i => { i.value = ''; i.placeholder = 'type a long random token'; }));

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
          /* A drawer switch or menu nobody touched says nothing. Posting what
             it was drawn with sent every knob in Fine Tune on every create, and
             a tunnel given its own numbers is a tunnel off its preset: pick
             Aggressive, get no preset line and the drawing's buffers — which
             are Turbo's. Touched, it is sent, off included. */
          if (n.dataset.drawn) return;
          const wired = n.hidden && n.type === 'checkbox';
          const v = n.type === 'checkbox' ? n.checked : n.value.trim();
          if (v === '' || (v === false && !wired)) return;
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

        // Only this end: the other is built from its setup link.
        const r = chosen.direction === 'direct'
          ? await api.directCreate(payload)
          : await api.tunnelCreate(payload);
        return { r, name: payload.name };
      }

      ctx.setTeardown(close);
    },
  }).catch(oops);
}

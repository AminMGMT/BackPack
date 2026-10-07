/* Edit one tunnel.
 *
 * The form is the preview's; the values come from /api/tunnel/settings and go
 * back through /api/tunnel/edit — the same call the CLI's edit screen makes, so
 * a tunnel edited here is byte-for-byte a tunnel edited in the terminal.
 *
 * CLI: Manage → Manage Tunnels → Edit.
 */

import { $$, el, esc } from '../lib/dom.js';
import { NUMERIC } from '../lib/numeric.js';
import { kindLabel, flag } from '../lib/format.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { oops, toast } from '../ui/toast.js';
import { go } from '../router.js';
import { setupLinkHTML, bindSetupLink } from '../ui/setuplink.js';

/* The Iran end of a reverse tunnel: the one whose edits the kharej follows. */
const tunnelIsIranEnd = (t, settings) => (settings.role || t?.role) === 'server';

/* After an edit both ends must agree on: the form gives way to the kharej's
   line, and Save becomes Done. */
function handOver(root, info, close) {
  const panes = root.querySelector('.panes');
  root.querySelectorAll('.tabs > button').forEach(b => { b.hidden = true; });
  if (panes) {
    panes.innerHTML = `<div class="pane ed-hand">
      <div class="ed-hand-h"><span class="ed-ok"></span><div><b>Saved on this server</b>
        <small>The kharej is still on the old settings. Run this there and both ends match again —
        it updates the tunnel it made before, it does not add a second one.</small></div></div>
      ${setupLinkHTML(info)}</div>`;
    bindSetupLink(panes);
  }
  root.querySelectorAll('.lede, .intro, .dsub').forEach(n => { n.hidden = true; });
  [...root.querySelectorAll('button')].filter(b => /^cancel$/i.test(b.textContent.trim()))
    .forEach(b => { b.hidden = true; });
  const note = root.querySelector('.note');
  if (note) note.textContent = 'Saved on this server.';
  const sub = root.querySelector('.dh .ttl small');
  if (sub) sub.textContent = 'saved here · the kharej follows with the line below';
  const save = root.querySelector('.save, [data-save], .primary');
  if (save) {
    const done = save.cloneNode(true);
    done.textContent = 'Done';
    done.disabled = false;
    save.replaceWith(done);
    done.addEventListener('click', close);
  }
}

/* The form's controls are named after the keys the server reads — dotted where
   the payload nests (tune.keepAlive, limits.bandwidthMbps) — so filling the
   form and reading it back are both one pass with no field list to drift.
   Five controls in the preview have no field behind them on the server; they
   carry data-unwired and are left alone rather than posted as invented keys. */
/* A <select> keeps its first option when the value it is handed matches none
   of them, which is how "turbo" quietly displayed as "Balanced". The server
   speaks in values ("turbo", "wss"); the preview's options were written with
   labels. Match either, and say so when neither fits. */
function setControl(node, v) {
  if (node.type === 'checkbox') { node.checked = !!v; return; }
  const val = Array.isArray(v) ? v.join(', ') : String(v ?? '');
  if (node.tagName !== 'SELECT') { node.value = val; return; }
  const want = val.trim().toLowerCase();
  const hit = [...node.options].find(o =>
    o.value.trim().toLowerCase() === want || o.textContent.trim().toLowerCase() === want);
  if (hit) { node.value = hit.value; return; }
  /* Nothing matched: show the server's value rather than a wrong one. */
  const opt = new Option(val, val, true, true);
  node.add(opt);
}

const dig = (obj, path) => path.split('.').reduce((o, k) => (o ?? {})[k], obj);

function plant(obj, path, value) {
  const keys = path.split('.');
  const last = keys.pop();
  let at = obj;
  for (const k of keys) at = at[k] ??= {};
  at[last] = value;
}

/* The two halves of the API disagree on one name.
 *
 * Reading a tunnel answers with serverHost; writing one takes serverAddr. The
 * field has to be named for the write — a key the edit handler does not know
 * fails the whole save, and that is what it used to do — so the read is
 * bridged here instead. One alias in one place, rather than a form that cannot
 * be filled or cannot be saved. */
const READ_ALIAS = { serverAddr: 'serverHost' };

function fill(root, values) {
  root.querySelectorAll('[name]').forEach(node => {
    let v = dig(values, node.name);
    if (v === undefined || v === null) v = dig(values, READ_ALIAS[node.name] || '');
    if (v === undefined || v === null) return;
    setControl(node, v);
  });
  root.querySelectorAll('[data-unwired]').forEach(n => {
    n.disabled = true;
    n.title = 'Not settable over the API — use the CLI';
  });
}

/* bindValue renders the tunnel port the way it is typed: "443" when the tunnel
   listens on every interface, "85.10.11.51:443" when it is pinned to one, and
   "[2a01:4f8::1]:443" for a v6 literal, which is the form the server's parser
   takes back. */
function bindValue(settings) {
  const port = settings.tunnelPort || '';
  const host = settings.bindHost || '';
  if (!host || !port) return port;
  return host.includes(':') ? `[${host}]:${port}` : `${host}:${port}`;
}

function read(root) {
  const out = {};
  root.querySelectorAll('input[name], select[name], textarea[name]').forEach(n => {
    /* Skipped, not sent as empty: TunnelEdit has no key for a setting this
       transport does not have, and one unknown key fails the entire save. The
       spoof drawer sits in every tunnel's markup, so every save from this
       screen was refused with `unknown field "spoof"`. */
    if (n.hasAttribute('data-off') || n.closest('[data-off]')) return;
    const raw = n.type === 'checkbox' ? n.checked
              : NUMERIC.has(n.name) ? Number(n.value.trim())
              : n.value;
    plant(out, n.name, raw);
  });
  return out;
}

/* What this tunnel actually has.
 *
 * The edit dialog showed every setting to every tunnel: a plain TCP tunnel was
 * offered mux sizes, a KCP window, an MSS the server refuses on a datagram
 * transport, and the whole spoof drawer. None of them do anything there, and a
 * form that offers a setting nothing reads is a form that teaches people their
 * changes do not stick.
 *
 * The rules are the engine's own, taken from internal/manage — isMux, isKCP,
 * isDatagram and supportsProxyProtocol — rather than guessed here, because the
 * server refuses several of these by name and a form that collects an answer
 * only to have it rejected is worse than one that never asked.
 */
const isMux = t => ['tcpmux', 'wsmux', 'wssmux', 'kcp', 'xdi', 'spoof', 'pck'].includes(t);
const needsTLS = t => ['wss', 'wssmux'].includes(t);
const isKCP = t => ['kcp', 'xdi', 'spoof', 'pck'].includes(t);
const isDatagram = t => ['udp', 'kcp', 'xdi', 'quic', 'pck'].includes(t);
const takesProxyProtocol = t =>
  ['tcp', 'tcpmux', 'kcp', 'wsmux', 'wssmux', 'stealth', 'quic', 'pck'].includes(t);

function shapeForTransport(root, settings, tunnel) {
  const t = String(settings.transport || tunnel?.transport || '').toLowerCase();
  const direct = !!tunnel && tunnel.direction === 'direct';
  const server = (settings.role || tunnel?.role || '') === 'server';

  /* Marked as well as hidden. read() has to skip these — a field that does not
     apply is one the server has no key for, and sending it fails the whole save
     — but it cannot use `hidden` for that, because a collapsed drawer is hidden
     too and its values must still be sent. */
  const show = (sel, on) => root.querySelectorAll(sel).forEach(n => {
    const row = n.closest('.f, .tg, .two > div, .row') || n;
    row.hidden = !on;
    n.dataset.off = on ? '' : '1';
    if (!on) n.setAttribute('data-off', '1'); else n.removeAttribute('data-off');
  });
  const byName = n => `[name="${n}"], [data-name="${n}"]`;

  show('[name^="tune.mux"]', isMux(t));
  show('[name^="tune.kcp"]', isKCP(t));
  // The server refuses an MSS on a datagram transport by name.
  show(byName('tune.mss'), !isDatagram(t));
  // Off by default and plain TCP only; anywhere else it is written and ignored.
  show(byName('tune.zeroCopy'), t === 'tcp');
  show(byName('proxyProtocol'), server && takesProxyProtocol(t));
  /* Only a TLS server presents a certificate. Elsewhere the two fields would be
     settings the transport has no use for. */
  show('.certrow', server && needsTLS(t));

  // Which half of the tunnel this is decides the rest.
  show(byName('serverAddr'), !server);
  show(byName('ports'), server);
  show(byName('tune.acceptUDP'), server);
  show(byName('tune.channelSize'), server);
  show(byName('tune.connectionPool'), !server);
  show(byName('tune.aggressivePool'), !server);

  // The forged-source settings belong to the spoof carrier, which is a direct
  // tunnel's choice — never a reverse transport's.
  root.querySelectorAll('[name^="spoof."], [data-name^="spoof."]').forEach(n => {
    const row = n.closest('.f, .tg, .dr2b, .two > div') || n;
    const on = direct && (settings.carrier || '') === 'spoof';
    row.hidden = !on;
    if (!on) n.setAttribute('data-off', '1'); else n.removeAttribute('data-off');
  });

  /* A drawer with nothing left in it is a drawer that opens onto nothing. */
  root.querySelectorAll('.dr2b, .drw').forEach(d => {
    const rows = [...d.querySelectorAll('[name], [data-name]')];
    if (rows.length) d.hidden = !rows.some(r => !(r.closest('.f, .tg, .two > div') || r).hidden);
  });
}

/* The choices each menu offers. Transport and preset come from the server, so
   the form can never offer one the engine does not have. */
async function choicesFor(name, opts, family) {
  switch (name) {
    case '__family':
      return (opts.families || []).map((f, i) => ({ value: String(i), label: f.label }));
    case 'transport': {
      const fam = (opts.families || [])[Number(family) || 0];
      return (fam?.entries || []).map(e => ({ value: e.value, label: e.label }));
    }
    case 'preset':
      return (opts.presets || []).map(p => ({ value: p.value, label: p.label }));
    case 'tune.logLevel':
      return ['debug', 'info', 'warn', 'error'].map(v => ({ value: v, label: v }));
    default:
      return [];
  }
}

/* Points the family menu at whichever family offers `transport`, and refreshes
   the transport menu underneath it so its list matches. */
function selectFamilyFor(root, opts, transport) {
  const sel = root.querySelector('.sel[data-name="__family"]');
  if (!sel || !transport) return;
  const fams = (opts && opts.families) || [];
  const i = fams.findIndex(f => (f.entries || []).some(e => e.value === transport));
  if (i < 0) return;
  sel.dataset.value = String(i);
  if (sel.childNodes[0]) sel.childNodes[0].textContent = fams[i].label;
}

async function wireControls(root, given) {
  let opts = given || { families: [], presets: [] };
  if (!given) {
    try { opts = await api.tunnelOptions(); } catch (e) { /* the menus stay empty */ }
  }
  wireControls.opts = opts;

  root.querySelectorAll('.sww[data-name]').forEach(sw => {
    const name = sw.dataset.name;
    if (!name || sw.dataset.wired) return;
    sw.dataset.wired = '1';
    const input = el('input', { type: 'checkbox', name, hidden: true });
    input.checked = sw.classList.contains('on');
    sw.after(input);
    sw.setAttribute('role', 'switch');
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

  const famOf = () => root.querySelector('.sel[data-name="__family"]')?.dataset.value || '0';

  for (const sel of root.querySelectorAll('.sel[data-name]')) {
    const name = sel.dataset.name;
    if (!name || sel.dataset.wired) continue;
    sel.dataset.wired = '1';
    /* __family picks which transports are offered and is not a field of its
       own — the server has no such key, and posting one would fail the whole
       save on an unknown key. */
    const posts = !name.startsWith('__');
    const input = posts ? el('input', { type: 'text', name, hidden: true }) : null;
    if (input) sel.after(input);
    sel.setAttribute('role', 'combobox');
    sel.tabIndex = 0;

    const menu = el('div', { class: 'selmenu', hidden: true });
    sel.append(menu);

    const paint = async () => {
      const list = await choicesFor(name, opts, famOf());
      menu.innerHTML = '';
      list.forEach(c => {
        const b = el('button', { type: 'button', class: 'selopt', text: c.label });
        b.addEventListener('click', ev => {
          ev.stopPropagation();
          sel.dataset.value = c.value;
          sel.childNodes[0].textContent = c.label;
          if (input) input.value = c.value;
          menu.hidden = true;
          sel.classList.remove('open');
          // Changing the family changes what transports there are.
          if (name === '__family') {
            const tr = root.querySelector('.sel[data-name="transport"]');
            tr?.dispatchEvent(new CustomEvent('repaint'));
            /* And the transport follows. It used to stay where it was, so
               choosing TCP on a UDP tunnel showed "udp" under the TCP family,
               and the save that followed sent the old transport — the edit
               that took several tries to make. A transport the new family
               lists is kept; otherwise its first one is chosen. */
            const fam = (opts.families || [])[Number(c.value)];
            const trIn = root.querySelector('input[name="transport"]');
            if (tr && trIn && fam?.entries?.length && !fam.entries.some(x => x.value === trIn.value)) {
              const first = fam.entries[0];
              trIn.value = first.value;
              tr.dataset.value = first.value;
              if (tr.childNodes[0]) tr.childNodes[0].textContent = first.label;
              trIn.dispatchEvent(new Event('change', { bubbles: true }));
            }
          }
        });
        menu.append(b);
      });
    };
    sel.addEventListener('repaint', paint);
    await paint();

    sel.addEventListener('click', ev => {
      if (ev.target.closest('.selopt')) return;
      const open = menu.hidden;
      root.querySelectorAll('.selmenu').forEach(m => { m.hidden = true; });
      root.querySelectorAll('.sel.open').forEach(x => x.classList.remove('open'));
      menu.hidden = !open;
      sel.classList.toggle('open', open);
    });
    sel.addEventListener('keydown', ev => {
      if (ev.key === 'Escape') { menu.hidden = true; sel.classList.remove('open'); }
    });
  }

  root.addEventListener('click', ev => {
    if (ev.target.closest('.sel')) return;
    root.querySelectorAll('.selmenu').forEach(m => { m.hidden = true; });
    root.querySelectorAll('.sel.open').forEach(x => x.classList.remove('open'));
  });
}

/* fill() writes the hidden fields; the drawings in front of them follow. */
function syncControls(root) {
  root.querySelectorAll('.sww[data-name]').forEach(sw => {
    const input = root.querySelector(`input[name="${sw.dataset.name}"]`);
    if (!input) return;
    sw.classList.toggle('on', input.checked);
    sw.setAttribute('aria-checked', String(input.checked));
  });
  root.querySelectorAll('.sel[data-name]').forEach(sel => {
    const input = root.querySelector(`input[name="${sel.dataset.name}"]`);
    if (!input) return;
    /* A tunnel whose numbers no longer match any profile has no preset, and
       the menu used to keep the label it was drawn with — "Balanced" — so a
       custom tunnel read as Balance whatever it really ran — reported as "it
       always goes back to Balance". Say what it is. */
    if (!input.value && sel.dataset.name === 'preset') {
      sel.dataset.value = '';
      sel.childNodes[0].textContent = 'Custom — tuned by hand';
      return;
    }
    if (!input.value) return;
    sel.dataset.value = input.value;
    const opt = [...sel.querySelectorAll('.selopt')]
      .find(o => o.textContent.trim().toLowerCase() === input.value.trim().toLowerCase());
    sel.childNodes[0].textContent = opt ? opt.textContent : input.value;
  });
}

/* A direct tunnel has a form of its own.
 *
 * It used to be handed the reverse form: transport families, mux sizes and KCP
 * windows a layer-3 tunnel does not have, filled from settings with none of
 * those keys, and saved with every field at the top of the request — where the
 * direct edit reads nothing, since its fields travel under "direct". Opening
 * Edit on a direct tunnel showed the wrong dialog and could not save it. The
 * reverse panes are replaced with the settings a direct tunnel really has, and
 * the save goes where the server reads it. */
const sw = (name, title, text) => `<div class="tg"><div class="tx"><b>${title}</b><span>${text}</span></div>` +
  `<div class="sww" data-name="${name}"><i></i></div></div>`;
const field = (name, label, hint = '') => `<div class="f"><label>${label}</label>` +
  `<input name="${name}" type="text">${hint ? `<div class="hint">${hint}</div>` : ''}</div>`;

function directMarkup(set) {
  const ports = !!set.holdsPorts;
  if (set.layer === 4) return `<div class="pane" data-tab="Direct">` + (ports ?
    field("addr", "Kharej address", "Host and port.") + field("ports", "Forwarded ports") +
    sw("acceptUdp", "Accept UDP") +
    `<div class="f"><label>Performance preset</label><div class="sel" data-name="preset">Turbo<span class="sp"></span></div></div>` +
    `<div class="two">${field("sessions", "Sessions")}${field("mss", "TCP MSS", "0 is auto.")}</div>` +
    `<div class="two">${field("maxConnections", "Max connections")}${field("bandwidthMbps", "Bandwidth (Mbit/s)")}</div>` :
    `<div class="hint">Forwarded ports and tuning are set on the Iran server.</div>`) + `</div>`;
  return `<div class="pane" data-tab="Direct">` +
    (ports ? field('ports', 'Forwarded ports', 'Comma separated. Blank leaves a plain TUN tunnel.') +
      sw('acceptUdp', 'Accept UDP', 'Carry UDP over the forwarded ports.') : '') +
    `<div class="f"><label>Performance preset</label><div class="sel" data-name="preset">Turbo` +
    `<span class="sp"></span><svg class="x" viewBox="0 0 24 24"><path d="M6 9l6 6 6-6"/></svg></div></div>` +
    `<div class="two">${field('mtu', 'MTU')}` +
    (set.carrier === 'udp' ? field('paths', 'Sockets', '1 is one socket; 2–8 spread it.') : '<div></div>') + `</div>` +
    sw('autoMtu', 'Auto MTU', 'Measure the path and correct the MTU once up.') +
    sw('fec', 'Error correction (FEC)', 'Spare packets repair loss. Both ends must match.') +
    (set.carrier === 'spoof' ? sw('stealth', 'Stealth', 'Padding and header cosmetics. Both ends must match.') : '') +
    (ports ? `<div class="two">${field('maxConnections', 'Max connections', '0 is no limit.')}` +
      `${field('bandwidthMbps', 'Bandwidth (Mbit/s)', '0 is no limit.')}</div>` : '') +
    `</div>`;
}

const DIRECT_NUMBERS = new Set(['mtu', 'paths', 'maxConnections', 'bandwidthMbps', 'sessions', 'mss']);

function readDirect(root) {
  const out = {};
  root.querySelectorAll('.panes input[name]').forEach(n => {
    const v = n.type === 'checkbox' ? n.checked
            : DIRECT_NUMBERS.has(n.name) ? Number(n.value.trim())
            : n.value;
    if (typeof v === 'number' && Number.isNaN(v)) return;
    out[n.name] = v;
  });
  return out;
}

export async function editView(ctx) {
  const name = ctx.params.name;
  /* A deep link arrives before the first poll, so the tunnel may not be in the
     store yet; without this the header falls back to the preview's own text. */
  let t = store.tunnel(name);
  if (!t) { await store.loadTunnels(); t = store.tunnel(name); }

  openScreen('edit', {
    pick: '.dlg',
    bind: async (root, close) => {
      const fl = root.querySelector('.dh .fl');
      if (fl) fl.textContent = flag(t?.peerCountry) || '·';
      const ttl = root.querySelector('.dh .ttl input, .dh .ttl > div');
      if (ttl) { if ('value' in ttl) ttl.value = name; else ttl.textContent = name; }
      const sub = root.querySelector('.dh .ttl small');
      if (sub && t) sub.textContent = kindLabel(t) + ' · ' + (t.addr || '');

      let settings = {};
      let direct = false;
      try {
        settings = await api.tunnelSettings(name);
        if (settings.kind === 'direct') { settings = settings.direct || {}; direct = true; }
      } catch (e) { oops(e); }

      if (direct) {
        /* Only History stays in the tab row; the reverse tabs have no pane. */
        root.querySelectorAll('.tabs > button:not(.hist)').forEach(b => { b.hidden = true; });
        const panes = root.querySelector('.panes');
        if (panes) panes.innerHTML = directMarkup(settings);
        let dopts = { presets: [] };
        try { dopts = await api.directOptions(); } catch (e) { /* the menu stays empty */ }
        await wireControls(root, { families: [], presets: dopts.presets || [] });
        fill(root, settings);
        syncControls(root);
      } else {
        /* The switches and the menus were drawings.
         *
         * Ten <div class="sww"> and four <div class="sel">, none of them a
         * control and none of them named — so every boolean on this form and
         * every choice on it went nowhere, and an edit that turned Accept UDP on
         * saved a tunnel with Accept UDP unchanged. They carry data-name in the
         * markup now, and each gets the hidden field it stands for, so fill() and
         * read() see them like any other input.
         */
        await wireControls(root);
        shapeForTransport(root, settings, t);
        /* The tunnel port field shows the address as well when the control port
           is pinned to one.
         *
         * tunnelPort stays the bare port in the API because adopt pairs the two
         * ends of a tunnel by comparing it, and the far end cannot know which of
         * this machine's addresses the port was bound to. The form is the one
         * place the two belong back together: an operator who typed
         * 85.10.11.51:443 has to see that on the next visit, or accepting the
         * field unchanged would quietly widen the tunnel to every interface. */
        fill(root, { ...settings, tunnelPort: bindValue(settings) });
        /* The family is not a field, so fill() never touches it and the dialog
           opened showing whichever one the preview happened to be drawn with —
           WebSocket, on every tunnel, including a TCP one. It is derived: the
           family is whichever one lists this tunnel's transport. */
        selectFamilyFor(root, wireControls.opts, settings.transport);
        syncControls(root);
      }

      const directOriginal = direct ? readDirect(root) : {};

      /* Tabs and drawers are the preview's own handlers, rebound in screen.js. */

      /* The History button has been in this dialog since it was drawn; it goes
         to the screen that lists what this tunnel used to be. */
      root.querySelector('.hist')?.addEventListener('click', () => {
        close();
        go(`/t/${encodeURIComponent(name)}/undo`);
      });

      /* Random asks the server for a free port rather than rolling one here. */
      [...root.querySelectorAll('button')]
        .filter(b => /^random$/i.test(b.textContent.trim()))
        .forEach(b => b.addEventListener('click', async () => {
          try {
            const r = await api.tunnelSuggest();
            const field = b.closest('div')?.querySelector('input') ||
                          root.querySelector('[name="tunnelPort"]');
            if (field && r.port) field.value = r.port;
          } catch (e) { oops(e); }
        }));

      /* The preview drew Save greyed out, meaning "nothing has changed yet",
         and nothing ever un-greyed it: the button was disabled from the moment
         the dialog opened, so no edit made on this screen could be saved at
         all. It now means what it was drawn to mean. */
      const save = root.querySelector('.save, [data-save], .primary');
      if (save) {
        const enable = () => { save.disabled = false; };
        ['input', 'change'].forEach(ev => root.addEventListener(ev, enable));
        root.addEventListener('click', ev => {
          if (ev.target.closest('.sel, .sww, .selopt, .sw3, .sww2')) enable();
        });
      }
      save?.addEventListener('click', async () => {
        const changedDirect = direct ? Object.fromEntries(Object.entries(readDirect(root))
          .filter(([key, value]) => value !== directOriginal[key])) : {};
        const payload = direct ? { name, direct: changedDirect } : { name, ...read(root) };
        save.disabled = true;
        try {
          const r = await api.tunnelEdit(payload);
          if (r.status === 'partial') {
            /* This end changed and the other did not. Half the values on this
               form are ones both ends must agree on, so that is not a detail to
               mention in passing — the dialog stays open, because saving again
               is what fixes it. */
            toast(r.peerHint || r.peerError || 'The other end was not updated.', true);
            store.refresh();
            save.disabled = false;
            return;
          }
          store.refresh();
          /* This end is the Iran end of a reverse tunnel and something both
             ends must agree on changed: the kharej is now on the old settings
             and will not reach it. Its setup link describes the tunnel as it
             is now, and applying it on the kharej brings that end into step —
             so the dialog stays open on the one line that does it. */
          const both = !direct && tunnelIsIranEnd(t, settings) && ['transport', 'tunnelPort', 'preset']
            .some(k => payload[k] !== undefined && String(payload[k]) !== String(k === 'tunnelPort' ? bindValue(settings) : settings[k] ?? ''));
          if (both && !r.node) {
            try {
              handOver(root, await api.tunnelLink(name), close);
              toast('Saved here — one line on the kharej and both ends match again.');
              return;
            } catch (e) { /* the link could not be made; the toast below still holds */ }
          }
          toast(r.node
            ? `Saved on this server and on ${r.node}.`
            : 'Saved — the tunnel restarted on the new settings.');
          close();
        } catch (e) { oops(e); }
        save.disabled = false;
      });

      ctx.setTeardown(close);
    },
  }).catch(oops);
}

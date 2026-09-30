/* Manage — three of the menu's Manage tools, on one page.
 *
 *   Auto Refresh     every tunnel restarted on a schedule, counted down to
 *   Built-in Proxy   a SOCKS5 or HTTP backend, wired to a tunnel's port
 *   File Locations   everything Backpack keeps, where, and how big
 *
 * Nothing opens a dialog: each tool is a panel with its state, its controls
 * and its result in the same place, because each is something you set and
 * then look at.
 *
 * CLI: main menu → 3 Manage.
 */

import { $, esc, copyText, flashCopied } from '../lib/dom.js';
import { bytes, ago } from '../lib/format.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { toast, oops } from '../ui/toast.js';
import { confirmBox } from '../ui/confirm.js';

const I = {
  clock: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3 2"/></svg>',
  proxy: '<svg viewBox="0 0 24 24"><circle cx="6" cy="12" r="2.3"/><circle cx="18" cy="6" r="2.3"/><circle cx="18" cy="18" r="2.3"/><path d="M8.2 11l7.6-4M8.2 13l7.6 4"/></svg>',
  folder: '<svg viewBox="0 0 24 24"><path d="M3 7a2 2 0 012-2h4l2 2h8a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2z"/></svg>',
  file: '<svg viewBox="0 0 24 24"><path d="M14 3H7a2 2 0 00-2 2v14a2 2 0 002 2h10a2 2 0 002-2V8z"/><path d="M14 3v5h5"/></svg>',
  copy: '<svg viewBox="0 0 24 24"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 012-2h10"/></svg>',
  test: '<svg viewBox="0 0 24 24"><path d="M5 12l4 4 10-10"/></svg>',
  search: '<svg viewBox="0 0 24 24"><circle cx="11" cy="11" r="7"/><path d="M20 20l-3.5-3.5"/></svg>',
  down: '<svg viewBox="0 0 24 24"><path d="M6 9l6 6 6-6"/></svg>',
  dice: '<svg viewBox="0 0 24 24"><rect x="4" y="4" width="16" height="16" rx="4"/><circle cx="9" cy="9" r="1.2"/><circle cx="15" cy="15" r="1.2"/><circle cx="15" cy="9" r="1.2"/><circle cx="9" cy="15" r="1.2"/></svg>',
  term: '<svg viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="16" rx="3"/><path d="M7 9l3 3-3 3"/><path d="M12.5 15H17"/></svg>',
  link: '<svg viewBox="0 0 24 24"><path d="M10 14a4 4 0 005.7 0l3-3a4 4 0 00-5.7-5.7l-1 1"/><path d="M14 10a4 4 0 00-5.7 0l-3 3a4 4 0 005.7 5.7l1-1"/></svg>',
};

/* ---- Auto Refresh -------------------------------------------------------- */
const CHOICES = [0, 6, 12, 24, 48];

/* The day on the server's clock as a dial: 24 hour marks, the ones a restart
   lands on standing out, and the countdown to the next one in the middle. */
function dayRing(h) {
  const marks = h > 0 && h < 24 ? [...Array(24).keys()].filter(x => x % h === 0) : (h >= 24 ? [0] : []);
  const ticks = [...Array(24).keys()].map(x => {
    const a = (x / 24) * 2 * Math.PI - Math.PI / 2;
    const on = marks.includes(x);
    const r1 = on ? 76 : x % 6 ? 86 : 82, r2 = 92;
    const lbl = x % 6 === 0
      ? `<text class="hr" x="${(100 + 66 * Math.cos(a)).toFixed(1)}" y="${(103 + 66 * Math.sin(a)).toFixed(1)}" text-anchor="middle">${String(x).padStart(2, '0')}</text>` : '';
    return `<line class="${on ? 'on' : ''}" x1="${(100 + r1 * Math.cos(a)).toFixed(1)}" y1="${(100 + r1 * Math.sin(a)).toFixed(1)}" x2="${(100 + r2 * Math.cos(a)).toFixed(1)}" y2="${(100 + r2 * Math.sin(a)).toFixed(1)}"/>${lbl}`;
  }).join('');
  return `<svg class="ar-dial" viewBox="0 0 200 200" aria-hidden="true">${ticks}
    <circle class="ar-trk" cx="100" cy="100" r="98"/>
    <circle class="ar-arc" cx="100" cy="100" r="98" pathLength="1000"/>
    <g class="ar-hand"><circle cx="100" cy="2" r="4"/></g></svg>`;
}

function refreshCard(r) {
  const h = r.effective || 0;
  const custom = h && !CHOICES.includes(h);
  const every = h <= 0 ? '' : h < 24 ? `every ${h} h` : h === 24 ? 'daily' : `every ${h / 24} days`;
  return `<div class="tl-card mg-tool mg-sq" id="mgRefresh">
    <div class="mg-h"><span class="ic">${I.clock}</span><div><b>Auto Refresh</b><small>Restart every tunnel on a schedule</small></div>
      <span class="tl-pill ${h ? 'ok' : ''}"><i></i>${h ? every : 'off'}</span></div>
    <div class="ar-hero ${h ? 'on' : 'off'}">
      ${dayRing(h)}
      <div class="ar-read">
        ${h ? `<small>next restart in</small><b id="arLeft">--:--:--</b>
          <span class="ar-at">${esc(serverTime(r))}</span>`
          : `<b class="off">Off</b><small>tunnels restart only when you do it</small>`}
      </div>
    </div>
    <div class="ar-pick">
      <div class="tl-chips" id="arPick">${CHOICES.map(c =>
        `<button type="button" data-h="${c}" class="${c === h && !custom ? 'on' : ''}">${c ? c + ' h' : 'Off'}</button>`).join('')}
        <label class="ar-custom ${custom ? 'on' : ''}"><input id="arHours" type="number" min="1" max="720" value="${custom ? h : ''}" placeholder="custom"><span>h</span></label></div>
      <p class="tl-hint dim">${h ? `At ${esc(slots(h))} on the server's clock.` : 'Pick an interval and cron takes it from there.'} Above a day, cron counts whole days — 36 becomes 24.</p>
    </div>
  </div>`;
}

/* The restart times as the server keeps them. */
function slots(h) {
  if (h < 24) {
    const list = [...Array(24).keys()].filter(x => x % h === 0).map(x => String(x).padStart(2, '0') + ':00');
    return list.length > 6 ? `${list.slice(0, 4).join(', ')} … every ${h} h` : list.join(', ');
  }
  return h === 24 ? '00:00 every day' : `00:00 every ${h / 24} days`;
}

/* When it fires, as the server's own clock reads — not the browser's, which
   is often in another zone entirely. */
function serverTime(r) {
  if (!r.next) return '';
  const d = new Date((r.next + (r.offset || 0)) * 1000);
  const day = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'][d.getUTCDay()];
  const hm = `${String(d.getUTCHours()).padStart(2, '0')}:${String(d.getUTCMinutes()).padStart(2, '0')}`;
  return `${day} ${hm}${r.zone ? ' ' + r.zone : ''}`;
}

const hms = s => {
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), x = s % 60;
  return (h ? `${h}:` : '') + `${String(m).padStart(h ? 2 : 1, '0')}:${String(x).padStart(2, '0')}`;
};

/* ---- Built-in Proxy ------------------------------------------------------ */
let proxyType = null;
let proxyWhere = null;   // 'tunnel' | 'here'
let wired = null;        // what "Wire it up" did, for the result box

/* The tunnels a proxy can hang off: the Iran end of a reverse tunnel, which is
   the one whose forwarded ports this server owns. */
const wireable = () => (store.get().tunnels || []).filter(t => t.role === 'server' && t.direction !== 'direct');

function proxyCard(p, test) {
  const type = proxyType || p.type || 'socks5';
  const tunnels = wireable();
  const where = proxyWhere || (tunnels.length ? 'tunnel' : 'here');
  const on = p.enabled && p.running;
  const state = on ? ['ok', `${p.type === 'http' ? 'HTTP' : 'SOCKS5'} on :${p.port}`]
    : p.enabled ? ['er', 'enabled, not running'] : ['', 'off here'];
  const seg = `<div class="tl-seg px-where" id="pxWhere">
      <button type="button" data-w="tunnel" class="${where === 'tunnel' ? 'on' : ''}">Through a tunnel<small>proxy on the kharej</small></button>
      <button type="button" data-w="here" class="${where === 'here' ? 'on' : ''}">On this server<small>127.0.0.1 here</small></button></div>`;
  const typeSeg = `<div class="tl-seg" id="pxType">
      <button type="button" data-t="socks5" class="${type === 'socks5' ? 'on' : ''}">SOCKS5<small>most apps, UDP too</small></button>
      <button type="button" data-t="http" class="${type === 'http' ? 'on' : ''}">HTTP<small>browsers</small></button></div>`;
  const auth = `<label class="tl-f"><span>Username <em>optional</em></span><input id="pxUser" type="text" value="${esc(where === 'here' ? p.username || '' : '')}" autocomplete="off" spellcheck="false"></label>
      <label class="tl-f"><span>Password</span><input id="pxPass" type="password" placeholder="${where === 'here' && p.hasPassword ? 'unchanged' : 'with a username'}" autocomplete="new-password"></label>`;

  let body;
  if (where === 'tunnel') {
    body = !tunnels.length
      ? `<div class="px-empty">${I.link}<b>No reverse tunnel on this server yet</b>
          <small>Add one from Tunnels, then come back and hang a proxy off it.</small></div>`
      : `${typeSeg}
        <div class="tl-grid px-grid">
          <label class="tl-f wide"><span>Tunnel</span>
            <select id="pxTun" class="px-sel">${tunnels.map(t =>
              `<option value="${esc(t.name)}">${esc(t.name)} · ${esc((t.carrier || t.transport || '').toUpperCase())} · ports ${esc(t.ports || '—')}</option>`).join('')}</select></label>
          <label class="tl-f"><span>Port people connect to <em>here</em></span>
            <span class="px-rand"><input id="pxPub" type="number" min="1" max="65535" placeholder="e.g. 8443">
              <button type="button" class="tl-btn ghost sm" id="pxRand" title="A free port">${I.dice}</button></span></label>
          <label class="tl-f"><span>Proxy port <em>on the kharej</em></span><input id="pxPort" type="number" min="1" max="65535" placeholder="e.g. 1085"></label>
          ${auth}
        </div>
        ${wired ? `<div class="px-done">
            <div class="px-done-h"><i></i><b>${esc(wired.tunnel)} now forwards :${wired.pub} to the proxy</b></div>
            <span class="tl-meta">Last step — run this on the kharej of that tunnel, as root:</span>
            <div class="ct-cmd"><span class="pr">${I.term}</span><code id="pxCmd">${esc(wired.cmd)}</code>
              <button class="tl-btn" data-copy="#pxCmd">${I.copy}<span>Copy</span></button></div>
          </div>` : ''}
        <span class="sp"></span>
        <div class="tl-actions"><span class="tl-meta">Adds <b>port=127.0.0.1:proxy</b> to the tunnel's ports</span><span class="sp"></span>
          <button class="tl-btn solid" id="pxWire">${I.link}Wire it up</button></div>`;
  } else {
    body = `${typeSeg}
      <div class="tl-grid px-grid">
        <label class="tl-f wide"><span>Port</span><input id="pxPort" type="number" min="1" max="65535" value="${p.port || ''}" placeholder="e.g. 1085"></label>
        ${auth}
      </div>
      ${p.port ? `<div class="px-map"><span>Forward a tunnel port to it</span>
        <code id="pxMap">443=127.0.0.1:${p.port}</code><button class="tl-btn ghost sm" data-copy="#pxMap">${I.copy}<span>Copy</span></button></div>` : ''}
      ${!p.username && on ? '<div class="tl-note wr">No username — anyone who reaches the forwarded port can use it.</div>' : ''}
      ${test ? `<div class="px-test ${test.ok ? 'ok' : 'er'}"><i></i><span><b>${test.ok ? 'Working' : 'Not working'}</b> — ${esc(test.detail)}${test.ms ? ` · ${test.ms} ms` : ''}</span></div>` : ''}
      <span class="sp"></span>
      <div class="tl-actions">
        ${p.enabled ? `<button class="tl-btn ghost" id="pxOff">Disable</button>` : ''}
        <span class="sp"></span>
        ${p.enabled ? `<button class="tl-btn" id="pxTest">${I.test}Test it</button>` : ''}
        <button class="tl-btn solid" id="pxSave">${p.enabled ? 'Save' : 'Enable'}</button></div>`;
  }
  return `<div class="tl-card mg-tool mg-sq" id="mgProxy">
    <div class="mg-h"><span class="ic">${I.proxy}</span><div><b>Built-in Proxy</b><small>SOCKS5 or HTTP behind a tunnel — nothing else to install</small></div>
      <span class="tl-pill ${state[0]}"><i></i>${esc(state[1])}</span></div>
    ${seg}${body}
  </div>`;
}

/* ---- File Locations ------------------------------------------------------ */
let fileQuery = '';
let filesOpen = false;
const GROUPS = ['Backpack', 'Services', 'Tunnels', 'Backups'];

function fileRows(files) {
  const q = fileQuery.trim().toLowerCase();
  const shown = files.filter(f => !q || (f.label + ' ' + f.path).toLowerCase().includes(q));
  const missing = files.filter(f => !f.exists).length;
  const groups = GROUPS.map(g => {
    const list = shown.filter(f => f.group === g);
    if (!list.length) return '';
    return `<div class="fl-g"><div class="ct-gh"><b>${g}</b><small>${list.length}</small><span class="ln"></span></div>
      ${list.map((f, i) => `<div class="fl-row ${f.exists ? '' : 'gone'}">
        <span class="ic">${f.dir ? I.folder : I.file}</span>
        <span class="nm"><b>${esc(f.label)}</b><code id="fp-${g}-${i}">${esc(f.path)}</code></span>
        <span class="meta">${!f.exists ? '<em>not present</em>'
          : `${f.dir ? `${f.items} item${f.items === 1 ? '' : 's'}` : esc(bytes(f.size || 0))}${f.modified ? `<small>${esc(ago(f.modified))}</small>` : ''}`}</span>
        <button class="tl-btn ghost sm" data-copy="#fp-${g}-${i}" title="Copy the path">${I.copy}</button>
      </div>`).join('')}</div>`;
  }).join('');
  return (groups || '<p class="tl-hint">Nothing matches that.</p>')
    + (missing && !q ? `<p class="tl-hint dim">${missing} not present — features not in use on this server.</p>` : '');
}

function filesCard(files) {
  const missing = files.filter(f => !f.exists).length;
  return `<div class="tl-card mg-tool mg-files ${filesOpen ? 'open' : ''}" id="mgFiles">
    <div class="fl-bar">
      <button type="button" class="fl-tog" id="flTog" aria-expanded="${filesOpen}">
        <span class="ic">${I.folder}</span>
        <span class="tx"><b>File Locations</b><small>${files.length - missing} of ${files.length} present</small></span>
      </button>
      <label class="fl-search">${I.search}<input id="flQ" type="search" placeholder="Search configs, services, backups…" value="${esc(fileQuery)}" autocomplete="off"></label>
      <button type="button" class="fl-chev" id="flChev" aria-label="Show or hide">${I.down}</button>
    </div>
    <div class="fl-body"><div class="fl-in" id="flList">${fileRows(files)}</div></div>
  </div>`;
}

export function manageView(ctx) {
  const view = $('#view');
  view.innerHTML = `<div class="tl-page mg">
    <div class="sech2 tl-head"><h2>Manage</h2><span class="sp"></span></div>
    <p class="tl-sub">The machine-level tools from the menu's Manage screen, each where you can see what it is doing.</p>
    <div class="mg-grid" id="mgGrid"><div class="tl-card tl-loading">Reading this server…</div></div>
  </div>`;
  const grid = $('#mgGrid', view);
  let st = null, test = null, skew = 0, tick = null, reading = false;

  const paint = () => {
    if (!st) return;
    grid.innerHTML = refreshCard(st.refresh || {}) + proxyCard(st.proxy || {}, test) + filesCard(st.files || []);
    paintClock();
  };
  const repaintProxy = () => {
    const c = $('#mgProxy', view);
    if (c) c.outerHTML = proxyCard(st.proxy || {}, test);
  };

  /* The countdown runs on the server's clock: skew is how far the browser's
     is from it, measured when the state was read. */
  const paintClock = () => {
    const r = st?.refresh;
    const lbl = $('#arLeft', view);
    const hero = $('#mgRefresh .ar-hero', view);
    if (!r?.next || !lbl || !hero) return;
    const nowS = Math.floor(Date.now() / 1000) + skew;
    const left = r.next - nowS;
    if (left <= 0) {
      // It is firing now; read the next one once cron has moved on.
      lbl.textContent = 'now';
      if (!reading) { reading = true; setTimeout(() => load().finally(() => { reading = false; }), 5000); }
      return;
    }
    lbl.textContent = hms(left);
    const span = (r.effective || 24) * 3600;
    hero.style.setProperty('--p', Math.max(0, Math.min(1, 1 - left / span)).toFixed(4));
    // The hand sits at the server's hour of day.
    const d = new Date((nowS + (r.offset || 0)) * 1000);
    const frac = (d.getUTCHours() * 3600 + d.getUTCMinutes() * 60 + d.getUTCSeconds()) / 86400;
    hero.style.setProperty('--hand', (frac * 360).toFixed(2) + 'deg');
  };

  const load = async () => {
    try {
      st = await api.manageState();
      adopt(st.refresh);
      paint();
    } catch (e) { oops(e); }
  };
  const adopt = r => {
    if (r?.now) skew = r.now - Math.floor(Date.now() / 1000);
  };

  const setHours = async h => {
    try {
      st.refresh = await api.setAutoRefresh(h);
      adopt(st.refresh);
      const c = $('#mgRefresh', view);
      if (c) c.outerHTML = refreshCard(st.refresh);
      paintClock();
      toast(st.refresh.effective ? `Every tunnel restarts every ${st.refresh.effective} hours.` : 'Auto Refresh is off.');
    } catch (e) { oops(e); }
  };

  view.addEventListener('click', async ev => {
    const b = ev.target.closest('button');
    if (!b || !view.contains(b)) return;
    if (b.dataset.copy) {
      const ok = await copyText($(b.dataset.copy, view)?.textContent.trim() || '');
      flashCopied(b.querySelector('span') || b, ok);
      if (!ok) toast('The browser would not copy it — select it and copy by hand.', true);
      return;
    }
    if (b.dataset.h !== undefined) { setHours(Number(b.dataset.h)); return; }
    if (b.dataset.w) { proxyWhere = b.dataset.w; wired = null; repaintProxy(); return; }
    if (b.dataset.t) {
      proxyType = b.dataset.t;
      b.parentElement.querySelectorAll('button').forEach(x => x.classList.toggle('on', x === b));
      return;
    }
    if (b.id === 'flTog' || b.id === 'flChev') {
      filesOpen = !filesOpen;
      const c = $('#mgFiles', view);
      c.classList.toggle('open', filesOpen);
      $('#flTog', view)?.setAttribute('aria-expanded', String(filesOpen));
      return;
    }
    if (b.id === 'pxRand') {
      try { const r = await api.tunnelSuggest(); $('#pxPub', view).value = r.port; } catch (e) { oops(e); }
      return;
    }
    if (b.id === 'pxWire') { wire(b); return; }
    if (b.id === 'pxSave') {
      const port = Number($('#pxPort', view)?.value);
      if (!port) { toast('Choose the port the proxy listens on.', true); return; }
      b.disabled = true;
      try {
        st.proxy = await api.proxyEnable({
          type: proxyType || st.proxy.type || 'socks5', port,
          username: $('#pxUser', view).value.trim(), password: $('#pxPass', view).value,
        });
        proxyType = null;
        test = await api.proxyTest().catch(() => null);
        repaintProxy();
        toast('The proxy is on.');
      } catch (e) { oops(e); b.disabled = false; }
      return;
    }
    if (b.id === 'pxOff') {
      if (!await confirmBox({ title: 'Disable the built-in proxy?', body: 'Any tunnel port forwarded to it stops working until it is enabled again.', go: 'Disable' })) return;
      try { st.proxy = await api.proxyDisable(); test = null; repaintProxy(); toast('The proxy is off.'); } catch (e) { oops(e); }
      return;
    }
    if (b.id === 'pxTest') {
      b.disabled = true;
      try { test = await api.proxyTest(); repaintProxy(); } catch (e) { oops(e); b.disabled = false; }
    }
  });

  /* Through a tunnel: this server gets the forwarded port — the tunnel's own
     edit, the same one the Edit dialog makes — and the kharej gets one line
     that turns the proxy on at the other end. */
  async function wire(b) {
    const name = $('#pxTun', view)?.value;
    const t = store.tunnel(name);
    const pub = Number($('#pxPub', view)?.value), port = Number($('#pxPort', view)?.value);
    const user = $('#pxUser', view)?.value.trim() || '', pass = $('#pxPass', view)?.value || '';
    if (!t) { toast('Choose the tunnel.', true); return; }
    if (!pub || !port) { toast('Give both ports: the one people connect to here, and the proxy’s on the kharej.', true); return; }
    if (!!user !== !!pass) { toast('Give both a username and a password, or neither.', true); return; }
    const have = String(t.ports || '').split(',').map(x => x.trim()).filter(Boolean);
    if (have.some(x => x.split('=')[0].split(':').pop() === String(pub))) {
      toast(`${name} already forwards port ${pub}.`, true); return;
    }
    b.disabled = true;
    try {
      await api.tunnelEdit({ name, ports: [...have, `${pub}=127.0.0.1:${port}`].join(',') });
      const type = proxyType || 'socks5';
      const q = s => `'${String(s).replace(/'/g, `'\\''`)}'`;
      wired = { tunnel: name, pub, cmd: `sudo backpack proxy enable ${type} ${port}${user ? ` --user ${q(user)} --pass ${q(pass)}` : ''}` };
      store.loadTunnels().catch(() => {});
      repaintProxy();
      toast(`${name} forwards :${pub} to the proxy — finish it on the kharej.`);
    } catch (e) { oops(e); b.disabled = false; }
  }

  view.addEventListener('change', ev => {
    if (ev.target.id === 'arHours') {
      const h = Number(ev.target.value);
      if (h > 0) setHours(h);
    }
  });
  view.addEventListener('input', ev => {
    if (ev.target.id !== 'flQ') return;
    fileQuery = ev.target.value;
    // Searching is looking: the list opens for it.
    if (fileQuery && !filesOpen) {
      filesOpen = true;
      $('#mgFiles', view)?.classList.add('open');
    }
    const list = $('#flList', view);
    if (list) list.innerHTML = fileRows(st.files || []);
  });

  tick = setInterval(paintClock, 1000);
  load();
  ctx.setTeardown(() => clearInterval(tick));
}

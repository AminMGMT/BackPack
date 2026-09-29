/* Manage — three of the menu's Manage tools, on one page.
 *
 *   Auto Refresh     every tunnel restarted on a schedule
 *   Built-in Proxy   a SOCKS5 or HTTP backend on 127.0.0.1, tested in place
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
};

/* ---- Auto Refresh -------------------------------------------------------- */
const CHOICES = [0, 6, 12, 24, 48];

/* The restarts in a day on the server's clock, as a 24-hour dial. */
function dayRing(h) {
  const marks = h > 0 && h < 24 ? [...Array(24).keys()].filter(x => x % h === 0) : (h >= 24 ? [0] : []);
  const ticks = [...Array(24).keys()].map(x => {
    const a = (x / 24) * 2 * Math.PI - Math.PI / 2;
    const on = marks.includes(x);
    const r1 = on ? 29 : 34, r2 = 38;
    return `<line class="${on ? 'on' : ''}" x1="${(44 + r1 * Math.cos(a)).toFixed(1)}" y1="${(44 + r1 * Math.sin(a)).toFixed(1)}" x2="${(44 + r2 * Math.cos(a)).toFixed(1)}" y2="${(44 + r2 * Math.sin(a)).toFixed(1)}"/>`;
  }).join('');
  const centre = h <= 0 ? 'Off' : h < 24 ? `${24 / h | 0}×` : h === 24 ? '1×' : `${h / 24}d`;
  return `<svg class="ar-ring" viewBox="0 0 88 88" aria-hidden="true">${ticks}
    <text x="44" y="47" text-anchor="middle">${centre}</text></svg>`;
}

function refreshCard(r) {
  const h = r.effective || 0;
  const custom = h && !CHOICES.includes(h);
  const when = h <= 0 ? 'Tunnels are never restarted on a schedule.'
    : h < 24 ? `Every tunnel restarts at ${[...Array(24).keys()].filter(x => x % h === 0).map(x => String(x).padStart(2, '0') + ':00').join(', ')} — server clock.`
    : h === 24 ? 'Every tunnel restarts once a day at 00:00 — server clock.'
    : `Every tunnel restarts at 00:00 every ${h / 24} days — server clock.`;
  return `<div class="tl-card mg-tool" id="mgRefresh">
    <div class="mg-h"><span class="ic">${I.clock}</span><div><b>Auto Refresh</b><small>Restart every tunnel on a schedule</small></div>
      <span class="tl-pill ${h ? 'ok' : ''}"><i></i>${h ? `every ${h} h` : 'off'}</span></div>
    <div class="ar-body">${dayRing(h)}
      <div class="ar-side">
        <div class="tl-chips" id="arPick">${CHOICES.map(c =>
          `<button type="button" data-h="${c}" class="${c === h && !custom ? 'on' : ''}">${c ? c + ' h' : 'Off'}</button>`).join('')}
          <label class="ar-custom ${custom ? 'on' : ''}"><input id="arHours" type="number" min="1" max="720" value="${custom ? h : ''}" placeholder="custom"><span>h</span></label></div>
        <p class="tl-hint">${esc(when)}</p>
        <p class="tl-hint dim">Above a day, cron counts whole days — 36 becomes 24.</p>
      </div></div>
  </div>`;
}

/* ---- Built-in Proxy ------------------------------------------------------ */
let proxyType = null;

function proxyCard(p, test) {
  const type = proxyType || p.type || 'socks5';
  const on = p.enabled && p.running;
  const state = on ? ['ok', `${p.type === 'http' ? 'HTTP' : 'SOCKS5'} on :${p.port}`]
    : p.enabled ? ['er', 'enabled, not running'] : ['', 'off'];
  return `<div class="tl-card mg-tool" id="mgProxy">
    <div class="mg-h"><span class="ic">${I.proxy}</span><div><b>Built-in Proxy</b><small>A backend on 127.0.0.1 — forward a tunnel port to it</small></div>
      <span class="tl-pill ${state[0]}"><i></i>${esc(state[1])}</span></div>
    <div class="tl-seg" id="pxType">
      <button type="button" data-t="socks5" class="${type === 'socks5' ? 'on' : ''}">SOCKS5<small>most apps, UDP too</small></button>
      <button type="button" data-t="http" class="${type === 'http' ? 'on' : ''}">HTTP<small>browsers</small></button></div>
    <div class="tl-grid">
      <label class="tl-f"><span>Port</span><input id="pxPort" type="number" min="1" max="65535" value="${p.port || ''}" placeholder="e.g. 1085"></label>
      <label class="tl-f"><span>Username <em>optional</em></span><input id="pxUser" type="text" value="${esc(p.username || '')}" autocomplete="off" spellcheck="false"></label>
      <label class="tl-f"><span>Password</span><input id="pxPass" type="password" placeholder="${p.hasPassword ? 'unchanged' : 'with a username'}" autocomplete="new-password"></label>
    </div>
    ${p.port ? `<div class="px-map"><span>Forward a tunnel port to it</span>
      <code id="pxMap">443=127.0.0.1:${p.port}</code><button class="tl-btn ghost sm" data-copy="#pxMap">${I.copy}<span>Copy</span></button></div>` : ''}
    ${!p.username && on ? '<div class="tl-note wr">No username — anyone who reaches the forwarded port can use it.</div>' : ''}
    ${test ? `<div class="px-test ${test.ok ? 'ok' : 'er'}"><i></i><span><b>${test.ok ? 'Working' : 'Not working'}</b> — ${esc(test.detail)}${test.ms ? ` · ${test.ms} ms` : ''}</span></div>` : ''}
    <div class="tl-actions">
      ${p.enabled ? `<button class="tl-btn ghost" id="pxOff">Disable</button>` : ''}
      <span class="sp"></span>
      ${p.enabled ? `<button class="tl-btn" id="pxTest">${I.test}Test it</button>` : ''}
      <button class="tl-btn solid" id="pxSave">${p.enabled ? 'Save' : 'Enable'}</button></div>
  </div>`;
}

/* ---- File Locations ------------------------------------------------------ */
let fileQuery = '';
const GROUPS = ['Backpack', 'Services', 'Tunnels', 'Backups'];

function filesCard(files) {
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
  return `<div class="tl-card mg-tool mg-files" id="mgFiles">
    <div class="mg-h"><span class="ic">${I.folder}</span><div><b>File Locations</b><small>Configs, services and backups — where each one lives</small></div>
      <span class="tl-pill ${missing ? '' : 'ok'}"><i></i>${files.length - missing} of ${files.length} present</span></div>
    <label class="fl-search">${I.search}<input id="flQ" type="search" placeholder="Filter by name or path" value="${esc(fileQuery)}" autocomplete="off"></label>
    ${groups || '<p class="tl-hint">Nothing matches that.</p>'}
    ${missing ? `<p class="tl-hint dim">${missing} not present — features not in use on this server.</p>` : ''}
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
  let st = null, test = null;

  const paint = () => {
    if (!st) return;
    grid.innerHTML = refreshCard(st.refresh || {}) + proxyCard(st.proxy || {}, test) + filesCard(st.files || []);
  };
  const load = async () => {
    try { st = await api.manageState(); paint(); } catch (e) { oops(e); }
  };

  const setHours = async h => {
    try {
      st.refresh = await api.setAutoRefresh(h);
      paint();
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
    if (b.dataset.t) {
      proxyType = b.dataset.t;
      b.parentElement.querySelectorAll('button').forEach(x => x.classList.toggle('on', x === b));
      return;
    }
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
        paint();
        toast('The proxy is on.');
      } catch (e) { oops(e); b.disabled = false; }
      return;
    }
    if (b.id === 'pxOff') {
      if (!await confirmBox({ title: 'Disable the built-in proxy?', body: 'Any tunnel port forwarded to it stops working until it is enabled again.', go: 'Disable' })) return;
      try { st.proxy = await api.proxyDisable(); test = null; paint(); toast('The proxy is off.'); } catch (e) { oops(e); }
      return;
    }
    if (b.id === 'pxTest') {
      b.disabled = true;
      try { test = await api.proxyTest(); paint(); } catch (e) { oops(e); b.disabled = false; }
    }
  });

  view.addEventListener('change', ev => {
    if (ev.target.id === 'arHours') {
      const h = Number(ev.target.value);
      if (h > 0) setHours(h);
    }
  });
  view.addEventListener('input', ev => {
    if (ev.target.id !== 'flQ') return;
    fileQuery = ev.target.value;
    const card = $('#mgFiles', view);
    const pos = ev.target.selectionStart;
    card.outerHTML = filesCard(st.files || []);
    const q = $('#flQ', view);
    q.focus();
    q.setSelectionRange(pos, pos);
  });

  load();
  ctx.setTeardown(() => {});
}

/* The dashboard: the server strip, then the fleet.
 *
 * The markup is the approved preview's, class for class — .RZ tiles and .c7
 * cards — so the design is the one that was signed off rather than a redraw of
 * it. What changed is only where the numbers come from.
 *
 * CLI: Manage → Status, and Manage → Manage Tunnels.
 */

import { $, delegate, esc } from '../lib/dom.js';
import { bytes, speed, kindLabel, flag } from '../lib/format.js';
import * as store from '../store.js';
import * as api from '../api.js';
import { toast, oops } from '../ui/toast.js';
import { confirmBox } from '../ui/confirm.js';
import { go } from '../router.js';

/* ---- the rate chart behind a card ---------------------------------------- */
/* The preview drew a fixed path; this is the same shape from real samples. */
function sparkPaths(rates, w = 340, h = 126) {
  if (!rates || rates.length < 2) return null;
  const pts = rates.slice(-15);
  const max = Math.max(...pts.map(p => p.in + p.out), 1);
  const step = w / (pts.length - 1);
  const xy = pts.map((p, i) => [
    +(i * step).toFixed(1),
    +(h - 8 - ((p.in + p.out) / max) * (h - 16)).toFixed(1),
  ]);
  const line = xy.map(([x, y], i) => `${i ? 'L' : 'M'}${x} ${y}`).join(' ');
  return { line, area: `${line} L${w} ${h} L0 ${h} Z` };
}

function field(t, idx) {
  const p = sparkPaths(t.rates);
  if (!p) return '';
  const id = 'L' + idx;
  return `<div class="field"><svg class="k" viewBox="0 0 340 126" preserveAspectRatio="none" style="height:126px">
<defs><linearGradient id="${id}" x1="0" y1="0" x2="0" y2="1">
<stop offset="0%" stop-color="var(--spark)" stop-opacity=".2"/>
<stop offset="100%" stop-color="var(--spark)" stop-opacity="0"/></linearGradient></defs>
<path d="${p.area}" fill="url(#${id})"/>
<path class="sparkpath" d="${p.line}" fill="none" stroke="var(--spark)" stroke-width="2"
 stroke-linecap="round" stroke-linejoin="round" vector-effect="non-scaling-stroke"/></svg></div>`;
}

const RELAY_SVG = `<svg class="x" viewBox="0 0 24 24"><rect x="4" y="8" width="16" height="11" rx="3"/>
<path d="M12 4v4"/><circle cx="12" cy="3" r="1.4"/><path d="M9 13.5h.01M15 13.5h.01"/></svg>`;

const BTN = {
  edit:  `<svg class="iA" viewBox="0 0 24 24"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 013 3L7 19l-4 1 1-4z"/></svg>`,
  logs:  `<svg class="iA" viewBox="0 0 24 24"><path d="M8 3H6a2 2 0 00-2 2v14a2 2 0 002 2h9a2 2 0 002-2v-2"/><path d="M16 3h4v4"/><path d="M9 8h5M9 12h6M9 16h4"/></svg>`,
  chart: `<svg class="iA" viewBox="0 0 24 24"><path d="M3 20h18"/><path d="M6 16l4-5 3.5 3L20 6"/></svg>`,
  more:  `<svg class="iA" viewBox="0 0 24 24"><circle cx="5" cy="12" r="1.4"/><circle cx="12" cy="12" r="1.4"/><circle cx="19" cy="12" r="1.4"/></svg>`,
};

function card(t, idx) {
  const off = t.state !== 'running';
  const dotCls = off ? 'dot off' : (t.kcpLossPercent > 2 ? 'dot wr' : 'dot');
  const label = off ? (t.state === 'failed' ? 'Failed' : 'Stopped') : 'Online';
  const hasPing = t.ping !== undefined && t.ping >= 0;
  const dir = t.direction === 'direct' ? 'Direct' : 'Reverse';
  const carrier = (t.carrier || t.transport || '').toUpperCase();
  const ports = (t.ports || []).map(p => ':' + String(p).split('=')[0]).join(', ');
  const [ip, port] = String(t.addr || '').split(':');

  return `<div class="c7 ${off ? 'off' : ''}" data-name="${esc(t.name)}">
${field(t, idx)}
<div class="inner">
  <div class="hd">
    <div class="fl">${flag(t.peerCountry) || flag(t.country) || '·'}</div>
    <div class="id">
      <b>${esc(t.name)}</b>
      <small>${esc([t.peerLocation, t.peerISP].filter(Boolean).join(' · ') || '—')}</small>
    </div>
    <span class="stt"><span class="${dotCls}"></span>${label}</span>
  </div>

  <div class="kind">
    <span>${dir}</span><span>${esc(carrier)}</span>
    ${ports ? `<span class="q">${esc(ports)}</span>` : ''}
  </div>

  <div class="mid">
  <div class="ping">
    <span class="n mono${hasPing ? '' : ' na'}">${hasPing ? t.ping : '—'}</span>
    <span class="u">${hasPing ? 'ms' : 'no ping'}</span>
    <span class="tail">${t.botRelay ? `<span class="relay">${RELAY_SVG} Bot Relay</span>` : ''}</span>
  </div>

  <div class="totals">
    <span class="t"><i>↓</i><b>${bytes(t.bytesIn || 0)}</b><em>total</em></span>
    <span class="t"><i>↑</i><b>${bytes(t.bytesOut || 0)}</b><em>total</em></span>
  </div>

  </div>

  <div class="addr">
    <span class="ip">${esc(ip || '—')}</span><span class="sep"></span>
    <span class="pt">port ${esc(port || t.tunnelPort || '—')}</span>
  </div>

  <div class="spacer"></div>

  <div class="foot">
    <span class="upt">${t.uptime ? `up <b>${esc(t.uptime)}</b>` : ''}</span>
    <span class="sp2"></span>
    <button class="btn" data-act="edit"   title="Edit">${BTN.edit}</button>
    <button class="btn" data-act="logs"   title="Logs">${BTN.logs}</button>
    <button class="btn" data-act="detail" title="Metrics">${BTN.chart}</button>
    <button class="btn" data-act="more"   title="Start, stop, restart, delete">${BTN.more}</button>
  </div>

  <div class="card-more">
    <button data-do="start"><span>▶</span>Start</button>
    <button data-do="stop"><span>■</span>Stop</button>
    <button data-do="restart"><span>↻</span>Restart</button>
    <div class="sep2"></div>
    <button class="danger" data-do="delete"><span>✕</span>Delete</button>
  </div>
</div></div>`;
}

/* ---- the server strip ---------------------------------------------------- */
const COLS = 22;
function tile(key, label, percent, sub, history) {
  /* The last column is the reading now; anything at 85% or above is marked,
     which is the one thing the strip has to say without colour doing the work. */
  const bars = Array.from({ length: COLS }, (_, i) => {
    const v = history ? history[i] : percent;
    const cls = [i === COLS - 1 ? 'now' : '', v >= 85 ? 'over' : ''].filter(Boolean).join(' ');
    return `<i class="${cls}" style="--h:${Math.max(4, Math.min(100, v)).toFixed(0)}%"></i>`;
  }).join('');
  return `<div class="t4" data-m="${key}">
    <div class="head6">
      <span class="v"><span class="num">${percent.toFixed(0)}</span><em>%</em></span>
      <span class="k">${label}</span><span class="trend"></span>
    </div>
    <div class="cols">${bars}</div>
    <div class="s">${esc(sub)}</div>
  </div>`;
}

/* One short history per meter so the columns mean something between polls. */
const hist = { cpu: [], mem: [], dsk: [], swp: [], dn: [], up: [] };
function push(key, v) {
  const a = hist[key];
  a.push(v);
  while (a.length > COLS) a.shift();
  return Array.from({ length: COLS }, (_, i) => a[i] ?? v);
}

/* Under the meters: what is moving right now, and two figures that only grow.
 *
 * The rates get the same column history the CPU and memory tiles have, because
 * a number that changes every four seconds is hard to read on its own — the
 * shape next to it says whether 40 Mb/s is the usual or a spike. The columns
 * are scaled to the highest value in the window rather than to the link speed,
 * which nothing here knows, so a quiet tunnel still draws a readable line.
 */
function rateTile(key, label, bytesPerSec) {
  const hist = push(key, Number(bytesPerSec) || 0);
  const peak = Math.max(...hist, 1);
  const bars = hist.map((v, i) => {
    const h = Math.max(4, (v / peak) * 100);
    const cls = i === COLS - 1 ? 'now' : '';
    return `<i class="${cls}" style="--h:${h.toFixed(0)}%"></i>`;
  }).join('');
  const [n, unit = ''] = speed(bytesPerSec || 0).split(' ');
  return `<div class="t4" data-m="${key}">
    <div class="head6">
      <span class="v"><span class="num">${esc(n)}</span><em>${esc(unit)}</em></span>
      <span class="k">${esc(label)}</span><span class="trend"></span>
    </div>
    <div class="cols">${bars}</div>
    <div class="s">peak ${esc(speed(peak))} in the last ${COLS} readings</div>
  </div>`;
}

/* The line under the two rates.
 *
 * Total traffic gets a strip beside it because a running total means little on
 * its own — 1.2 TB is only worth reading next to how it was spent. The bars are
 * summed from every tunnel's own per-day figures.
 *
 * The window is a month. The store keeps 720 hourly buckets, and the endpoint
 * takes ?days= up to 30 — it still answers a week to anyone who does not ask,
 * so the older dashboard draws exactly what it always did.
 */
let dayTotals = null;          /* [{label, bytes}] summed across tunnels */

async function loadDayTotals(names) {
  const sum = new Map();
  const runs = await Promise.allSettled(names.map(n => api.history(n, 30)));
  for (const r of runs) {
    if (r.status !== 'fulfilled' || !r.value?.days) continue;
    for (const d of r.value.days) {
      sum.set(d.label, (sum.get(d.label) || 0) + (d.in || 0) + (d.out || 0));
    }
  }
  dayTotals = [...sum].map(([label, bytes]) => ({ label, bytes }));
  return dayTotals;
}

function daysStrip() {
  if (!dayTotals || !dayTotals.length) return '';
  const peak = Math.max(...dayTotals.map(d => d.bytes), 1);
  const w = 7, gap = 3;
  const bars = dayTotals.map((d, i) => {
    const h = Math.max(2, (d.bytes / peak) * 20);
    return `<rect x="${i * (w + gap)}" y="${(22 - h).toFixed(1)}" width="${w}"
      height="${h.toFixed(1)}" rx="1.5"><title>${esc(d.label)} · ${esc(bytes(d.bytes))}</title></rect>`;
  }).join('');
  const width = dayTotals.length * (w + gap) - gap;
  return `<svg class="dstrip" viewBox="0 0 ${width} 22" width="${width}" height="22"
    aria-label="traffic per day, last ${dayTotals.length} days">${bars}</svg>`;
}

function factLine(s) {
  return `<div class="factline">
    <span>Version <b>${esc(s.version || '—')}</b></span>
    <span class="dot"></span>
    <span class="tt">Total traffic <b>${bytes(s.totalTraffic || 0)}</b>${daysStrip()}</span>
    ${dayTotals?.length ? `<span class="cap">last ${dayTotals.length} days</span>` : ''}
  </div>`;
}

function totalsRow(s) {
  return `<div class="RZ rates">
    ${rateTile('dn', 'Download', s.downSpeed)}
    ${rateTile('up', 'Upload', s.upSpeed)}
  </div>
  ${factLine(s)}`;
}

function serverStrip(s) {
  const gb = n => (n / 1024 ** 3).toFixed(1);
  return `<div class="sectitle"><h3>Server</h3><div class="ln"></div></div>
  <div class="RZ">
    ${tile('cpu', 'Processor', s.cpuPercent, `${s.cpuCores} cores · load ${s.load || '—'}`, push('cpu', s.cpuPercent))}
    ${tile('mem', 'Memory', s.memPercent, `${gb(s.memUsed)} / ${gb(s.memTotal)} GB`, push('mem', s.memPercent))}
    ${tile('dsk', 'Disk', s.diskPercent, `${gb(s.diskUsed)} / ${gb(s.diskTotal)} GB`, push('dsk', s.diskPercent))}
    ${s.swapTotal > 0
      ? tile('swp', 'Swap', s.swapPercent, `${gb(s.swapUsed)} / ${gb(s.swapTotal)} GB`, push('swp', s.swapPercent))
      : tile('swp', 'Swap', 0, 'not configured', push('swp', 0))}
  </div>
  ${totalsRow(s)}`;
}

const EMPTY = `<div class="emptybox">
  <b>No tunnels yet</b>
  <span>Add one with the button above, or from the CLI menu (<code>sudo backpack</code>).</span>
</div>`;

const ACTION_DONE = {
  start: 'Tunnel started.', stop: 'Tunnel stopped.',
  restart: 'Tunnel restarted.', delete: 'Tunnel deleted.',
};

export function dashboard(ctx) {
  const view = $('#view');

  const paint = state => {
    const s = state.stats;
    view.innerHTML = (s ? serverStrip(s) : '') + `
      <div class="sech2">
        <h2>Tunnels</h2>
        <span class="cnt">${state.tunnels.length}</span>
        <span class="sp"></span>
        <button class="sb" data-act="restartall">Restart all</button>
        <button class="sb primary" data-act="add">Add tunnel</button>
      </div>
      <div class="grid3">${state.tunnels.length
        ? state.tunnels.map(card).join('')
        : EMPTY}</div>`;
  };

  const unsub = store.subscribe(paint);

  /* One pass over the tunnels for their per-day figures; the strip is the sum.
     It waits for the first tunnel list rather than asking once and giving up —
     on a cold load the view is built before that list has arrived. */
  let asked = false;
  const wantDays = store.subscribe(async state => {
    if (asked || dayTotals || !state.tunnels.length) return;
    asked = true;
    try {
      await loadDayTotals(state.tunnels.map(t => t.name));
      paint(store.get());
    } catch (e) { /* the line simply has no strip */ }
  });

  delegate(view, 'click', '[data-act]', async (ev, btn) => {
    const name = btn.closest('.c7')?.dataset.name;
    switch (btn.dataset.act) {
      case 'more': {
        ev.stopPropagation();
        const sheet = btn.closest('.c7').querySelector('.card-more');
        const was = sheet.classList.contains('on');
        view.querySelectorAll('.card-more.on').forEach(s => s.classList.remove('on'));
        sheet.classList.toggle('on', !was);
        break;
      }
      case 'edit':   go(`/t/${encodeURIComponent(name)}/edit`); break;
      case 'logs':   go(`/t/${encodeURIComponent(name)}/logs`); break;
      case 'detail': go(`/t/${encodeURIComponent(name)}/metrics`); break;
      case 'add':    go('/add'); break;
      case 'restartall': {
        if (!await confirmBox({
          title: 'Restart every tunnel?',
          body: 'Each one drops and rebuilds its connection. Traffic stops for a moment on all of them.',
          go: 'Restart all',
        })) return;
        toast('Restarting every tunnel…');
        try {
          const r = await api.restartAll();
          toast(r.failed ? `Restarted ${r.restarted}, failed ${r.failed}`
                         : `Restarted ${r.restarted || 0}.`, !!r.failed);
        } catch (e) { oops(e); }
        store.refresh();
        break;
      }
    }
  });

  delegate(view, 'click', '[data-do]', async (ev, btn) => {
    const cardEl = btn.closest('.c7');
    const name = cardEl.dataset.name;
    const action = btn.dataset.do;
    cardEl.querySelector('.card-more').classList.remove('on');

    if (action === 'delete') {
      const ok = await confirmBox({
        title: `Delete <q>${esc(name)}</q>?`,
        body: 'Its config and its service are removed. This cannot be undone.',
        lines: [
          { text: `/etc/backpack/${name}.json` },
          { text: `backpack-${name}.service` },
        ],
        go: 'Delete', danger: true,
      });
      if (!ok) return;
    }
    cardEl.style.opacity = '.5';
    try { await api.tunnelAction(name, action); toast(ACTION_DONE[action] || 'Done.'); }
    catch (e) { oops(e); }
    cardEl.style.opacity = '';
    store.refresh();
  });

  const closeSheets = () => view.querySelectorAll('.card-more.on').forEach(s => s.classList.remove('on'));
  document.addEventListener('click', closeSheets);

  ctx.setTeardown(() => { unsub(); wantDays(); document.removeEventListener('click', closeSheets); });
}

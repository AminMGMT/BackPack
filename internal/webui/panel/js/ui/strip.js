/* The server strip: this machine, on every page.
 *
 * It used to be the top of the dashboard, which meant that watching the box
 * while doing anything else — adding a server, reading a tunnel's settings —
 * required leaving the thing you were doing. It is chrome now, rendered once
 * into #strip and repainted from the store, so the meters are wherever you are.
 *
 * The markup is the approved preview's, class for class: .RZ tiles, .factline.
 */

import { $, esc } from '../lib/dom.js';
import { speed } from '../lib/format.js';
import * as store from '../store.js';

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

function totalsRow(s) {
  return `<div class="RZ rates">
    ${rateTile('dn', 'Download', s.downSpeed)}
    ${rateTile('up', 'Upload', s.upSpeed)}
  </div>`;
}

/* The sizes arrive already formatted — "5.1 GiB", not a count of bytes — so
 * they are printed, not converted. Dividing them by 1024³ is what put "NaN /
 * NaN GB" under Memory and Disk, and comparing one to 0 is what reported a
 * configured swap as "not configured". */
const size = (used, total) => {
  const u = String(used ?? '').trim(), t = String(total ?? '').trim();
  if (!u || !t) return '—';
  const uu = u.split(' ')[1], tu = t.split(' ')[1];
  return uu && uu === tu ? `${u.split(' ')[0]} / ${t}` : `${u} / ${t}`;
};
const hasSize = v => {
  const n = parseFloat(String(v ?? ''));
  return Number.isFinite(n) && n > 0;
};

function serverStrip(s) {
  return `<div class="sectitle"><h3>Server</h3><div class="ln"></div></div>
  <div class="RZ">
    ${tile('cpu', 'Processor', s.cpuPercent, `${s.cpuCores} cores · load ${s.load || '—'}`, push('cpu', s.cpuPercent))}
    ${tile('mem', 'Memory', s.memPercent, size(s.memUsed, s.memTotal), push('mem', s.memPercent))}
    ${tile('dsk', 'Disk', s.diskPercent, size(s.diskUsed, s.diskTotal), push('dsk', s.diskPercent))}
    ${hasSize(s.swapTotal)
      ? tile('swp', 'Swap', s.swapPercent, size(s.swapUsed, s.swapTotal), push('swp', s.swapPercent))
      : tile('swp', 'Swap', 0, 'not configured', push('swp', 0))}
  </div>
  ${totalsRow(s)}`;
}


/* Mounts the strip and keeps it painted. Called once, at boot. */
export function mountStrip() {
  const host = $('#strip');
  if (!host) return;

  const paint = state => {
    if (!state.stats) return;
    host.innerHTML = serverStrip(state.stats);
  };
  store.subscribe(paint);

}

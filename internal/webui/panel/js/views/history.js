/* The long view of one tunnel: speed over the last day, per-day totals for the
 * week, the two uptime figures, and the configuration changes inside the
 * window. The chart code is the preview's, fed from /api/history.
 *
 * CLI: Manage → Tunnel Metrics (the long half).
 */

import { $$, esc } from '../lib/dom.js';
import { bytes, kindLabel, flag } from '../lib/format.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { oops } from '../ui/toast.js';

const W = 720, H = 186, PAD = 8;

function draw(root, data) {
  const g = root.querySelector('#g7');
  const plot = root.querySelector('#plot7');
  const read = root.querySelector('#read7');
  const xax = root.querySelector('#xax7');
  if (!g) return;

  const series = data.series || [];
  if (series.length < 2) return;

  /* The handler drops a sample whose counter went backwards, so the series has
     real holes. They stay holes: joining across one would draw a straight line
     over the exact moment something restarted. */
  const t0 = series[0].t, t1 = series[series.length - 1].t;
  const span = Math.max(1, t1 - t0);
  const X = t => ((t - t0) / span) * W;
  const peak = Math.max(...series.map(p => p.in), 1);
  const Y = v => H - PAD - (v / peak) * (H - PAD * 2);

  const out = [];
  for (let k = 0; k <= 3; k++) {
    const v = (peak * k) / 3, y = Y(v);
    out.push(`<line class="grid7" x1="0" y1="${y.toFixed(1)}" x2="${W}" y2="${y.toFixed(1)}"/>`);
    out.push(`<text class="gap7" x="2" y="${(y - 4).toFixed(1)}">${(v * 8 / 1e6).toFixed(0)} Mb/s</text>`);
  }
  for (const at of data.changes || []) {
    if (at < t0 || at > t1) continue;
    const x = X(at).toFixed(1);
    out.push(`<line class="cg7" x1="${x}" y1="0" x2="${x}" y2="${H}"/>`);
    out.push(`<rect class="cgh" x="${(X(at) - 2).toFixed(1)}" y="-1" width="4" height="4" rx="1"/>`);
  }

  /* One path per unbroken stretch: a gap wider than two sample intervals is a
     gap, not a line. */
  const STEP = 300, runs = [];
  let run = [series[0]];
  for (let i = 1; i < series.length; i++) {
    if (series[i].t - series[i - 1].t > STEP * 2) { if (run.length > 1) runs.push(run); run = []; }
    run.push(series[i]);
  }
  if (run.length > 1) runs.push(run);

  for (const r of runs) {
    const dIn = r.map((p, i) => `${i ? 'L' : 'M'}${X(p.t).toFixed(1)} ${Y(p.in).toFixed(1)}`).join(' ');
    const dOut = r.map((p, i) => `${i ? 'L' : 'M'}${X(p.t).toFixed(1)} ${Y(p.out).toFixed(1)}`).join(' ');
    out.push(`<path class="area7" d="${dIn} L${X(r[r.length - 1].t).toFixed(1)} ${H - PAD} L${X(r[0].t).toFixed(1)} ${H - PAD} Z"/>`);
    out.push(`<path class="in7" d="${dIn}"/>`);
    out.push(`<path class="out7" d="${dOut}"/>`);
  }
  for (let i = 1; i < runs.length; i++) {
    const mid = (X(runs[i - 1][runs[i - 1].length - 1].t) + X(runs[i][0].t)) / 2;
    out.push(`<text class="gap7" text-anchor="middle" x="${mid.toFixed(1)}" y="${H - 4}">no sample</text>`);
  }
  out.push(`<line class="cross7" id="cx7" x1="0" y1="0" x2="0" y2="${H}"/>`);
  out.push(`<circle class="dot7" id="d1" r="3"/><circle class="dot7 o" id="d2" r="2.5"/>`);
  g.innerHTML = out.join('');

  const labels = [];
  for (let k = 0; k <= 4; k++) {
    const d = new Date((t0 + (span * k) / 4) * 1000);
    labels.push(k === 4 ? 'now'
      : `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`);
  }
  xax.innerHTML = labels.map(t => `<span>${t}</span>`).join('');

  const cx = g.querySelector('#cx7'), d1 = g.querySelector('#d1'), d2 = g.querySelector('#d2');
  plot.addEventListener('pointermove', ev => {
    const r = plot.getBoundingClientRect();
    const at = t0 + ((ev.clientX - r.left) / r.width) * span;
    let best = series[0];
    for (const p of series) if (Math.abs(p.t - at) < Math.abs(best.t - at)) best = p;
    plot.classList.add('live'); read.classList.add('on');
    const d = new Date(best.t * 1000);
    root.querySelector('#rt7').textContent =
      `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
    root.querySelector('#rd7').textContent = (best.in * 8 / 1e6).toFixed(1) + ' Mb/s';
    root.querySelector('#ru7').textContent = (best.out * 8 / 1e6).toFixed(1) + ' Mb/s';
    read.classList.toggle('cg',
      (data.changes || []).some(c => Math.abs(c - best.t) <= STEP));
    const x = X(best.t);
    cx.setAttribute('x1', x); cx.setAttribute('x2', x);
    d1.setAttribute('cx', x); d1.setAttribute('cy', Y(best.in));
    d2.setAttribute('cx', x); d2.setAttribute('cy', Y(best.out));
  });
  plot.addEventListener('pointerleave', () => {
    plot.classList.remove('live'); read.classList.remove('on', 'cg');
  });
}

function days(root, list) {
  const box = root.querySelector('#bars7');
  if (!box || !list?.length) return;
  const top = Math.max(...list.map(d => d.in + d.out), 1);
  box.innerHTML = list.map(d => {
    const hi = (d.in / top * 100).toFixed(1), lo = (d.out / top * 100).toFixed(1);
    return `<div class="day7"><div class="tip7">${esc(d.label)} · ↓${bytes(d.in)} · ↑${bytes(d.out)}</div>
      <div class="stk"><div class="bi" style="height:${hi}%"></div>
      <div class="bo" style="height:${lo}%"></div></div>
      <div class="lab7">${esc(d.label)}</div></div>`;
  }).join('');
}

export async function historyView(ctx) {
  const name = ctx.params.name;
  /* A deep link arrives before the first poll, so the tunnel may not be in the
     store yet; without this the header falls back to the preview's own text. */
  let t = store.tunnel(name);
  if (!t) { await store.loadTunnels(); t = store.tunnel(name); }

  openScreen('history', {
    pick: '.dlg',
    bind: async (root, close) => {
      const fl = root.querySelector('.dh .fl');
      if (fl) fl.textContent = flag(t?.peerCountry) || '·';
      const ttl = root.querySelector('.dh .ttl > div');
      if (ttl) ttl.textContent = 'History · ' + name;
      const sub = root.querySelector('.dh .ttl small');
      if (sub && t) sub.textContent = kindLabel(t) + ' · sampled every five minutes';

      let data;
      try { data = await api.history(name); } catch (e) { return oops(e); }

      /* Fewer than two samples and there is no rate to draw at all. */
      if (data.collecting) {
        /* The tiles sit above the panes, so replacing the panes alone left a
           tunnel with no samples yet showing the preview's uptime and its
           1.24 TB week — invented numbers, directly above the line explaining
           that there are no numbers yet. */
        $$('.st7', root).forEach(tile => {
          const v = tile.querySelector('.v7');
          if (v) v.innerHTML = '—';
          const bar = tile.querySelector('.bar7 i');
          if (bar) bar.style.width = '0%';
          const sub = tile.querySelector('small');
          if (sub) sub.textContent = 'nothing measured yet';
        });
        const body = root.querySelector('.panes') || root;
        body.innerHTML = `<div class="coll7"><div class="rings"><i></i><i></i><i></i></div>
          <b>Still collecting</b>
          <span>The monitor samples every tunnel every five minutes. Two samples are needed
          before there is a rate to draw, so the first chart appears about ten minutes after a
          tunnel is added.</span></div>`;
        return;
      }

      const pct = v => (v < 0 ? '—' : v.toFixed(1) + '<em>%</em>');
      const u24 = root.querySelector('#u24'), u7 = root.querySelector('#u7');
      if (u24) u24.innerHTML = pct(data.uptime24h);
      if (u7) u7.innerHTML = pct(data.uptime7d);
      $$('.st7 .bar7 i', root).forEach((bar, i) => {
        const v = [data.uptime24h, data.uptime7d][i];
        if (v >= 0) bar.style.width = v + '%';
      });

      /* The third tile was drawn with an example total; it is the sum of the
         very days the chart below is about. */
      const week = (data.days || []).reduce(
        (a, d) => ({ in: a.in + (d.in || 0), out: a.out + (d.out || 0) }), { in: 0, out: 0 });
      /* Written whether or not there is anything to write. Guarding on "some
         traffic was seen" left a tunnel that has carried nothing showing the
         preview's 1.24 TB — an invented figure, on the screen whose whole job
         is to report real ones. */
      const tiles = $$('.st7', root);
      if (tiles[2]) {
        const total = bytes(week.in + week.out).split(' ');
        tiles[2].querySelector('.v7').innerHTML = `${total[0]}<em>${total[1] || ''}</em>`;
        const sub = tiles[2].querySelector('small');
        if (sub) sub.textContent = `${bytes(week.in)} down · ${bytes(week.out)} up`;
      }

      draw(root, data);
      days(root, data.days);
      ctx.setTeardown(close);
    },
  }).catch(oops);
}

/* Everything known about one tunnel: traffic, the peer, limits, the bot relay,
 * the certificate, failover, the connection pool and — on a KCP link — what the
 * error correction is actually repairing.
 *
 * The preview drew every section at once. A real tunnel has only some of them,
 * so a section with nothing behind it is removed rather than left showing the
 * example values it was drawn with.
 *
 * CLI: Manage → Tunnel Metrics.
 */

import { $$, esc } from '../lib/dom.js';
import { isUp } from '../lib/tstate.js';
import { bytes, speed, ago, kindLabel, flag } from '../lib/format.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { oops } from '../ui/toast.js';
import { go } from '../router.js';

const num = n => (Number(n) || 0).toLocaleString();

/* label -> what to put there, or null when this tunnel has no such thing */
function values(t) {
  const last = t.rates?.[t.rates.length - 1];
  const pool = t.pool;
  const k = t.kcp;
  return {
    'State': t.state,
    'Tunnel uptime': t.uptime || '—',
    'Traffic in': bytes(t.inBytes || 0),
    'Traffic out': bytes(t.outBytes || 0),
    'Preset': t.preset ? t.preset[0].toUpperCase() + t.preset.slice(1) : '—',
    'Transport': (t.carrier || t.transport || '').toUpperCase(),

    'Peer': t.addr || '—',
    'Control channel': isUp(t) ? 'Held' : 'Not held',
    'Snapshot taken': last ? ago(last.t) : '—',
    'Role': t.role === 'client' ? 'Client — dials out' : 'Server — waits to be dialled',

    'Limits': [t.maxConnections ? `${t.maxConnections} connections` : null,
               t.bandwidthMbps ? `${t.bandwidthMbps} Mbit/s` : null]
              .filter(Boolean).join(' · ') || 'none set',
    'Real client IP': t.proxyProtocol ? 'On (PROXY protocol v2)' : 'Off',

    'Local port': t.botRelayPort || null,
    'Type': t.certType === 'letsencrypt' ? "Let's Encrypt"
          : t.certType === 'selfsigned' ? 'Self-signed' : (t.certType || null),
    'Domain': t.certDomain || null,
    'Expires': t.certExpiry || null,

    'Backup addresses': t.fallbackAddrs?.length ? t.fallbackAddrs.join(', ') : null,
    'Load balancing': t.fallbackAddrs?.length
      ? (t.loadBalance ? 'On — traffic is spread over them' : 'Off — backups are spares') : null,

    'Open now': pool ? `${pool.live} of ${pool.configured} configured` : null,
    'Grown to': pool ? `${pool.live} — target ${pool.target}` : null,
    'Carrying': last ? speed(last.in + last.out) : null,

    'Packets in / out': k ? `${num(k.packetsIn)} / ${num(k.packetsOut)}` : null,
    'Repaired by FEC': k ? num(k.fecRecovered) : null,
    'Retransmitted': k ? num(k.retransmitted) : null,
    'Lost': k ? num(k.lost) : null,
    'Duplicated': k ? num(k.duplicated) : null,
    'Loss': k ? (t.kcpLossPercent ?? 0).toFixed(1) + '%' : null,
  };
}

/* Replace a section's body, keeping its heading.
 *
 * The markup is the caller's, unwrapped: these sections are a chart, a pair of
 * figures and a list of changes, and each needs the element the screen's CSS
 * was written for. Returns the heading's own .sec2, so a caller can reach the
 * badge that sits in it. */
function setSection(root, heading, html) {
  for (const h of $$('.sec2 h4', root)) {
    if (h.textContent.trim().toLowerCase() !== heading.toLowerCase()) continue;
    const sec = h.closest('.sec2');
    const body = sec.nextElementSibling;
    if (body) body.outerHTML = html;
    return sec;
  }
  return null;
}

/* What a section says when there is nothing to draw yet. Said in the section
 * rather than left as the preview's example chart, because a picture of
 * somebody else's traffic is worse than an empty space. */
const nothing = (title, why) =>
  `<div class="dl"><div class="dr2 wide"><span class="k2">${esc(title)}</span>` +
  `<span class="v2">${esc(why)}</span></div></div>`;

/* The speed chart: one line over the window, with a dashed mark wherever the
 * configuration was changed — which is what makes "did that change help?" a
 * question the chart answers rather than a matter of impression. */
/* The named series a tunnel has: what came down, and what went up.
 *
 * One combined line was what this drew before, and it hid the thing worth
 * seeing — a tunnel carrying 90 Mb/s in one direction reads the same as one
 * carrying 45 each way. Two series, named, in their own colours. */
const SERIES = [
  { key: 'down', name: 'Down', colour: 'var(--sc1)' },
  { key: 'up', name: 'Up', colour: 'var(--sc2)' },
];

function legend() {
  return `<div class="mlegend">${SERIES.map(s =>
    `<span><i style="background:${s.colour}"></i>${s.name}</span>`).join('')}</div>`;
}

/* A smooth curve through the points.
 *
 * Straight segments between five-minute samples make a rate look like it
 * changed in steps, which is not what a link does. This is a Catmull-Rom
 * spline written out as cubic béziers: it passes through every sample — so no
 * reading is invented — and only the path between them is eased.
 */
function smooth(pts) {
  if (pts.length < 2) return '';
  let d = `M${pts[0][0].toFixed(1)} ${pts[0][1].toFixed(1)}`;
  for (let i = 0; i < pts.length - 1; i++) {
    const p0 = pts[i - 1] || pts[i], p1 = pts[i], p2 = pts[i + 1], p3 = pts[i + 2] || p2;
    const c1 = [p1[0] + (p2[0] - p0[0]) / 6, p1[1] + (p2[1] - p0[1]) / 6];
    const c2 = [p2[0] - (p3[0] - p1[0]) / 6, p2[1] - (p3[1] - p1[1]) / 6];
    d += ` C${c1[0].toFixed(1)} ${c1[1].toFixed(1)},${c2[0].toFixed(1)} ${c2[1].toFixed(1)},` +
         `${p2[0].toFixed(1)} ${p2[1].toFixed(1)}`;
  }
  return d;
}

/* The chart: gridlines behind, each series a smooth curve over a gradient that
 * fades out downward, and a dashed mark wherever the configuration changed —
 * which is what makes "did that change help?" a question the chart answers.
 *
 * Both series are scaled to the same maximum. Scaling each to its own would
 * draw a trickle and a flood the same height, which is a chart that lies. */
function areaChart(rows, changes, id, ticksToo = true) {
  const n = Math.max(...rows.map(r => r.pts.length));
  if (n < 2) return null;
  const w = 680, h = 160, padT = 12, padB = 10;
  const max = Math.max(1, ...rows.flatMap(r => r.pts.map(p => p.v)));
  const y = v => h - padB - (v / max) * (h - padT - padB);
  const x = i => (i / (n - 1)) * w;

  const grid = [0, .25, .5, .75, 1].map(f => {
    const gy = padT + f * (h - padT - padB);
    return `<line x1="0" y1="${gy.toFixed(1)}" x2="${w}" y2="${gy.toFixed(1)}" class="gl"/>`;
  }).join('');

  const first = rows[0].pts, t0 = first[0].t, t1 = first[first.length - 1].t;
  const marks = (changes || []).filter(at => at >= t0 && at <= t1).map(at => {
    const px = ((at - t0) / Math.max(1, t1 - t0)) * w;
    return `<line x1="${px.toFixed(1)}" y1="0" x2="${px.toFixed(1)}" y2="${h - padB}" class="chg"/>`;
  }).join('');

  const defs = rows.map((r, i) => `<linearGradient id="${id}g${i}" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0%" stop-color="${r.colour}" stop-opacity=".4"/>
      <stop offset="100%" stop-color="${r.colour}" stop-opacity="0"/></linearGradient>`).join('');

  const bands = rows.map((r, i) => {
    const line = smooth(r.pts.map((p, k) => [x(k), y(p.v)]));
    return `<path d="${line} L${w} ${h - padB} L0 ${h - padB} Z" fill="url(#${id}g${i})"/>` +
           `<path class="ln" d="${line}" fill="none" stroke="${r.colour}"/>`;
  }).join('');

  /* The times go under the chart as text, not into it.
     The drawing is stretched to the width it is given — which is right for a
     line and wrong for type: SVG text inside it comes out squashed or stretched
     with everything else. */
  const ticks = !ticksToo ? '' : `<div class="mticks">${[0, .5, 1].map(f => {
    const at = t0 + f * (t1 - t0);
    /* 24-hour, because the window is a day: in 12-hour form the three read
       "03:39 AM · 03:34 PM · 03:29 AM", which looks like they are out of
       order when they are a day apart. */
    return `<span>${esc(new Date(at * 1000)
      .toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false }))}</span>`;
  }).join('')}</div>`;

  return `<div class="chartbox"><svg viewBox="0 0 ${w} ${h}" style="height:${h}px" preserveAspectRatio="none">
    <defs>${defs}</defs>${grid}${marks}${bands}
  </svg>${ticks}</div>`;
}

/* One headline figure, with what it did since the reading before it.
 *
 * The badge is the shape the design calls for — a round tint with an arrow —
 * and it is only drawn when there is a previous reading to compare against.
 * A trend on the first sample would be an arrow pointing at nothing. */
function metricRow(icon, label, value, trend) {
  const dir = trend == null ? null : trend > 0.5 ? 'up' : trend < -0.5 ? 'down' : 'flat';
  const badge = dir === null ? '' :
    `<span class="mtr ${dir}" title="${Math.abs(trend).toFixed(1)}% against the reading before">
       ${dir === 'up' ? '↑' : dir === 'down' ? '↓' : '→'}</span>`;
  return `<div class="mrow">
    <span class="mk">${ICON[icon] || ''}<span>${esc(label)}</span></span>
    <span class="mv">${esc(value)}${badge}</span>
  </div>`;
}

const ICON = {
  rate:  `<svg viewBox="0 0 20 20"><path d="M2 14l4-6 3.5 3L18 4"/></svg>`,
  peak:  `<svg viewBox="0 0 20 20"><path d="M10 3l7 13H3z"/></svg>`,
  clock: `<svg viewBox="0 0 20 20"><circle cx="10" cy="10" r="7.5"/><path d="M10 5.5V10l3 2"/></svg>`,
  loss:  `<svg viewBox="0 0 20 20"><path d="M10 2.6L2.4 16.5h15.2z"/><path d="M10 8v3.4"/><circle cx="10" cy="14" r=".7"/></svg>`,
};

/* Down and up side by side for each day, scaled to the busiest of either — so
 * the two bars in a pair stay comparable to each other and to every other day. */
function dayChart(days) {
  if (!days.length) return null;
  const w = 680, h = 92, base = 77, top = 6;
  const peak = Math.max(...days.map(d => Math.max(d.in || 0, d.out || 0)), 1);
  const slot = w / days.length;
  const bar = (v, x) => {
    const bh = Math.max(1, ((v || 0) / peak) * (base - top));
    return { x, y: base - bh, h: bh };
  };
  return `<div class="chartbox"><svg viewBox="0 0 ${w} ${h}" style="height:${h}px">${days.map((d, i) => {
    const cx = slot * i + slot / 2;
    const dn = bar(d.in, cx - 17), up = bar(d.out, cx + 2);
    return `<g><title>${esc(d.label)}: ↓ ${esc(bytes(d.in || 0))} · ↑ ${esc(bytes(d.out || 0))}</title>
      <rect x="${dn.x.toFixed(1)}" y="${dn.y.toFixed(1)}" width="15" height="${dn.h.toFixed(1)}" rx="2" fill="var(--tx2)"/>
      <rect x="${up.x.toFixed(1)}" y="${up.y.toFixed(1)}" width="15" height="${up.h.toFixed(1)}" rx="2" fill="var(--dim)"/>
      <text x="${cx.toFixed(1)}" y="89" text-anchor="middle" font-size="9.5" fill="var(--dim)">${esc(String(d.label).split(' ')[0])}</text></g>`;
  }).join('')}</svg></div>`;
}

/* A section is its <h4> heading and the list that follows it. */
function dropSection(root, heading) {
  for (const h of $$('.sec2 h4', root)) {
    if (h.textContent.trim().toLowerCase() !== heading.toLowerCase()) continue;
    const sec = h.closest('.sec2');
    const list = sec.nextElementSibling;
    sec.remove();
    if (list && list.classList.contains('dl')) list.remove();
    return;
  }
}

function fill(root, t) {
  const v = values(t);
  for (const row of $$('.dr2', root)) {
    const k = row.querySelector('.k2')?.textContent.trim();
    const val = row.querySelector('.v2');
    if (!k || !val) continue;
    if (!(k in v)) continue;                 /* explanatory rows keep their text */
    if (v[k] === null) { row.remove(); continue; }
    val.textContent = v[k];
  }
  if (!t.botRelay) dropSection(root, 'Bot Relay');
  if (!t.certType) dropSection(root, 'Certificate');
  if (!t.fallbackAddrs?.length) dropSection(root, 'Failover');
  if (!t.pool) dropSection(root, 'Connection pool');
  if (!t.kcp) dropSection(root, 'KCP link quality');
}

/* The top of the screen: which series, the chart, then the figures.
 *
 * The preview drew one line here with an invented shape and a legend that said
 * "throughput, last hour" under it. This is the tunnel's own two series, and
 * the block below is filled from the history call.
 */
function topBlock(root, t) {
  const box = root.querySelector('.mbody > .chartbox');
  if (!box) return;
  const rows = SERIES.map((sp, i) => ({
    colour: sp.colour,
    pts: (t.rates || []).map(p => ({ t: p.t, v: (i ? p.out : p.in) || 0 })),
  }));
  const chart = areaChart(rows, null, 'mlive', false);
  box.outerHTML = legend()
    + (chart || nothing('Not enough samples yet',
        'Speed is the difference between two readings, so the chart appears once there are two.'))
    + `<div class="mrows"></div>`;
  /* The preview's key rows named a single series and a colour this does not
     use; the legend above each chart says what is actually drawn. */
  root.querySelectorAll('.mbody > .key').forEach(k => k.remove());
}

/* Redrawing only the lines, so a poll does not rebuild the block under the
   reader — the same rule the cards follow. */
function spark(root, t) {
  const svg = root.querySelector('.mbody > .chartbox svg');
  if (!svg) return;
  const rows = SERIES.map((sp, i) => ({
    colour: sp.colour,
    pts: (t.rates || []).map(p => ({ t: p.t, v: (i ? p.out : p.in) || 0 })),
  }));
  const fresh = areaChart(rows, null, 'mlive', false);
  if (!fresh) return;
  const tmp = document.createElement('div');
  tmp.innerHTML = fresh;
  svg.innerHTML = tmp.querySelector('svg').innerHTML;
}

export async function metricsView(ctx) {
  const name = ctx.params.name;
  if (!store.tunnel(name)) await store.loadTunnels();

  openScreen('metrics', {
    pick: '.dlg',
    bind: async (root, close) => {
      const paint = () => {
        const t = store.tunnel(name);
        if (!t) return;
        const fl = root.querySelector('.dh .fl');
        if (fl) fl.textContent = flag(t.peerCountry) || flag(t.country) || '·';
        const ttl = root.querySelector('.dh .ttl > div');
        if (ttl) ttl.textContent = name;
        const sub = root.querySelector('.dh .ttl small');
        if (sub) sub.textContent =
          [t.peerLocation, t.peerISP, kindLabel(t)].filter(Boolean).join(' · ');
        const state = root.querySelector('.dh .stt, .dh .state');
        if (state) state.textContent = t.state;
        fill(root, t);
        spark(root, t);
      };
      topBlock(root, store.tunnel(name) || {});
      paint();

      /* The long view. These four sections shipped drawing the preview's
         example charts — a picture of a tunnel nobody has, which reads as
         "this tunnel carried 142 GB on Sunday" and is a lie. They were then
         replaced by a button that opened another screen, which is why the
         report that reached us said they show nothing at all.
         They are drawn here now, from this tunnel's own history. */
      const t = store.tunnel(name) || {};
      try {
        const h = await api.history(name);
        const collecting = !!h.collecting;
        const series = collecting ? [] :
          (h.series || []).map(p => ({ t: p.t, v: (p.in || 0) + (p.out || 0) }));

        const rows = SERIES.map((sp, i) => ({
          colour: sp.colour,
          pts: collecting ? [] : (h.series || []).map(p => ({ t: p.t, v: (i ? p.out : p.in) || 0 })),
        }));
        setSection(root, 'Last 24 hours',
          (areaChart(rows, h.changes, 'mday') ? legend() + areaChart(rows, h.changes, 'mday') : null) ||
          nothing('Not enough samples yet',
            'Speed is the difference between two five-minute samples, so the chart appears once there are two.'));

        /* The headline figures, under the chart at the top they come from. */
        const last = series[series.length - 1], prev = series[series.length - 2];
        const step = last && prev && prev.v ? ((last.v - prev.v) / prev.v) * 100 : null;
        const peak = series.length ? Math.max(...series.map(p => p.v)) : null;
        const upPct = v => (typeof v === 'number' && v >= 0) ? v.toFixed(1) + '%' : '—';
        const rowsBox = root.querySelector('.mrows');
        if (rowsBox) {
          rowsBox.innerHTML = series.length
            ? metricRow('rate', 'Carrying now', speed(last.v), step)
              + metricRow('peak', 'Peak in the day', speed(peak), null)
              + metricRow('clock', 'Up, last 24 hours', upPct(h.uptime24h), null)
            : '';
        }

        setSection(root, 'Per day — ↓ down · ↑ up',
          dayChart(collecting ? [] : (h.days || [])) ||
          nothing('No daily totals yet', 'They build up an hour at a time.'));

        const pct = v => (typeof v === 'number' && v >= 0) ? v.toFixed(1) + '%' : '—';
        setSection(root, 'Uptime', `<div class="upt">
          <div class="u2"><div class="k2">Last 24 hours</div><div class="v2">${pct(h.uptime24h)}</div></div>
          <div class="u2"><div class="k2">Last 7 days</div><div class="v2">${pct(h.uptime7d)}</div></div>
        </div>`);
      } catch (e) {
        const why = 'The history for this tunnel could not be read.';
        setSection(root, 'Last 24 hours', nothing('Unavailable', why));
        setSection(root, 'Per day — ↓ down · ↑ up', nothing('Unavailable', why));
        setSection(root, 'Uptime', nothing('Unavailable', why));
      }

      /* The configurations this tunnel had before each edit. Restoring one is
         the undo screen's job — it arms the change, restarts on it and reverts
         if the tunnel does not come up — so the button goes there rather than
         carrying a second copy of a flow that can take a tunnel down. */
      try {
        const changes = (await api.confHistory(name)).changes || [];
        const sec = setSection(root, 'Configuration history', changes.length
          ? `<div class="ch2">${changes.map(c => `<div class="row2">
               <span class="when">${esc(c.when)}</span>
               <span class="what">${esc(c.note || 'the configuration in place before this')}</span>
               <button class="mini2" data-to="undo">Restore</button></div>`).join('')}</div>`
          : nothing('Nothing to go back to',
              'Nothing has been changed on this tunnel yet, so there is no earlier configuration kept.'));
        const badge = sec?.querySelector('.badge2');
        if (badge) badge.textContent = changes.length ? `${changes.length} kept` : 'none kept';
      } catch (e) {
        setSection(root, 'Configuration history',
          nothing('Unavailable', 'The change history for this tunnel could not be read.'));
      }

      const lt = root.querySelector('.lt b');
      if (lt) lt.textContent = t.addr || '—';

      root.addEventListener('click', ev => {
        const b = ev.target.closest('[data-to]');
        if (b) go(`/t/${encodeURIComponent(name)}/${b.dataset.to}`);
        if (ev.target.closest('#ltbtn')) go(`/t/${encodeURIComponent(name)}/link`);
      });

      const unsub = store.subscribe(paint);
      ctx.setTeardown(() => { unsub(); close(); });
    },
  }).catch(oops);
}

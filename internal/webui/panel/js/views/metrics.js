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
function areaChart(points, changes, id) {
  if (points.length < 2) return null;
  const w = 680, h = 86, pad = 4;
  const max = Math.max(...points.map(p => p.v), 1);
  const x = i => (i / (points.length - 1)) * w;
  const y = v => h - pad - (v / max) * (h - pad * 2);
  const line = points.map((p, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)} ${y(p.v).toFixed(1)}`).join(' ');
  const t0 = points[0].t, t1 = points[points.length - 1].t;
  const marks = (changes || []).filter(at => at >= t0 && at <= t1).map(at => {
    const px = ((at - t0) / Math.max(1, t1 - t0)) * w;
    return `<line x1="${px.toFixed(1)}" y1="0" x2="${px.toFixed(1)}" y2="${h}" ` +
           `stroke="var(--dim)" stroke-width="1" stroke-dasharray="2 3"/>`;
  }).join('');
  return `<div class="chartbox"><svg viewBox="0 0 ${w} ${h}" style="height:${h}px" preserveAspectRatio="none">
    <defs><linearGradient id="${id}" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0%" stop-color="var(--spark)" stop-opacity=".18"/>
      <stop offset="100%" stop-color="var(--spark)" stop-opacity="0"/></linearGradient></defs>
    ${marks}<path d="${line} L${w} ${h} L0 ${h} Z" fill="url(#${id})"/>
    <path class="sparkpath" d="${line}" fill="none" stroke="var(--spark)" stroke-width="2"
      stroke-linecap="round" stroke-linejoin="round" vector-effect="non-scaling-stroke"/>
  </svg></div>`;
}

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

function spark(root, rates) {
  const path = root.querySelector('#mspk path, .spk path, svg path.sparkpath');
  if (!path || !rates || rates.length < 2) return;
  const w = 340, h = 60;
  const max = Math.max(...rates.map(p => p.in + p.out), 1);
  const step = w / (rates.length - 1);
  path.setAttribute('d', rates.map((p, i) =>
    `${i ? 'L' : 'M'}${(i * step).toFixed(1)} ${(h - 4 - ((p.in + p.out) / max) * (h - 8)).toFixed(1)}`
  ).join(' '));
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
        spark(root, t.rates);
      };
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

        setSection(root, 'Last 24 hours',
          areaChart(series, h.changes, 'mday') ||
          nothing('Not enough samples yet',
            'Speed is the difference between two five-minute samples, so the chart appears once there are two.'));

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

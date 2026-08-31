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

import { $$ } from '../lib/dom.js';
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
    'Traffic in': bytes(t.bytesIn || 0),
    'Traffic out': bytes(t.bytesOut || 0),
    'Preset': t.preset ? t.preset[0].toUpperCase() + t.preset.slice(1) : '—',
    'Transport': (t.carrier || t.transport || '').toUpperCase(),

    'Peer': t.addr || '—',
    'Control channel': t.state === 'running' ? 'Held' : 'Not held',
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

/* Replace a section's body, keeping its heading. */
function setSection(root, heading, html) {
  for (const h of $$('.sec2 h4', root)) {
    if (h.textContent.trim().toLowerCase() !== heading.toLowerCase()) continue;
    const list = h.closest('.sec2').nextElementSibling;
    if (list) list.outerHTML = `<div class="dl">${html}</div>`;
    return true;
  }
  return false;
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

      /* The long view and the change list are separate endpoints; the preview
         drew them in this same dialog, so they are filled here too. */
      try {
        const h = await api.history(name);
        if (!h.collecting) {
          const set = (label, text) => {
            for (const row of $$('.dr2', root)) {
              if (row.querySelector('.k2')?.textContent.trim() === label) {
                row.querySelector('.v2').textContent = text;
              }
            }
          };
          if (h.uptime24h >= 0) set('Last 24 hours', h.uptime24h.toFixed(1) + '%');
          if (h.uptime7d >= 0) set('Last 7 days', h.uptime7d.toFixed(1) + '%');
        }
      } catch (e) { /* the section simply keeps its heading */ }

      /* These four were drawn here with example charts and an example probe.
         Each has a screen of its own now, so the section says what it is and
         opens it rather than showing a picture of somebody else's tunnel. */
      const t = store.tunnel(name) || {};
      const jump = (label, note, to) =>
        `<div class="jumprow"><span>${note}</span>` +
        `<button class="btn6" data-to="${to}">${label}</button></div>`;

      setSection(root, 'Last 24 hours',
        jump('Open history', 'Speed between five-minute samples, with the moments the configuration changed.', 'history'));
      setSection(root, 'Per day — ↓ down · ↑ up',
        jump('Open history', 'Totals for each of the last seven days.', 'history'));
      setSection(root, 'Uptime',
        jump('Open history', 'The share of samples this tunnel was up, over a day and over a week.', 'history'));
      setSection(root, 'Configuration history',
        jump('Undo a change', 'The configurations this tunnel had before each edit, and the chance to put one back.', 'undo'));

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

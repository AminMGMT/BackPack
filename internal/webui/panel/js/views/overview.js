/* Overview — the whole installation in one screen.
 *
 * It repeats nothing from the strip above it. The strip is this machine right
 * now — processor, memory, what is moving this second. This page is everything
 * that is not a live meter: what has been carried since the server was set up,
 * which tunnels carried it, and whatever is currently wrong.
 *
 * Three rules it is built to:
 *
 *   Trouble first. A page that opens with healthy numbers and hides the one
 *   failure among them is a page that gets glanced at and trusted.
 *
 *   Compact over complete. Every block here answers a question someone actually
 *   asks. A figure nobody acts on is one more thing to read past.
 *
 *   The motion carries meaning or it does not run. Bars grow to their value and
 *   the total counts to it, so the eye is told what changed; nothing loops,
 *   nothing decorates, and prefers-reduced-motion turns all of it off.
 */

import { $, esc } from '../lib/dom.js';
import { svg } from '../lib/icons.js';
import { isUp } from '../lib/tstate.js';
import { bytes } from '../lib/format.js';
import * as store from '../store.js';
import * as api from '../api.js';
import { go } from '../router.js';

const REDUCED = matchMedia('(prefers-reduced-motion: reduce)').matches;

/* ---- the day strip ------------------------------------------------------- */
/* The window is a month. The store keeps 720 hourly buckets and the endpoint
   takes ?days= up to 30, summed across every tunnel. */
let dayTotals = null;

async function loadDayTotals(names) {
  const sum = new Map();
  const runs = await Promise.allSettled(names.map(n => api.history(n, 30)));
  for (const r of runs) {
    if (r.status !== 'fulfilled' || !r.value?.days) continue;
    for (const d of r.value.days) {
      sum.set(d.label, (sum.get(d.label) || 0) + (d.in || 0) + (d.out || 0));
    }
  }
  dayTotals = [...sum].map(([label, b]) => ({ label, bytes: b }));
  return dayTotals;
}

/* Drawn as elements rather than an SVG path so each day can carry its own
   growth delay — the chart fills left to right in the order the days happened,
   which is the one thing a month of traffic has to say. */
function dayBars() {
  if (!dayTotals?.length) {
    return `<div class="tk-empty">No per-day figures yet — they build up as the tunnels carry traffic.</div>`;
  }
  const peak = Math.max(...dayTotals.map(d => d.bytes), 1);
  return `<div class="tk-days" style="--n:${dayTotals.length}">${dayTotals.map((d, i) => {
    const h = Math.max(3, (d.bytes / peak) * 100);
    return `<i style="--h:${h.toFixed(1)}%;--i:${i}" title="${esc(d.label)} · ${esc(bytes(d.bytes))}"></i>`;
  }).join('')}</div>
  <div class="tk-axis"><span>${esc(dayTotals[0].label)}</span><span class="sp"></span>
    <span>${esc(dayTotals[dayTotals.length - 1].label)}</span></div>`;
}

/* ---- what is wrong, if anything ------------------------------------------ */
function troubles(state, fleet, backup) {
  const s = state.stats || {};
  const out = [];

  const down = (state.tunnels || []).filter(t => !isUp(t));
  if (down.length) {
    const one = down[0];
    out.push({
      k: 'er',
      b: down.length === 1 ? `${one.name} is not running` : `${down.length} tunnels are not running`,
      // One tunnel is named in the heading, so the line under it says something
      // else — repeating the name is a second line that carries nothing.
      s: down.length === 1
        ? [one.role, one.transport || one.carrier, one.addr].filter(Boolean).join(' · ')
        : down.map(t => t.name).join(', '),
      act: 'Tunnels', to: '/tunnels',
    });
  }
  const offline = (fleet?.nodes || []).filter(n => !n.online);
  if (offline.length) {
    out.push({
      k: 'wr',
      b: offline.length === 1 ? `${offline[0].name} is not connected` : `${offline.length} servers are not connected`,
      s: 'Tunnels there keep running, but this panel cannot change them until they are back.',
      act: 'Servers', to: '/servers',
    });
  }
  if (s.monitorRunning === false) {
    out.push({
      k: 'wr', b: 'The monitor service is not running',
      s: 'The watchdog, the bot and the alerts live in it. A tunnel that drops will not be restarted.',
      act: 'Health', to: '/health',
    });
  }
  /* Chosen and not in force. The wizard offers BBR; a kernel without it takes
     the setting, reports something else, and every tunnel runs on the fallback
     — which is a performance question nobody thinks to ask. It was said only in
     the menu, where it is read once when a server is set up. */
  if (s.congestionWanted && s.congestion && s.congestion !== s.congestionWanted) {
    out.push({
      k: 'wr',
      b: `${s.congestionWanted.toUpperCase()} is not available on this kernel`,
      s: `Congestion control is running ${s.congestion.toUpperCase()} instead.`,
      act: 'Health', to: '/health',
    });
  }
  /* On but not up. Ports forwarded to the built-in proxy are refused while it
     is in this state, and nothing else on the panel says so. */
  if (s.proxyEnabled && s.proxyRunning === false) {
    out.push({
      k: 'er',
      b: 'The built-in proxy is enabled but not running',
      s: 'Forwarded ports pointing at it are refused until it starts.',
      act: 'Health', to: '/health',
    });
  }
  if (backup && backup.enabled === false) {
    out.push({
      k: 'wr', b: 'Automatic backups are off',
      s: 'A backup holds every config, token and certificate — the one loss on this server that cannot be undone.',
      act: 'Backup', to: '/maintenance',
    });
  } else if (backup?.last && staleDays(backup.last) > 10) {
    out.push({
      k: 'wr', b: `The last backup was ${staleDays(backup.last)} days ago`,
      s: 'Automatic backups are weekly, so one this old means they are not completing.',
      act: 'Backup', to: '/maintenance',
    });
  }
  if (s.updateTag) {
    out.push({
      k: 'in', b: `${s.updateTag} is available`, s: `This server is on ${s.version || '—'}.`,
      act: 'Update', to: '/maintenance',
    });
  }
  return out;
}

/* ---- per-tunnel ----------------------------------------------------------
   Sorted by what each one carried, because that is the order the question
   "where does the traffic go" is asked in. The bar is a share of the busiest,
   not of the total: shares of a total are unreadable once one tunnel dominates,
   and the busiest is the comparison anyone actually makes. */
/* A tunnel with no measurement reports -1, not nothing, so a plain truthiness
   test prints "-1 ms" — which the dashboard has always got right and this did
   not. Same rule as there: a ping is a ping when it is zero or more. */
function footer() {
  return `<div class="ov-foot">
    <div class="ov-links">
      <a href="https://github.com/AminMGMT" target="_blank" rel="noopener" title="GitHub" aria-label="GitHub">
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 19c-5 1.5-5-2.5-7-3m14 6v-3.9a3.4 3.4 0 00-1-2.6c3-.3 6.5-1.5 6.5-7A5.4 5.4 0 0020 4.8 5 5 0 0019.9 1S18.7.6 16 2.5a13.4 13.4 0 00-7 0C6.3.6 5.1 1 5.1 1A5 5 0 005 4.8 5.4 5.4 0 003.5 8.5c0 5.5 3.5 6.7 6.5 7a3.4 3.4 0 00-1 2.6V22"/></svg>
      </a>
      <a href="https://t.me/BlackProtocols" target="_blank" rel="noopener" title="Telegram" aria-label="Telegram">
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M22 3L2 11l6 2 2 6 3-4 5 4z"/><path d="M8 13l8-6"/></svg>
      </a>
    </div>
    <a class="ov-by" href="https://blackprotocols.space" target="_blank" rel="noopener">developed by AminMGMT</a>
  </div>`;
}

/* The four things done often enough to deserve a door on the front page.
 *
 * They sit beside the machine's own facts rather than under them, because the
 * pair is what this section is: what this server is, and what you would do to
 * it. Each carries its own icon — four rows of identical grey text is a list
 * to read, and these are meant to be aimed at.
 *
 * Bug reports leaves the panel, so it is an anchor and says so; the other
 * three are screens here. */
const ACTIONS = [
  { icon: 'pulse', label: 'Health check', to: '/health',
    note: 'What this server would trip over' },
  { icon: 'gear', label: 'Settings', to: '/settings',
    note: 'The panel, alerts and backups' },
  { icon: 'life', label: 'Support', to: '/support',
    note: 'Where to ask, and what to send' },
  { icon: 'bug', label: 'Bug reports', href: 'https://github.com/AminMGMT/BackPack/issues',
    note: 'Report one, or read the open ones' },
];

function quickActions() {
  return `<div class="ov-acts">${ACTIONS.map(a => {
    const inner = `<span class="qa-ic">${svg(a.icon, 'ic')}</span>
      <span class="qa-tx"><b>${esc(a.label)}</b><span>${esc(a.note)}</span></span>`;
    return a.href
      ? `<a class="qa" href="${esc(a.href)}" target="_blank" rel="noopener">${inner}</a>`
      : `<button class="qa" data-to="${esc(a.to)}">${inner}</button>`;
  }).join('')}</div>`;
}

function facts(s) {
  const rows = [
    ['Host', s.hostname], ['Version', s.version], ['OS', s.os],
    ['Location', s.location], ['ISP', s.isp],
    ['IPv4', s.ipv4], ['IPv6', s.ipv6], ['Uptime', s.uptime],
    // Both of these have a row above when they are wrong. Here they are just
    // facts about the machine, which is what they are when they are right.
    ['Congestion', s.congestion ? s.congestion.toUpperCase() : ''],
    ['Built-in proxy', s.proxyEnabled && s.proxyRunning
      ? (s.proxyType || 'proxy').toUpperCase() + (s.proxyPort ? ' :' + s.proxyPort : '') : ''],
  ].filter(([, v]) => v && v !== '-');
  return `<div class="ov-facts">${rows.map(([k, v]) =>
    `<div class="ov-fact"><span>${esc(k)}</span><b>${esc(v)}</b></div>`).join('')}</div>`;
}

/* The total counts up to itself once and then stops.
 *
 * "Once" cannot be recorded on the element: every paint rebuilds the page from
 * innerHTML, so the node is a new one each time and a flag on it is always
 * absent. The caller owns the flag instead — otherwise the figure re-animates
 * on every poll, which is the one thing a number nobody can read looks like.
 */
function countTo(node, value, animate) {
  if (!node) return;
  // Before the first read there is no figure, and "0 B" is a claim. The dash is
  // the placeholder the rest of the panel uses for the same thing.
  if (value === null || value === undefined) { node.textContent = '—'; return; }
  const text = bytes(value);
  if (REDUCED || !animate) { node.textContent = text; return; }

  const [target, unit = ''] = text.split(' ');
  const to = parseFloat(target);
  if (!isFinite(to)) { node.textContent = text; return; }

  const dur = 900;
  let start = null;
  const tick = now => {
    // The timestamp a frame carries is when the frame began, which can be
    // earlier than the performance.now() taken while scheduling it — so a start
    // captured outside gives a negative progress on the first frame and the
    // figure flashes below zero. Taking it from the first frame cannot.
    if (start === null) start = now;
    const p = Math.max(0, Math.min(1, (now - start) / dur));
    // Ease out: fast to nearly-there, then settle, like the panel's --spring.
    const e = 1 - Math.pow(1 - p, 3);
    const dp = to < 10 ? 2 : to < 100 ? 1 : 0;
    node.textContent = (to * e).toFixed(dp) + (unit ? ' ' + unit : '');
    if (p < 1) requestAnimationFrame(tick);
    else node.textContent = text;
  };
  requestAnimationFrame(tick);
}

/* Whole days since an ISO timestamp. */
function staleDays(iso) {
  const t = Date.parse(iso);
  if (!isFinite(t)) return 0;
  return Math.floor((Date.now() - t) / 86400000);
}

/* The per-tunnel breakdown that used to sit beside this one is gone.
 *
 * It listed every tunnel and what each had carried — which is what the tunnel
 * cards say, on the screen those tunnels live on. Two places drawing the same
 * numbers means two places to keep right, and the overview is meant to be the
 * shape of the machine rather than an index of everything on it. */

export function overview(ctx) {
  const view = $('#view');
  let fleet = null;
  let backup = null;
  let counted = false;   // the total has had its one run

  /* The page is built once and then edited.
   *
   * It used to be written from innerHTML on every poll, which is four times a
   * minute — and every one of those threw the whole page away and made it
   * again. That is what the report of "the overview refreshes graphically
   * every few seconds, and it is distracting" describes: not the data
   * changing, but every entrance animation on the page restarting together,
   * the counter running up from nothing again, and anything the reader had
   * hovered or focused coming back as a different element. The tunnel cards
   * were fixed this way already; this screen never was.
   *
   * So each region carries a signature of what it draws. When the signature is
   * the same the region is left alone, and the figures that move on every poll
   * — the total, the split, each row's bar — are written into the elements
   * that are already there. */
  view.innerHTML = `
    <div class="ov">
      <div class="ov-bar" id="ovBar"></div>

      <div class="sectitle"><h3>This server</h3><div class="ln"></div></div>
      <!-- One machine, said in three parts: what it has carried, what you would
           do to it, and what it is. The traffic takes the full width on its own
           because it is the figure people come here for and it carries a month
           of bars under it; the pair below share the width evenly. -->
      <div class="ov-srv">
        <!-- Traffic: the one figure that only grows, with the month that made it. -->
        <div class="tk">
          <div class="tk-head">
            <div class="tk-tot">
              <span class="tk-num" id="ovTotal">—</span>
              <span class="tk-cap">carried since this server was set up</span>
            </div>
            <div class="tk-chips" id="ovChips"></div>
          </div>
          <div class="tk-split" aria-hidden="true">
            <i class="up"></i><i class="dn"></i>
          </div>
          <div id="ovDays"></div>
        </div>

        ${quickActions()}
        <div id="ovFacts"></div>
      </div>

      ${footer()}
    </div>`;

  const ov = view.querySelector('.ov');
  const el = id => view.querySelector('#' + id);
  const sigs = new Map();
  /* Replace a region only when what it draws has changed. */
  const region = (id, sig, html) => {
    const n = el(id);
    if (!n || sigs.get(id) === sig) return false;
    sigs.set(id, sig);
    n.innerHTML = html;
    return true;
  };

  /* One listener for the page, bound once. Binding them after each paint is
     what made a click open four screens: the old paint added a fresh listener
     to every button it had just made, and nothing removed the last set. */
  view.addEventListener('click', ev => {
    const b = ev.target.closest('[data-to]');
    if (b && view.contains(b)) go(b.dataset.to);
  });

  const paint = () => {
    /* Nothing to paint once this page has been replaced.
     *
     * The fleet and the backup state are read once and painted when they
     * arrive, and the store's own poll calls this too — all of which can land
     * after the route has moved on and #view has been rebuilt by another page.
     * Without this they wrote into elements that no longer exist, which is
     * exactly what happened every time this page was the one under a dialog. */
    if (!ov.isConnected) return;

    const state = store.get();
    const s = state.stats || {};
    const tuns = state.tunnels || [];
    const up = tuns.filter(isUp).length;
    const nodes = fleet?.nodes || [];
    const onlineNodes = nodes.filter(n => n.online).length;
    const bad = troubles(state, fleet, backup);

    /* The numeric siblings, not totalSent/totalRecv: those are formatted for
       reading, and adding two of them concatenated the strings, which made the
       split bar below NaN% wide and therefore invisible. */
    const sent = Number(s.totalSentBytes) || 0, recv = Number(s.totalRecvBytes) || 0;
    const both = sent + recv || 1;

    ov.classList.toggle('settled', counted);

    region('ovBar', JSON.stringify([bad, up, tuns.length, onlineNodes, nodes.length]),
      bad.length
        ? bad.map(t => `<button class="ov-pill ${t.k}" data-to="${esc(t.to)}" title="${esc(t.s)}">
             <span class="ov-dot"></span>${esc(t.b)}</button>`).join('')
        : `<span class="ov-pill ok"><span class="ov-dot"></span>
             ${up} of ${tuns.length} tunnels up${nodes.length
               ? ` · ${onlineNodes}/${nodes.length} servers connected` : ''}</span>`);

    region('ovFacts', JSON.stringify([s.hostname, s.version, s.os, s.location, s.isp,
      s.ipv4, s.ipv6, s.uptime, s.congestion, s.proxyEnabled, s.proxyRunning,
      s.proxyType, s.proxyPort]), facts(s));

    region('ovChips', JSON.stringify([sent, recv, up, tuns.length, onlineNodes, nodes.length]),
      `<span class="tk-chip"><i class="up"></i>${esc(bytes(sent))} sent</span>
       <span class="tk-chip"><i class="dn"></i>${esc(bytes(recv))} received</span>
       <span class="tk-chip ghost">${up}/${tuns.length} tunnels</span>
       ${nodes.length ? `<span class="tk-chip ghost">${onlineNodes}/${nodes.length} servers</span>` : ''}`);

    /* The split is two widths, so it is set rather than rebuilt. */
    const split = view.querySelectorAll('.tk-split i');
    if (split[0]) split[0].style.setProperty('--w', `${((sent / both) * 100).toFixed(1)}%`);
    if (split[1]) split[1].style.setProperty('--w', `${((recv / both) * 100).toFixed(1)}%`);

    region('ovDays', JSON.stringify(dayTotals), dayBars());

    const total = state.stats ? (Number(s.totalTrafficBytes) || 0) : null;
    countTo(el('ovTotal'), total, !counted && total > 0);
    if (total > 0) counted = true;
  };

  const unsub = store.subscribe(paint);
  ctx.setTeardown(unsub);

  /* The fleet is not in the store — nothing else polls it — so it is read once.
     A panel with the feature off answers instantly with an empty list. */
  api.nodes().then(f => { fleet = f; paint(); }).catch(() => {});

  /* Backups are read once too. It is the only state on this page whose failure
     is unrecoverable, and it is silent while it is fine — a row that appears
     only when something is wrong is a row people believe. */
  api.autoBackup().then(b => { backup = b; paint(); }).catch(() => {});

  /* The month of traffic is one request per tunnel, so it waits for the first
     tunnel list rather than asking once and giving up. */
  let asked = false;
  const stop = store.subscribe(async state => {
    if (asked || dayTotals || !state.tunnels?.length) return;
    asked = true;
    try { await loadDayTotals(state.tunnels.map(t => t.name)); paint(); }
    catch (e) { /* the card simply has no month */ }
  });
  ctx.setTeardown(() => { unsub(); stop(); });

  paint();
}

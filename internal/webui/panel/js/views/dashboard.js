/* The dashboard: the server strip, then the fleet.
 *
 * The markup is the approved preview's, class for class — .RZ tiles and .c7
 * cards — so the design is the one that was signed off rather than a redraw of
 * it. What changed is only where the numbers come from.
 *
 * CLI: Manage → Status, and Manage → Manage Tunnels.
 */

import { $, delegate, esc } from '../lib/dom.js';
import { isUp, stateLabel, stateTone, serviceDown } from '../lib/tstate.js';
import { bytes, speed, kindLabel, flag } from '../lib/format.js';
import * as store from '../store.js';
import * as api from '../api.js';
import { toast, oops, onFix } from '../ui/toast.js';
import { confirmBox } from '../ui/confirm.js';
import { go } from '../router.js';

/* ---- the rate chart behind a card ---------------------------------------- */
/* The preview drew a fixed path; this is the same shape from real samples. */
/* The metric field behind a card.
 *
 * The shape is the progress-metric-card design: a chart region taking the right
 * of the card, behind the content, with a dotted ground fading in from the left
 * and a wash of the accent colour behind the line. The content sits over it and
 * does not take pointer events, so the card reads as one surface rather than a
 * panel with a picture stuck to it.
 *
 * The accent is not decoration — it is the reading. It is the tunnel's state:
 * green while it is connected, red while it is offline, and plain white while
 * it is stopped or still trying, so the card says how it is before any number
 * is read.
 */
const REGION_W = 62;      // % of the card the chart region takes

/* Which view each card is in. Kept out here because a card is rebuilt whenever
   its data changes, and a toggle that reset itself every few seconds would be
   a toggle nobody could use. */
const cardView = new Map();   // name -> 'curve' | 'bars'

/* Which window each card draws, kept out here for the same reason: a card is
   rebuilt whenever any figure on it changes, so a choice held in the markup
   would be lost within seconds of being made. */
const cardPeriod = new Map(); // name -> period key

/* The windows a card can draw.
 *
 * `live` is the rate history the panel already holds — the engine writes a
 * metrics snapshot every 30 seconds and seriesOf takes the last 24 of them, so
 * it is about twelve minutes. Everything longer is the history the monitor
 * writes, and it is fetched only when it is asked for: the dashboard polls
 * every six seconds, and putting a per-tunnel history request on that path
 * would be one fetch per tunnel per poll for a figure nobody had asked to see.
 *
 * The unit changes with the window, and has to. live and 24h are rates — what
 * the tunnel was carrying at that moment. The day windows are totals, because
 * a total is what an hourly bucket adds up to; drawing them as a rate would
 * put a per-day number on an axis that means per-second. */
const PERIODS = [
  { key: 'live', label: 'Live',          unit: 'rate',  note: 'Last ~12 minutes' },
  { key: '24h',  label: 'Past 24 hours', unit: 'rate',  note: 'Speed, 5-minute samples' },
  { key: '7d',   label: 'Past 7 days',   unit: 'total', note: 'Total per day', days: 7 },
  { key: '30d',  label: 'Past 30 days',  unit: 'total', note: 'Total per day', days: 30 },
];

const periodOf = name =>
  PERIODS.find(p => p.key === cardPeriod.get(name)) || PERIODS[0];

/* Fetched history, per tunnel and window. The store behind it moves every five
   minutes, so a reading kept for one is fresh by definition and a card that is
   left open does not re-ask on every repaint. */
const HIST_TTL = 5 * 60 * 1000;
const histCache = new Map(); // `${name}|${key}` -> { at, points }

async function loadPeriod(name, key) {
  const p = PERIODS.find(x => x.key === key);
  if (!p || p.unit === undefined || key === 'live') return;
  const ck = name + '|' + key;
  const got = histCache.get(ck);
  if (got && Date.now() - got.at < HIST_TTL) return;

  const data = await api.history(name, p.days);
  /* Nothing sampled yet is not an error — the monitor writes the first points
     five minutes after it starts. An empty series is cached so the card stops
     asking, and the card falls back to the live window. */
  const points = data.collecting ? []
    : p.unit === 'rate'
      ? (data.series || []).map(s => ({ t: s.t, v: (s.in || 0) + (s.out || 0) }))
      : (data.days || []).map((d, i) => ({ t: i, v: (d.in || 0) + (d.out || 0) }));
  histCache.set(ck, { at: Date.now(), points });
}

/* Rates are quoted in bits the way a link is sold; totals are bytes the way a
   quota is. The window decides which, so the footer figures and the chart can
   never disagree about what they are counting. */
const fmtOf = name => (periodOf(name).unit === 'total' ? bytes : speed);

/* The series a card draws: what the tunnel was carrying across the chosen
   window. A window whose history has not arrived — or has nothing in it yet —
   falls back to the live samples rather than blanking the chart, because an
   empty card says less than a short one. */
function seriesOf(t) {
  const p = periodOf(t.name);
  if (p.key !== 'live') {
    const got = histCache.get(t.name + '|' + p.key);
    if (got && got.points.length >= 2) return got.points;
  }
  return (t.rates || []).slice(-24).map(r => ({ t: r.t, v: (r.in || 0) + (r.out || 0) }));
}

/* Whether what is on screen is the window that was asked for. The footer says
   so when it is not, so a card showing twelve minutes under a heading that
   says thirty days is never left to be read as thirty days. */
function periodReady(t) {
  const p = periodOf(t.name);
  if (p.key === 'live') return true;
  const got = histCache.get(t.name + '|' + p.key);
  return !!(got && got.points.length >= 2);
}

function statsOf(vals) {
  if (!vals.length) return null;
  const first = vals[0], last = vals[vals.length - 1];
  const prev = vals.length > 1 ? vals[vals.length - 2] : first;
  const sum = vals.reduce((a, b) => a + b, 0);
  const net = last - first;
  return {
    net, step: last - prev,
    pct: first ? (net / first) * 100 : 0,
    peak: Math.max(...vals), low: Math.min(...vals), avg: sum / vals.length,
  };
}

/* curve: an area under a line. bars: one column per sample.
   Both are drawn to the same box so switching does not move anything. */
function chartSVG(series, view, id) {
  const w = 340, h = 150, pad = 10;
  if (series.length < 2) return '';
  const max = Math.max(...series.map(p => p.v), 1);
  const y = v => h - pad - (v / max) * (h - pad * 2);

  if (view === 'bars') {
    /* Solid, and nothing behind them. The area gradient belongs to the line —
       left under the bars it read as a shadow the columns were casting. */
    const slot = w / series.length;
    const bw = Math.max(2, slot * 0.62);
    return series.map((p, i) => {
      const top = y(p.v), x = slot * i + (slot - bw) / 2;
      return `<rect x="${x.toFixed(1)}" y="${top.toFixed(1)}" width="${bw.toFixed(1)}" ` +
             `height="${Math.max(1, h - pad - top).toFixed(1)}" rx="2" fill="var(--mc)" opacity=".8"/>`;
    }).join('');
  }
  const step = w / (series.length - 1);
  const line = series.map((p, i) => `${i ? 'L' : 'M'}${(i * step).toFixed(1)} ${y(p.v).toFixed(1)}`).join(' ');
  return `<path d="${line} L${w} ${h} L0 ${h} Z" fill="url(#${id})"/>` +
         `<path d="${line}" fill="none" stroke="var(--mc)" stroke-width="2" ` +
         `stroke-linecap="round" stroke-linejoin="round" vector-effect="non-scaling-stroke"/>`;
}

function field(t, idx) {
  const series = seriesOf(t);
  if (series.length < 2) return '';
  const id = 'L' + idx;
  const view = cardView.get(t.name) || 'curve';
  return `<div class="mfield" style="width:${REGION_W}%">
    <div class="mgrid"></div>
    <div class="mwash"></div>
    <svg class="mchart" viewBox="0 0 340 150" preserveAspectRatio="none" aria-hidden="true">
      <defs><linearGradient id="${id}" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" stop-color="var(--mc)" stop-opacity=".22"/>
        <stop offset="100%" stop-color="var(--mc)" stop-opacity="0"/></linearGradient></defs>
      ${chartSVG(series, view, id)}
    </svg>
  </div>`;
}

const RELAY_SVG = `<svg class="x" viewBox="0 0 24 24"><rect x="4" y="8" width="16" height="11" rx="3"/>
<path d="M12 4v4"/><circle cx="12" cy="3" r="1.4"/><path d="M9 13.5h.01M15 13.5h.01"/></svg>`;

/* The direction the window moved, end to end. Three states rather than two:
   a reading that barely moved is flat, and an arrow that commits to up or down
   on a half-percent drift is noise wearing the clothes of a measurement. */
const TREND = {
  up:   `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 19V5"/><path d="M5 12l7-7 7 7"/></svg>`,
  down: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14"/><path d="M5 12l7 7 7-7"/></svg>`,
  flat: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12h14"/><path d="M12 5l7 7-7 7"/></svg>`,
};
const CHEV = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 9l6 6 6-6"/></svg>`;

/* Under this, the window is flat. Matches the reference design's own dead
   zone: without one, a card twitches between up and down forever. */
const NEUTRAL_PCT = 0.5;

function trendHTML(st) {
  if (!st) return '';
  const flat = Math.abs(st.pct) < NEUTRAL_PCT;
  const icon = flat ? TREND.flat : st.net >= 0 ? TREND.up : TREND.down;
  return `<span class="trend" title="Change across the window drawn">` +
         `${icon}${Math.abs(st.pct).toFixed(1)}%</span>`;
}

const VIEW = {
  curve: `<svg class="iB" viewBox="0 0 24 24"><path d="M3 17c4-1 5-9 9-9s5 5 9 4"/></svg>`,
  bars:  `<svg class="iB" viewBox="0 0 24 24"><rect x="4" y="12" width="3.4" height="8" rx="1"/><rect x="10.3" y="7" width="3.4" height="13" rx="1"/><rect x="16.6" y="14" width="3.4" height="6" rx="1"/></svg>`,
};

const BTN = {
  edit:  `<svg class="iA" viewBox="0 0 24 24"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 013 3L7 19l-4 1 1-4z"/></svg>`,
  logs:  `<svg class="iA" viewBox="0 0 24 24"><path d="M8 3H6a2 2 0 00-2 2v14a2 2 0 002 2h9a2 2 0 002-2v-2"/><path d="M16 3h4v4"/><path d="M9 8h5M9 12h6M9 16h4"/></svg>`,
  chart: `<svg class="iA" viewBox="0 0 24 24"><path d="M3 20h18"/><path d="M6 16l4-5 3.5 3L20 6"/></svg>`,
  more:  `<svg class="iA" viewBox="0 0 24 24"><circle cx="5" cy="12" r="1.4"/><circle cx="12" cy="12" r="1.4"/><circle cx="19" cy="12" r="1.4"/></svg>`,
  link:  `<svg class="iA" viewBox="0 0 24 24"><path d="M10 13a5 5 0 007.5.5l3-3a5 5 0 00-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 00-7.5-.5l-3 3a5 5 0 007 7L12.2 19"/></svg>`,
};

function card(t, idx) {
  const up = isUp(t);
  const off = !up;
  const tone = stateTone(t);
  const dotCls = tone === 'off' ? 'dot off'
               : (tone === 'warn' || t.kcpLossPercent > 2 ? 'dot wr' : 'dot');
  const label = stateLabel(t);
  const hasPing = t.ping !== undefined && t.ping >= 0;
  const dir = t.direction === 'direct' ? 'Direct' : 'Reverse';
  const carrier = (t.carrier || t.transport || '').toUpperCase();
  // ports arrives as one ", "-joined string, not a list — an empty one is
  // falsy and so used to fall through to a harmless [], which is why only a
  // tunnel that actually forwards a port ever hit this.
  //
  // Just the numbers. "port :8080" said the word and the colon and then the
  // thing, and with three of them it said the colon three times.
  const ports = String(t.ports || '').split(',')
    .map(p => p.trim()).filter(Boolean)
    .map(p => p.split('=')[0]).join(', ');
  const [ip, port] = String(t.addr || '').split(':');

  const series = seriesOf(t);
  const st = statsOf(series.map(p => p.v));
  const view = cardView.get(t.name) || 'curve';
  const per = periodOf(t.name);
  const fmt = fmtOf(t.name);

  return `<div class="c7 ${off ? 'off' : ''} st-${esc(t.state || 'unknown')}" data-name="${esc(t.name)}">
<div class="inner">
${field(t, idx)}
  <div class="hd">
    <div class="fl">${flag(t.peerCountry) || flag(t.country) || '·'}</div>
    <div class="id">
      <b>${esc(t.name)}</b>
      <!-- The address is the fallback, not a dash. A location comes from a
           lookup that can fail — and when it does, the one thing this card can
           always say about the far end is where it is, which is more use than
           a line saying nothing. -->
      <small>${esc([t.peerLocation, t.peerISP].filter(Boolean).join(' · ')
                || (t.addr || '').split(':')[0] || '—')}</small>
    </div>
    ${st ? `<div class="vw" role="group" aria-label="Chart shape">
      <button data-view="curve" class="${view === 'curve' ? 'on' : ''}" title="Line">${VIEW.curve}</button>
      <button data-view="bars"  class="${view === 'bars'  ? 'on' : ''}" title="Bars">${VIEW.bars}</button>
    </div>` : ''}
    <span class="sp2"></span>
    ${trendHTML(st)}
    <span class="stt"><span class="${dotCls}"></span>${label}</span>
  </div>

  <div class="kind">
    <span>${dir}</span><span>${esc(carrier)}</span>
    ${ports ? `<span class="q">${esc(ports)}</span>` : ''}
    ${st ? `<button type="button" class="per" data-per-open aria-haspopup="true"
      title="What the chart covers">${esc(per.label)}${CHEV}</button>` : ''}
  </div>

  <!-- Said in full, on the card, because it is the one failure where every
       other reading on this card is green and correct. -->
  ${serviceDown(t) ? `<div class="nosvc">${esc(t.serviceDown)}</div>` : ''}

  <!-- The headline is the ping: the one figure that says how this tunnel is
       behaving right now. What it has carried is a total, and a total is a
       fact about the past — it goes under, at the size of one. -->
  <div class="headline">${hasPing ? t.ping : '—'}<em>${hasPing ? 'ms' : 'no ping'}</em></div>
  <div class="hcap">${esc(bytes(t.totalBytes || 0))}${t.uptime ? ` · up ${esc(t.uptime)}` : ''}</div>

  <div class="lines">
    <span class="ip">${esc(ip || '—')}</span><span class="sep"></span>
    <span class="pt">port ${esc(port || t.tunnelPort || '—')}</span>
  </div>
</div>

<!-- The bottom band: flush with the card's edge, opaque, and the chart above
     stops where it starts. -->
<div class="mfoot">
  <span class="relaymark">${t.botRelay ? `<span class="relay" title="Bot Relay">${RELAY_SVG}</span>` : ''}</span>
  <span class="sp2"></span>
  ${st ? `<span class="mstats">
      <span><b>${esc(fmt(st.peak))}</b> peak</span><i>·</i>
      <span><b>${esc(fmt(st.low))}</b> low</span><i>·</i>
      <span><b>${esc(fmt(st.avg))}</b> avg</span>
    </span>` : ''}
</div>

<div class="foot">
  <span class="tot"><i>↓</i>${esc(bytes(t.inBytes || 0))}<i>↑</i>${esc(bytes(t.outBytes || 0))}</span>
  <span class="sp2"></span>
  <button class="btn" data-act="edit"   title="Edit">${BTN.edit}</button>
  <button class="btn" data-act="logs"   title="Logs">${BTN.logs}</button>
  <button class="btn" data-act="detail" title="Metrics">${BTN.chart}</button>
  <button class="btn" data-act="link"   title="Link test">${BTN.link}</button>
  <button class="btn" data-act="more"   title="Start, stop, restart, delete">${BTN.more}</button>
</div>

${st ? `<div class="permenu">
  <i>Chart window</i>
  ${PERIODS.map(o => `<button data-per="${o.key}" class="${o.key === per.key ? 'on' : ''}"
    title="${esc(o.note)}">${esc(o.label)}</button>`).join('')}
</div>` : ''}

<div class="card-more">
  <button data-do="start"><span>▶</span>Start</button>
  <button data-do="stop"><span>■</span>Stop</button>
  <button data-do="restart"><span>↻</span>Restart</button>
  <div class="sep2"></div>
  <!-- Offered only while the other end is unknown. Everything the fleet does
       for a tunnel is gated on knowing where that end lives, and a tunnel not
       built through the fleet has never had a way to say. -->
  ${t.node
    ? `<button data-do="unlink"><span>⛓</span>Unlink from ${esc(t.node)}</button>`
    : `<button data-do="adopt"><span>⛓</span>Link to a server…</button>`}
  <div class="sep2"></div>
  <button class="danger" data-do="delete"><span>✕</span>Delete</button>
</div>
</div>`;
}

/* New points into the line that is already there, rather than a new line.
 *
 * Setting d on an existing path moves it; replacing the path element restarts
 * the draw animation attached to it. Only one of those is what a fresh reading
 * means. */
/* The chart moves on every poll and the rest of the card does not, so it is
   redrawn into the element that is already there. Rebuilding the card for it
   would restart the entrance animation four times a minute — the fault the
   signature exists to avoid. */
function updateSpark(el, t, idx) {
  const series = seriesOf(t);
  const svg = el.querySelector('.mfield .mchart');
  if (series.length < 2) { el.querySelector('.mfield')?.remove(); return; }
  if (!svg) return;
  const id = svg.querySelector('linearGradient')?.id || ('L' + idx);
  const defs = svg.querySelector('defs');
  svg.innerHTML = (defs ? defs.outerHTML : '') +
    chartSVG(series, cardView.get(t.name) || 'curve', id);
  paintTrend(el, t);
}

/* The footer figures come from the same window as the chart, so they are
   written whenever it is.
 *
 * The card's colour is its state and nothing else — green connected, red
 * offline, plain white stopped or still trying. It used to be the direction of
 * the traffic, which meant the same green stood for "up" on a tunnel that was
 * down; one colour has to mean one thing. */
function paintTrend(el, t) {
  el.classList.remove('st-online', 'st-offline', 'st-stopped', 'st-unknown');
  el.classList.add('st-' + (serviceDown(t) ? 'offline' : (t.state || 'unknown')));
  const st = statsOf(seriesOf(t).map(p => p.v));
  if (!st) return;
  const fmt = fmtOf(t.name);
  const cells = el.querySelectorAll('.mstats b');
  [st.peak, st.low, st.avg].forEach((v, i) => { if (cells[i]) cells[i].textContent = fmt(v); });

  /* The trend moves with the chart, so it is written here rather than left to
     the next rebuild — a percentage describing a window the card has already
     stopped drawing is worse than none. */
  const tr = el.querySelector('.trend');
  if (tr) {
    const flat = Math.abs(st.pct) < NEUTRAL_PCT;
    tr.innerHTML = (flat ? TREND.flat : st.net >= 0 ? TREND.up : TREND.down) +
                   Math.abs(st.pct).toFixed(1) + '%';
  }
  const lbl = el.querySelector('[data-per-open]');
  if (lbl) {
    const p = periodOf(t.name);
    lbl.innerHTML = esc(p.label) + (periodReady(t) ? '' : ' ·') + CHEV;
  }
}

const EMPTY = `<div class="emptybox">
  <b>No tunnels yet</b>
  <span>Add one with the button above, or from the CLI menu (<code>sudo backpack</code>).</span>
</div>`;

const ACTION_DONE = {
  start: 'Tunnel started.', stop: 'Tunnel stopped.',
  restart: 'Tunnel restarted.', delete: 'Tunnel deleted.',
};

/* What a card draws, minus the chart.
 *
 * The chart is left out on purpose. Its points change on every poll, so a
 * signature that included them would never match and the card would be rebuilt
 * every few seconds — which is the fault this exists to stop. The line is
 * updated in place instead; see paint.
 */
function cardSig(t) {
  return JSON.stringify([
    t.state, t.serviceDown, t.role, t.direction, t.transport, t.carrier, t.addr, t.ports,
    t.ping, t.uptime, t.bytesIn, t.bytesOut, t.bytesTotal, t.country, t.peerCountry,
    t.peerLocation, t.peerISP, t.botRelay, t.botRelayPort, t.tunnelPort,
    t.kcpLossPercent, t.pool, t.preset, t.certType, t.certDomain, t.certExpiry,
    t.maxConnections, t.bandwidthMbps,
  ]);
}

export function dashboard(ctx) {
  const view = $('#view');

  view.innerHTML = `
    <div class="sech2">
      <h2>Tunnels</h2>
      <span class="cnt" id="tCount">0</span>
      <span class="sp"></span>
      <button class="sb" data-act="restartall">Restart all</button>
      <button class="sb primary" data-act="add">Add tunnel</button>
    </div>
    <div class="grid3" id="tGrid"></div>`;

  const grid = $('#tGrid', view);
  const held = new Map();   // name -> { el, sig }

  /* Painting without rebuilding.
   *
   * The obvious paint writes the whole grid on every poll — and every four
   * seconds that destroyed every card and made it again, which restarted the
   * draw animation on each chart. That is the white line people see sweeping
   * across the metrics: not a refresh, the same one-shot animation running over
   * and over because the element it belongs to keeps being a new element.
   *
   * So a card is rebuilt only when something it draws has changed, and the
   * chart — the one part that changes on every poll — is updated in place. An
   * animation that has already finished stays finished when its path is given
   * new points.
   */
  const paint = state => {
    const tuns = state.tunnels || [];
    $('#tCount', view).textContent = String(tuns.length);

    if (!tuns.length) {
      held.clear();
      if (!grid.querySelector('.emptybox')) grid.innerHTML = EMPTY;
      return;
    }
    if (grid.querySelector('.emptybox')) grid.innerHTML = '';

    const want = new Set(tuns.map(t => t.name));
    for (const [name, h] of held) {
      if (!want.has(name)) { h.el.remove(); held.delete(name); }
    }

    let at = null;
    tuns.forEach((t, i) => {
      const sig = cardSig(t);
      let h = held.get(t.name);
      if (!h || h.sig !== sig) {
        const box = document.createElement('div');
        box.innerHTML = card(t, i);
        const fresh = box.firstElementChild;
        if (h) h.el.replaceWith(fresh); else if (at) at.after(fresh); else grid.prepend(fresh);
        h = { el: fresh, sig };
        held.set(t.name, h);
      } else {
        if (at ? h.el.previousElementSibling !== at : grid.firstElementChild !== h.el) {
          if (at) at.after(h.el); else grid.prepend(h.el);
        }
        updateSpark(h.el, t, i);
      }
      at = h.el;
    });
  };

  const unsub = store.subscribe(paint);

  /* The chart's shape is a per-card choice and is remembered, so a poll a
     second later does not put it back. */
  const offView = delegate(view, 'click', '[data-view]', (ev, btn) => {
    ev.stopPropagation();
    const card = btn.closest('.c7');
    const name = card?.dataset.name;
    if (!name) return;
    cardView.set(name, btn.dataset.view);
    card.querySelectorAll('[data-view]').forEach(b => b.classList.toggle('on', b === btn));
    const t = store.tunnel(name);
    if (t) updateSpark(card, t, 0);
  });

  /* The window the chart covers, on the same terms as its shape: remembered
     per card, and the menu closes whatever else was open. */
  const offPerOpen = delegate(view, 'click', '[data-per-open]', (ev, btn) => {
    ev.stopPropagation();
    const card = btn.closest('.c7');
    const menu = card?.querySelector('.permenu');
    if (!menu) return;
    const was = menu.classList.contains('on');
    view.querySelectorAll('.permenu.on,.card-more.on').forEach(m => m.classList.remove('on'));
    menu.classList.toggle('on', !was);
  });

  const offPerPick = delegate(view, 'click', '[data-per]', async (ev, btn) => {
    ev.stopPropagation();
    const card = btn.closest('.c7');
    const name = card?.dataset.name;
    const key = btn.dataset.per;
    if (!name) return;

    cardPeriod.set(name, key);
    card.querySelector('.permenu')?.classList.remove('on');
    card.querySelectorAll('[data-per]').forEach(b => b.classList.toggle('on', b === btn));

    /* Drawn from what is already held first, so the card answers the click at
       once, and again when the history lands. A window that turns out to hold
       nothing keeps the live line rather than emptying the card. */
    const before = store.tunnel(name);
    if (before) updateSpark(card, before, 0);
    try { await loadPeriod(name, key); } catch (e) { oops(e); }
    const after = store.tunnel(name);
    if (after && card.isConnected) updateSpark(card, after, 0);
  });

  const offAct = delegate(view, 'click', '[data-act]', async (ev, btn) => {
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
      /* The link test measures the path to the far server and says what
         transport suits what it finds. It was reachable only from the metrics
         screen, two clicks in, which is one more than a thing you run when a
         tunnel feels slow should take. */
      case 'link':   go(`/t/${encodeURIComponent(name)}/link`); break;
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

  const offDo = delegate(view, 'click', '[data-do]', async (ev, btn) => {
    const cardEl = btn.closest('.c7');
    const name = cardEl.dataset.name;
    const action = btn.dataset.do;
    cardEl.querySelector('.card-more').classList.remove('on');

    if (action === 'adopt') { await linkToServer(name); store.refresh(); return; }
    if (action === 'unlink') {
      const t = store.tunnel(name);
      const ok = await confirmBox({
        title: `Unlink <q>${esc(name)}</q> from ${esc(t && t.node || '')}?`,
        body: 'Nothing is deleted or stopped on either machine. This panel simply '
            + 'stops treating them as two ends of one tunnel, so edits, start and stop, '
            + 'and the far journal go back to reaching this end only.',
        go: 'Unlink',
      });
      if (!ok) return;
      try { await api.unlinkTunnel(name); toast('Unlinked.'); } catch (e) { oops(e); }
      store.refresh();
      return;
    }

    const extra = {};
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

      /* The far end is a second machine and therefore a second question.
         Asked only when there is one, asked after this end is settled, and
         answered "leave it" by every reflex — Esc, Enter and the button under
         the cursor — because saying nothing must not delete something
         somewhere else. */
      const t = store.tunnel(name);
      if (t && t.node) {
        const far = t.peerName || name;
        const alsoFar = await confirmBox({
          title: `Also delete <q>${esc(far)}</q> on ${esc(t.node)}?`,
          body: `${name} is gone from this server either way. This is about the other `
              + `half of it, on a different machine. There is no undo for that one either.`,
          lines: [{ text: `${t.node}: ${far}` }],
          go: 'Delete there too', danger: true, defaultNo: true,
        });
        if (alsoFar) extra.alsoFarEnd = '1';
      }
    }
    cardEl.style.opacity = '.5';
    try {
      const r = await api.tunnelAction(name, action, extra);
      /* A tunnel built across a managed server is one tunnel in two places, and
         the button reaches both. When only one end moved, that is the thing
         worth saying — "Tunnel stopped" over a far end still dialling is the
         message that costs somebody an afternoon. */
      if (r && r.status === 'partial') {
        toast(r.peerHint || r.peerError || 'Only this end changed.', true);
      } else {
        /* The server says what actually happened when it is not simply "both
           ends". A delete crosses only when it was asked to, so what happened
           to the far end is the server's to report, not something to append
           here — saying "Both ends" over a far end still running is the message
           that costs somebody an afternoon. */
        toast(r && r.note
          ? `${ACTION_DONE[action] || 'Done.'} ${r.note}`
          : (ACTION_DONE[action] || 'Done.')
            + (r && r.node ? ` Both ends, here and on ${r.node}.` : ''));
      }
    } catch (e) { oops(e); }
    cardEl.style.opacity = '';
    store.refresh();
  });

  const closeSheets = () =>
    view.querySelectorAll('.card-more.on,.permenu.on').forEach(s => s.classList.remove('on'));
  document.addEventListener('click', closeSheets);

  ctx.setTeardown(() => {
    unsub();
    offView();
    offPerOpen();
    offPerPick();
    offAct();
    offDo();
    document.removeEventListener('click', closeSheets);
  });
}

/* The refusal a measurement gives when it does not know where the other end is
   carries a button, and this is what the button does. Registered here because
   this is the screen that knows how to link a tunnel; toast.js only has to know
   that something does. */
onFix.link = name => { if (name) linkToServer(name).then(() => store.refresh()); };

/* Linking a tunnel to the server that holds its other end.
 *
 * Two steps, because they are two different questions. Which server is the
 * operator's to answer and the panel cannot guess it. Which tunnel on that
 * server the panel can often demonstrate — a reverse client aimed at this
 * machine's address, on this tunnel's port, is this tunnel's other half and
 * nothing about the names has to agree — so it says so, and still asks.
 *
 * It never links on its own. A pairing decides where the next edit is sent,
 * and a wrong one sends it to a tunnel somebody else is using.
 */
async function linkToServer(name) {
  /* Asked for here rather than read from the store: the store carries what the
     tunnels screen polls, and the fleet is not part of that. Reading it from
     there returned undefined every time, which reads as an empty fleet — so the
     one thing this screen needs the fleet for said there was none. */
  let servers = [];
  try {
    servers = ((await api.nodes()).nodes || []).map(n => n.name);
  } catch (e) { oops(e); return; }
  if (!servers.length) {
    toast('No servers in the fleet yet — add one on Servers first.', true);
    return;
  }

  const server = servers.length === 1 ? servers[0] : await pickOne(
    'Which server holds the other end?', servers.map(s => ({ value: s, label: s })));
  if (!server) return;

  let list;
  try {
    list = (await api.adoptCandidates(name, server)).tunnels || [];
  } catch (e) { oops(e); return; }

  if (!list.length) {
    toast(`${server} has no tunnels on it.`, true);
    return;
  }
  /* The demonstrated ones first, and labelled with what the match rests on, so
     the operator confirming can see whether it was shown or merely guessed. */
  list.sort((a, b) => (b.certain ? 1 : 0) - (a.certain ? 1 : 0));
  const peer = await pickOne(
    `Which tunnel on ${server} is the other end of ${name}?`,
    list.map(t => ({
      value: t.name,
      label: t.name + (t.role ? ` · ${t.role}` : ''),
      note: t.why || 'no shared address — pick it only if you know',
      strong: !!t.certain,
    })));
  if (!peer) return;

  try {
    await api.adoptTunnel(name, server, peer);
    toast(`${name} is linked to ${peer} on ${server}.`);
  } catch (e) { oops(e); }
}

/* A one-of-many chooser, built on the confirm box's scrim so it looks like the
   rest of the panel's questions rather than a browser prompt. */
function pickOne(title, options) {
  return new Promise(resolve => {
    const scrim = document.createElement('div');
    scrim.className = 'cfZ pickZ';
    scrim.innerHTML = `<div class="veilZ"></div><div class="boxZ">
      <h2>${esc(title)}</h2>
      <div class="pickL">${options.map(o => `
        <button class="pickI${o.strong ? ' strong' : ''}" data-v="${esc(o.value)}">
          <b>${esc(o.label)}</b>${o.note ? `<span>${esc(o.note)}</span>` : ''}
        </button>`).join('')}</div>
      <div class="actZ"><button class="pickX">Cancel</button></div>
    </div>`;
    document.body.append(scrim);
    requestAnimationFrame(() => scrim.classList.add('on'));

    const done = v => {
      document.removeEventListener('keydown', key);
      scrim.classList.remove('on');
      setTimeout(() => scrim.remove(), 280);
      resolve(v);
    };
    const key = ev => { if (ev.key === 'Escape') done(null); };
    document.addEventListener('keydown', key);
    scrim.addEventListener('click', ev => {
      if (ev.target === scrim) return done(null);
      const b = ev.target.closest('.pickI');
      if (b) return done(b.dataset.v);
      if (ev.target.closest('.pickX')) done(null);
    });
  });
}

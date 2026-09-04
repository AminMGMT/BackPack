/* The dashboard: the server strip, then the fleet.
 *
 * The markup is the approved preview's, class for class — .RZ tiles and .c7
 * cards — so the design is the one that was signed off rather than a redraw of
 * it. What changed is only where the numbers come from.
 *
 * CLI: Manage → Status, and Manage → Manage Tunnels.
 */

import { $, delegate, esc } from '../lib/dom.js';
import { isUp, stateLabel, stateTone } from '../lib/tstate.js';
import { bytes, speed, kindLabel, flag } from '../lib/format.js';
import * as store from '../store.js';
import * as api from '../api.js';
import { toast, oops } from '../ui/toast.js';
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

/* The rate series a card draws: what the tunnel was carrying at each sample. */
function seriesOf(t) {
  return (t.rates || []).slice(-24).map(p => ({ t: p.t, v: (p.in || 0) + (p.out || 0) }));
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
  const id = 'L' + idx, gid = 'G' + idx;
  const view = cardView.get(t.name) || 'curve';
  return `<div class="mfield" style="width:${REGION_W}%">
    <div class="mwash"></div>
    <div class="mgrid"><svg aria-hidden="true"><defs>
      <pattern id="${gid}" width="14" height="14" patternUnits="userSpaceOnUse">
        <circle cx="1" cy="1" r="1" fill="currentColor"/></pattern></defs>
      <rect width="100%" height="100%" fill="url(#${gid})"/></svg></div>
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
  speed: `<svg class="iA" viewBox="0 0 24 24"><path d="M12 20a8 8 0 118-8"/><path d="M12 12l5-3"/><circle cx="12" cy="12" r="1.3"/></svg>`,
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

  return `<div class="c7 ${off ? 'off' : ''} st-${esc(t.state || 'unknown')}" data-name="${esc(t.name)}">
<div class="inner">
${field(t, idx)}
  <div class="hd">
    <div class="fl">${flag(t.peerCountry) || flag(t.country) || '·'}</div>
    <div class="id">
      <b>${esc(t.name)}</b>
      <small>${esc([t.peerLocation, t.peerISP].filter(Boolean).join(' · ') || '—')}</small>
    </div>
    ${st ? `<div class="vw" role="group" aria-label="Chart shape">
      <button data-view="curve" class="${view === 'curve' ? 'on' : ''}" title="Line">${VIEW.curve}</button>
      <button data-view="bars"  class="${view === 'bars'  ? 'on' : ''}" title="Bars">${VIEW.bars}</button>
    </div>` : ''}
    <span class="sp2"></span>
    <span class="stt"><span class="${dotCls}"></span>${label}</span>
  </div>

  <div class="kind">
    <span>${dir}</span><span>${esc(carrier)}</span>
    ${ports ? `<span class="q">${esc(ports)}</span>` : ''}
  </div>

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
      <span><b>${esc(speed(st.peak))}</b> peak</span><i>·</i>
      <span><b>${esc(speed(st.low))}</b> low</span><i>·</i>
      <span><b>${esc(speed(st.avg))}</b> avg</span>
    </span>` : ''}
</div>

<div class="foot">
  <span class="tot"><i>↓</i>${esc(bytes(t.inBytes || 0))}<i>↑</i>${esc(bytes(t.outBytes || 0))}</span>
  <span class="sp2"></span>
  <button class="btn" data-act="edit"   title="Edit">${BTN.edit}</button>
  <button class="btn" data-act="logs"   title="Logs">${BTN.logs}</button>
  <button class="btn" data-act="detail" title="Metrics">${BTN.chart}</button>
  <button class="btn" data-act="link"   title="Link test">${BTN.link}</button>
  <button class="btn" data-act="speed"  title="Speed test">${BTN.speed}</button>
  <button class="btn" data-act="more"   title="Start, stop, restart, delete">${BTN.more}</button>
</div>

<div class="card-more">
  <button data-do="start"><span>▶</span>Start</button>
  <button data-do="stop"><span>■</span>Stop</button>
  <button data-do="restart"><span>↻</span>Restart</button>
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
  el.classList.add('st-' + (t.state || 'unknown'));
  const st = statsOf(seriesOf(t).map(p => p.v));
  if (!st) return;
  const cells = el.querySelectorAll('.mstats b');
  [st.peak, st.low, st.avg].forEach((v, i) => { if (cells[i]) cells[i].textContent = speed(v); });
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
    t.state, t.role, t.direction, t.transport, t.carrier, t.addr, t.ports,
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
      case 'speed':  go(`/t/${encodeURIComponent(name)}/speed`); break;
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
    try {
      const r = await api.tunnelAction(name, action);
      /* A tunnel built across a managed server is one tunnel in two places, and
         the button reaches both. When only one end moved, that is the thing
         worth saying — "Tunnel stopped" over a far end still dialling is the
         message that costs somebody an afternoon. */
      if (r && r.status === 'partial') {
        toast(r.peerHint || r.peerError || 'Only this end changed.', true);
      } else {
        /* The server says what actually happened when it is not simply "both
           ends": a delete is the one action that does not cross, because a node
           has no operation that removes a tunnel. Appending "Both ends" to it
           told the operator the far end was gone when it is still there. */
        toast(r && r.note
          ? `${ACTION_DONE[action] || 'Done.'} ${r.note}`
          : (ACTION_DONE[action] || 'Done.')
            + (r && r.node ? ` Both ends, here and on ${r.node}.` : ''));
      }
    } catch (e) { oops(e); }
    cardEl.style.opacity = '';
    store.refresh();
  });

  const closeSheets = () => view.querySelectorAll('.card-more.on').forEach(s => s.classList.remove('on'));
  document.addEventListener('click', closeSheets);

  ctx.setTeardown(() => {
    unsub();
    offView();
    offAct();
    offDo();
    document.removeEventListener('click', closeSheets);
  });
}

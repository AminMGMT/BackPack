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
  const ports = String(t.ports || '').split(',')
    .map(p => p.trim()).filter(Boolean)
    .map(p => ':' + p.split('=')[0]).join(', ');
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
    <span class="t"><i>↓</i><b>${bytes(t.inBytes || 0)}</b><em>total</em></span>
    <span class="t"><i>↑</i><b>${bytes(t.outBytes || 0)}</b><em>total</em></span>
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
    <button class="btn" data-act="link"   title="Link test">${BTN.link}</button>
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

/* New points into the line that is already there, rather than a new line.
 *
 * Setting d on an existing path moves it; replacing the path element restarts
 * the draw animation attached to it. Only one of those is what a fresh reading
 * means. */
function updateSpark(el, t, idx) {
  const p = sparkPaths(t.rates);
  const svg = el.querySelector('.field svg');
  if (!p) { el.querySelector('.field')?.remove(); return; }
  if (!svg) return;
  const line = svg.querySelector('path.sparkpath');
  const area = svg.querySelector('path:not(.sparkpath)');
  if (line) line.setAttribute('d', p.line);
  if (area) area.setAttribute('d', p.area);
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
    t.ping, t.uptime, t.bytesIn, t.bytesOut, t.country, t.peerCountry,
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
    offAct();
    offDo();
    document.removeEventListener('click', closeSheets);
  });
}

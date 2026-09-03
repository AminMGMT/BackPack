/* Speedtest — one screen, one question: how fast is this tunnel.
 *
 * It measures the tunnel, not the internet. That distinction is the whole
 * reason this is here rather than a link to speedtest.net: a public speed test
 * measures the server's own uplink to somebody else's machine, and says nothing
 * about the path a tunnel actually takes. The only way to know what a tunnel
 * carries is to push bytes through it, which is what the engine does.
 *
 * The dial is logarithmic, and that is a decision rather than a decoration. A
 * linear 0–1000 face puts 40 Mb/s and 90 Mb/s within a few degrees of each
 * other while spending half the arc on speeds no tunnel here will reach. These
 * tunnels run from a few Mb/s on a bad evening to several Gb/s on a local link,
 * so the scale is a decade per quarter turn and every decade gets the same
 * room.
 */

import { $, el, esc } from '../lib/dom.js';
import { isUp } from '../lib/tstate.js';
import { bytes } from '../lib/format.js';
import * as store from '../store.js';
import * as api from '../api.js';
import { toast, oops } from '../ui/toast.js';

/* ---- the dial ------------------------------------------------------------ */
const MIN = 1, MAX = 10000;          // Mb/s, four decades
const SWEEP = 270, START = -135;     // degrees
const R = 118, CX = 150, CY = 150;

const frac = mbps => {
  const v = Math.max(MIN, Math.min(MAX, Number(mbps) || MIN));
  return Math.log10(v / MIN) / Math.log10(MAX / MIN);
};
const angle = f => START + SWEEP * Math.max(0, Math.min(1, f));
const point = (deg, r) => {
  const rad = (deg - 90) * Math.PI / 180;
  return [CX + r * Math.cos(rad), CY + r * Math.sin(rad)];
};

/* The face, drawn once. Ticks at every decade and at the 2× and 5× between
   them, because those are the numbers links are actually sold in. */
function face() {
  const marks = [];
  for (let d = 0; d < 4; d++) {
    for (const m of [1, 2, 5]) {
      const v = MIN * Math.pow(10, d) * m;
      if (v > MAX) break;
      const major = m === 1;
      const a = angle(frac(v));
      const [x1, y1] = point(a, R - (major ? 13 : 7));
      const [x2, y2] = point(a, R - 1);
      marks.push(`<line class="tk${major ? ' maj' : ''}" x1="${x1.toFixed(1)}" y1="${y1.toFixed(1)}"
        x2="${x2.toFixed(1)}" y2="${y2.toFixed(1)}"/>`);
      if (major) {
        const [lx, ly] = point(a, R - 27);
        marks.push(`<text class="tl" x="${lx.toFixed(1)}" y="${(ly + 3.5).toFixed(1)}">${
          v >= 1000 ? (v / 1000) + 'G' : v + 'M'}</text>`);
      }
    }
  }
  const [ax, ay] = point(START, R);
  const [bx, by] = point(START + SWEEP, R);
  const arc = `M${ax.toFixed(1)} ${ay.toFixed(1)} A${R} ${R} 0 1 1 ${bx.toFixed(1)} ${by.toFixed(1)}`;
  const len = 2 * Math.PI * R * (SWEEP / 360);
  return `<svg class="dial" viewBox="0 0 300 300" aria-hidden="true">
    <path class="trk" d="${arc}"/>
    <path class="prog" id="stArc" d="${arc}"
      stroke-dasharray="${len.toFixed(1)}" stroke-dashoffset="${len.toFixed(1)}"/>
    ${marks.join('')}
  </svg>`;
}
const ARCLEN = 2 * Math.PI * R * (SWEEP / 360);

const rate = mbps => (mbps >= 1000
  ? (mbps / 1000).toFixed(2) + ' Gb/s'
  : mbps.toFixed(mbps < 100 ? 1 : 0) + ' Mb/s');

export function speedtestView(ctx) {
  const view = $('#view');
  let running = false, plan = null, port = 0, tunnel = '';
  let loadSeq = 0;   // the read in flight; an older one stops painting

  view.innerHTML = `
    <div class="st">
      <div class="sech2">
        <h2>Speedtest</h2>
        <span class="sp"></span>
        <select id="stPick" aria-label="Tunnel"></select>
      </div>

      <div class="st-stage">
        <div class="st-dialwrap">
          ${face()}
          <div class="st-read">
            <div class="st-num" id="stNum">—</div>
            <div class="st-unit" id="stUnit">Mb/s</div>
            <button class="st-go" id="stGo">GO</button>
          </div>
        </div>

        <div class="st-side">
          <div class="st-what" id="stWhat">Pick a tunnel.</div>
          <div class="st-maps" id="stMaps"></div>
          <!-- Space is held for the result so the page does not jump when it
               arrives; empty tiles read as "not measured yet", which is true. -->
          <div class="st-tiles">
            <div class="st-tile"><span>Throughput</span><b id="tThru">—</b></div>
            <div class="st-tile"><span>Moved</span><b id="tMoved">—</b></div>
            <div class="st-tile"><span>Took</span><b id="tTook">—</b></div>
            <div class="st-tile"><span>Receiver</span><b id="tRecv">—</b></div>
          </div>
          <div class="st-note" id="stNote"></div>
        </div>
      </div>
    </div>`;

  const pick = $('#stPick', view), go = $('#stGo', view);
  const num = $('#stNum', view), unit = $('#stUnit', view);
  const arc = $('#stArc', view);
  const what = $('#stWhat', view), maps = $('#stMaps', view), note = $('#stNote', view);
  const tile = id => $('#' + id, view);

  /* The dial is driven from here rather than by a CSS transition.
   *
   * stroke-dashoffset and transform are SVG presentation attributes, and
   * transitioning them through CSS puts the rendered value and the value the
   * code set out of step — which is a bad property for the one number this page
   * exists to show. Tweened explicitly, the attribute is always the truth, the
   * end state is set outright rather than approached, and there is one place to
   * turn the motion off. */
  const REDUCED = matchMedia('(prefers-reduced-motion: reduce)').matches;
  let tween = 0, settle = 0, shown = 0;

  const put = f => arc.setAttribute('stroke-dashoffset', (ARCLEN * (1 - f)).toFixed(1));

  const paintDial = (mbps, live) => {
    const to = mbps === null ? 0 : frac(mbps);
    const from = shown, dur = 900;
    arc.classList.toggle('live', !!live);
    cancelAnimationFrame(tween);
    clearTimeout(settle);
    if (REDUCED) { shown = to; put(to); return; }

    /* The end state does not depend on the animation running.
     *
     * requestAnimationFrame is throttled to nothing in a background tab, and
     * stops entirely in some headless runs — so a dial tweened only by frames
     * can be left sitting at zero while the reading beside it says 300 Mb/s.
     * The tween is the flourish; this is the guarantee. */
    settle = setTimeout(() => { shown = to; put(to); }, dur + 80);
    let t0 = null;
    const step = now => {
      if (t0 === null) t0 = now;
      const p = Math.max(0, Math.min(1, (now - t0) / dur));
      const e = 1 - Math.pow(1 - p, 3);
      shown = from + (to - from) * e;
      put(shown);
      if (p < 1) tween = requestAnimationFrame(step);
      else { shown = to; put(to); }
    };
    tween = requestAnimationFrame(step);
  };
  put(0);
  let unsub2 = () => {};
  ctx.setTeardown(() => { cancelAnimationFrame(tween); clearTimeout(settle); unsub2(); });

  /* ---- which tunnel -------------------------------------------------------
     Only the ones that are running: a stopped tunnel has nothing to measure,
     and offering it turns a choice into an error at the end. */
  const fill = state => {
    const live = (state.tunnels || []).filter(isUp);
    const had = pick.value;
    pick.innerHTML = live.length
      ? live.map(t => `<option value="${esc(t.name)}">${esc(t.name)}</option>`).join('')
      : '<option value="">no tunnel is running</option>';
    go.disabled = !live.length;
    if (live.some(t => t.name === had)) pick.value = had;
    if (pick.value !== tunnel) load();
  };
  unsub2 = store.subscribe(fill);
  fill(store.get());

  /* ---- what can be measured ---------------------------------------------- */
  async function load() {
    /* The store's first paint and the view's own call both arrive, and a plan
       that comes back second used to append its mappings under the first one's
       — the same two ports listed twice. Only the newest read paints. */
    const seq = ++loadSeq;
    tunnel = pick.value;
    maps.innerHTML = '';
    note.textContent = '';
    if (!tunnel) { what.textContent = 'Nothing is running to measure.'; return; }
    what.textContent = 'Reading what this tunnel can measure…';
    try {
      const got = await api.speedPlan(tunnel);
      if (seq !== loadSeq) return;
      plan = got;
    } catch (e) {
      if (seq !== loadSeq) return;
      what.textContent = e.message || 'Could not work out what to measure.';
      go.disabled = true;
      return;
    }
    if (plan.blocked) {
      what.textContent = plan.blocked;
      go.disabled = true;
      return;
    }
    go.disabled = false;
    const targets = plan.targets || [];
    const choose = t => {
      port = t ? t.listenPort : (plan.port || 0);
      what.innerHTML = t
        ? `Through <b>port ${t.listenPort}</b>, about ${plan.seconds} seconds. `
          + `The backend on that port is unavailable while it runs.`
        : `To <b>${esc(plan.peer || 'the other end')}</b> on port ${plan.port}, `
          + `about ${plan.seconds} seconds.`;
    };
    if (targets.length > 1) {
      maps.innerHTML = '';
      targets.forEach((t, i) => {
        const usable = !t.reason;
        const b = el('button', {
          class: 'st-map' + (usable && i === 0 ? ' on' : ''),
          title: t.reason || `backend port ${t.backendPort}`,
        }, [el('b', { text: 'Port ' + t.listenPort })]);
        b.disabled = !usable;
        if (usable) {
          b.addEventListener('click', () => {
            maps.querySelectorAll('.st-map').forEach(x => x.classList.toggle('on', x === b));
            choose(t);
          });
        }
        maps.append(b);
      });
    }
    choose(targets.find(t => !t.reason) || targets[0] || null);
  }
  pick.addEventListener('change', load);

  /* ---- the run ------------------------------------------------------------
     The server measures in one call and answers at the end, so there is no
     stream of readings to draw. The dial says "working" rather than pretending
     to show a rate — a needle sweeping to a number it has not been told yet is
     a lie the operator would have no way to catch. The seconds are real: they
     come from the plan. */
  go.addEventListener('click', async () => {
    if (running || !tunnel) return;
    running = true;
    go.disabled = true;
    go.textContent = '…';
    view.querySelector('.st').classList.add('busy');
    ['tThru', 'tMoved', 'tTook', 'tRecv'].forEach(id => { tile(id).textContent = '—'; });

    const total = (plan?.seconds || 10);
    const t0 = Date.now();
    num.textContent = '0';
    unit.textContent = 's';
    paintDial(null, true);
    const tick = setInterval(() => {
      const s = (Date.now() - t0) / 1000;
      num.textContent = s.toFixed(0);
      note.textContent = `Measuring — ${Math.max(0, total - s).toFixed(0)}s to go.`;
    }, 200);

    try {
      const r = await api.speedRun({ name: tunnel, port });
      clearInterval(tick);
      const mbps = Number(r.mbps) || 0;
      unit.textContent = mbps >= 1000 ? 'Gb/s' : 'Mb/s';
      num.textContent = mbps >= 1000 ? (mbps / 1000).toFixed(2) : mbps.toFixed(mbps < 100 ? 1 : 0);
      paintDial(mbps);
      tile('tThru').textContent = rate(mbps);
      tile('tMoved').textContent = bytes(r.bytes || 0);
      tile('tTook').textContent = (Number(r.seconds) || 0).toFixed(1) + 's';
      tile('tRecv').textContent = r.receiver || (r.receiverError ? 'could not start' : 'already running');
      /* Where the sink could not be started, the number is not wrong but it is
         not clean either: the bytes still crossed the tunnel, and what read
         them at the far end was the real service rather than a sink. Said
         plainly, next to the figure it qualifies. */
      note.textContent = r.receiverError
        ? `The receiver could not be started on the other server (${r.receiverError}). `
          + `The traffic went to the real backend instead, so this is a floor rather `
          + `than the tunnel's capacity.`
        : r.receiver
          ? `The receiver was started on ${r.receiver} for this run and closed itself after it.`
          : '';
      note.classList.toggle('warn', !!r.receiverError);
    } catch (e) {
      clearInterval(tick);
      paintDial(null);
      num.textContent = '—';
      unit.textContent = '';
      note.textContent = e.message || 'The measurement did not run.';
      toast(e.message || 'The measurement did not run.', true);
    }
    view.querySelector('.st').classList.remove('busy');
    go.textContent = 'GO';
    go.disabled = false;
    running = false;
  });

  return load().catch(oops);
}

/* Alerts, health check and speed test — three screens that were designed
 * together and share one stylesheet.
 *
 * CLI: Manage → Health Check, Manage → Speed Test, and the alert history.
 */

import { $$, esc } from '../lib/dom.js';
import { ago } from '../lib/format.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { oops, toast } from '../ui/toast.js';

/* ---- alerts -------------------------------------------------------------- */
export function alertsView(ctx) {
  openScreen('monitor', {
    pick: '.dlg.al2',
    bind: async (root, close) => {
      let data;
      try { data = await api.alerts(); } catch (e) { return oops(e); }

      const active = root.querySelector('.active');
      const list = data.active || [];
      if (active) {
        active.innerHTML = list.length
          ? list.map(a => `<div class="arow"><span class="adot"></span><span>${esc(a)}</span></div>`).join('')
          : `<div class="arow quiet"><span>Nothing is firing right now.</span></div>`;
      }

      const feed = root.querySelector('.evs');
      if (feed) {
        feed.innerHTML = (data.events || []).map(e =>
          `<div class="erow"><span class="et">${esc(ago(e.time))}</span>` +
          `<span class="em">${esc(e.message)}</span></div>`).join('')
          || `<div class="erow"><span class="em">No events recorded yet.</span></div>`;
      }
      ctx.setTeardown(close);
    },
  }).catch(oops);
}

/* ---- health check --------------------------------------------------------
   Grouped exactly as Diagnose returns them: System, Monitor, Web Panel, then
   one group per tunnel. A check with a Fix carries it; one without does not. */
export function healthView(ctx) {
  openScreen('monitor', {
    pick: '.dlg.hc2',
    bind: async (root, close) => {
      /* .body4 is the scrolling area under the header. Falling back to the
         dialog itself would take the header and the close button with it. */
      const body = root.querySelector('.body4');
      if (!body) return;
      /* The whole read-and-render, so "Run again" can do exactly what opening
         the screen does — rather than reopening the screen, which fights the
         dialog's own lifecycle and left it closing itself. */
      const load = async () => {
        body.innerHTML = `<div class="hcrun">Running the checks…</div>`;

        let checks;
        try { checks = await api.health(); } catch (e) { return oops(e); }

        const tally = root.querySelector('.tally');
        const groups = new Map();
        for (const c of checks) {
          const g = c.Group || c.group || 'System';
          if (!groups.has(g)) groups.set(g, []);
          groups.get(g).push(c);
        }
        const lvl = c => (c.Level || c.level || 'ok').toLowerCase();
        const counts = { ok: 0, warn: 0, error: 0 };
        checks.forEach(c => { counts[lvl(c)] = (counts[lvl(c)] || 0) + 1; });

        /* The header already has a place for the tally; the pills go there
           rather than being repeated at the top of the list. */
        if (tally) {
          tally.innerHTML =
            `<span class="hcpill ok">${counts.ok} passed</span>` +
            `<span class="hcpill wr">${counts.warn || 0} to look at</span>` +
            `<span class="hcpill er">${counts.error || 0} broken</span>`;
        }
        body.innerHTML =
          (tally ? '' : `<div class="hcsum">
            <span class="hcpill ok">${counts.ok} passed</span>
            <span class="hcpill wr">${counts.warn || 0} to look at</span>
            <span class="hcpill er">${counts.error || 0} broken</span>
          </div>`) +
          [...groups].map(([g, list]) => `
            <div class="hcgroup">
              <div class="hcg">${esc(g)}</div>
              ${list.map(c => `
                <div class="hcitem ${lvl(c)}">
                  <span class="hcdot"></span>
                  <div class="hctx">
                    <b>${esc(c.Name || c.name)}</b>
                    <span>${esc(c.Detail || c.detail || '')}</span>
                    ${(c.Fix || c.fix) ? `<i class="hcfix">${esc(c.Fix || c.fix)}</i>` : ''}
                  </div>
                </div>`).join('')}
            </div>`).join('');
        /* The footer counted the preview's own example run. */
        const foot = root.querySelector('.df .note') || root.querySelector('.df span');
        if (foot) {
          const n = checks.length;
          foot.textContent = `${n} check${n === 1 ? '' : 's'} · ` +
            `${counts.warn || 0} warning${counts.warn === 1 ? '' : 's'} · ` +
            `${counts.error || 0} failure${counts.error === 1 ? '' : 's'}`;
        }
        /* Diagnose runs on the server; asking again is just asking again. */
        const again = [...root.querySelectorAll('.df button, .bt')]
          .find(b => /run again/i.test(b.textContent));
        again?.addEventListener('click', () => healthView(ctx));
      };
      await load();

      /* "Run again" ran nothing.
       *
       * Its onclick named runHc(), a function that lived in the preview's own
       * script and was never carried over — so the checks could be read once
       * and never refreshed, on the screen whose whole purpose is telling you
       * whether something is still wrong. */
      $$('button', root)
        .filter(b2 => /^run again$/i.test((b2.textContent || '').trim()))
        .forEach(b2 => b2.addEventListener('click', () => load()));

      ctx.setTeardown(close);
    },
  }).catch(oops);
}

/* ---- speed test ---------------------------------------------------------- */
export function speedView(ctx) {
  const name = ctx.params.name;
  openScreen('monitor', {
    pick: '.dlg.st3',
    bind: async (root, close) => {
      ctx.setTeardown(close);
      const sub = root.querySelector('.dh .ttl small');
      if (sub) sub.textContent = name;

      /* The gauge was drawn and never driven.
       *
       * The view looked for `.go, #spGo, .gobtn` and the button is #spGoBtn, so
       * nothing was bound: the whole screen — needle, arc, the three result
       * tiles, Test again — was a picture. The selectors below are the ids that
       * are actually in monitor.html.
       */
      const el = id => root.querySelector('#' + id);
      const go = el('spGoBtn'), wrap = el('spGoWrap'), centre = el('spCentre');
      const phase = el('spPhase'), big = el('spBig'), unit = el('spUnit');
      const prog = el('spProg'), needle = el('spNeedle');
      const note = el('spNote'), via = el('spVia'), back = el('spBack');
      const tile = id => root.querySelector('#' + id + ' .v6');

      const ARC = 556.1, SWEEP = 270, MAX = 1000;
      const paint = mbps => {
        const frac = Math.max(0, Math.min(1, (Number(mbps) || 0) / MAX));
        if (prog) prog.setAttribute('stroke-dashoffset', String(ARC * (1 - frac)));
        if (needle) needle.setAttribute('transform',
          `rotate(${(-135 + SWEEP * frac).toFixed(1)} 150 150)`);
      };
      const show = (n, u, label) => {
        if (centre) centre.hidden = false;
        if (big) big.textContent = n;
        if (unit) unit.textContent = u;
        if (phase) phase.textContent = label;
      };
      const reset = () => {
        paint(0);
        if (centre) centre.hidden = true;
        if (wrap) wrap.hidden = false;
        if (back) back.hidden = true;
        ['rPing', 'rDown', 'rUp'].forEach(id => { const v = tile(id); if (v) v.textContent = '—'; });
      };
      reset();
      back?.addEventListener('click', reset);

      /* What this machine can measure for this tunnel, and what it cannot. A
         side that holds the backends has nothing to measure through — it is the
         side that receives — and the plan says so rather than letting the
         operator press GO and wait for a failure. */
      let port = 0;
      const maps = el('spMaps');
      try {
        const plan = await api.speedPlan(name);
        if (plan.blocked) {
          if (note) note.textContent = plan.blocked;
          if (go) go.disabled = true;
          if (via) via.hidden = true;
          if (maps) maps.hidden = true;
        } else {
          const targets = plan.targets || [];
          const pick = t => {
            port = t ? t.listenPort : (plan.port || 0);
            if (via) {
              via.innerHTML = t
                ? `Through <b>port ${t.listenPort}</b> · ${plan.seconds}s · `
                  + `the backend on it is unavailable while this runs`
                : `To <b>${plan.peer || 'the other end'}</b> on port ${plan.port} · ${plan.seconds}s`;
            }
          };

          /* The mappings, from the plan rather than from the preview.
           *
           * The markup shipped with three example ports and pickMap(this) on
           * each — a function that does not exist here, so the choice could not
           * be made and the first one was assumed. A mapping the server says
           * cannot be measured is shown and refused, because "why is that one
           * not there" is a worse question than "why can that one not be
           * tested", and the plan already answers the second.
           */
          if (maps) {
            maps.hidden = !targets.length;
            maps.innerHTML = '';
            targets.forEach((t, i) => {
              const usable = !t.reason;
              const b = document.createElement('button');
              b.className = 'mp' + (usable && i === 0 ? ' on' : '');
              b.disabled = !usable;
              b.innerHTML = `<span class="rd3"${usable ? '' : ' style="opacity:.35"'}></span>`
                + `<span><b>Port ${t.listenPort}</b><i>${esc(t.reason
                  || `The receiver there must sink on port ${t.backendPort} — `
                     + `that backend is down while this runs`)}</i></span>`;
              if (usable) {
                b.addEventListener('click', () => {
                  maps.querySelectorAll('.mp').forEach(x => x.classList.toggle('on', x === b));
                  pick(t);
                });
              }
              maps.append(b);
            });
          }
          pick(targets.find(t => !t.reason) || targets[0] || null);
          const how = targets.length > 1 ? 'Pick a mapping, then press GO' : 'Press GO';
          if (note) note.textContent = `${how} — it takes about ${plan.seconds} seconds.`;
        }
      } catch (e) {
        if (note) note.textContent = e.message || 'Could not work out what to measure.';
        if (go) go.disabled = true;
      }

      go?.addEventListener('click', async () => {
        go.disabled = true;
        if (wrap) wrap.hidden = true;
        show('…', '', 'Measuring');
        if (note) note.textContent = 'Measuring — the port being used is unavailable meanwhile.';
        try {
          const r = await api.speedRun({ name, port });
          const mbps = Number(r.mbps) || 0;
          paint(mbps);
          show(mbps.toFixed(mbps < 100 ? 1 : 0), 'Mb/s', 'Throughput');
          /* One figure is what the server measures, so one figure is what is
             shown. The other two tiles say they were not measured rather than
             being filled with something that looks like a reading. */
          const d = tile('rDown'); if (d) d.textContent = mbps.toFixed(1) + ' Mb/s';
          const p = tile('rPing'); if (p) p.textContent = 'not measured';
          const u = tile('rUp');   if (u) u.textContent = 'not measured';
          /* Where the far end is a managed server the panel started the
             receiver itself, and saying so is the difference between a number
             and a number you understand the provenance of. */
          if (note) {
            note.textContent = (r.summary || '')
              + (r.receiver ? ` · the receiver was started on ${r.receiver} for this run` : '');
          }
        } catch (e) {
          show('—', '', 'Failed');
          if (note) note.textContent = e.message || 'The test did not run.';
        }
        if (back) back.hidden = false;
        go.disabled = false;
      });
    },
  }).catch(oops);
}


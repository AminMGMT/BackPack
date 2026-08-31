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
      const sub = root.querySelector('.dh .ttl small');
      if (sub) sub.textContent = name;

      const go = root.querySelector('.go, #spGo, .gobtn');
      const out = root.querySelector('.spout, .result, .res');
      go?.addEventListener('click', async () => {
        go.disabled = true;
        toast('Speed test started — this takes a moment.');
        try {
          await api.speedRun({ name });
          if (out) out.textContent = 'Running…';
        } catch (e) { oops(e); }
        go.disabled = false;
      });
      ctx.setTeardown(close);
    },
  }).catch(oops);
}

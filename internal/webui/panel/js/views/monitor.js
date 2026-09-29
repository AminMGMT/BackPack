/* Alerts and health check — screens that were designed together and share
 * one stylesheet.
 *
 * CLI: Manage → Health Check, and the alert history.
 */

import { $$, esc } from '../lib/dom.js';
import { ago } from '../lib/format.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { oops, toast } from '../ui/toast.js';

/* ---- alerts -------------------------------------------------------------- */
const clock = d => d.toTimeString().slice(0, 5);

/* Today, Yesterday, or the date — the feed is read by day. */
function dayLabel(d) {
  const start = x => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const days = Math.round((start(new Date()) - start(d)) / 864e5);
  if (days === 0) return 'Today';
  if (days === 1) return 'Yesterday';
  return d.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' });
}

export function alertsView(ctx) {
  openScreen('monitor', {
    pick: '.dlg.al2',
    bind: async (root, close) => {
      let data;
      try { data = await api.alerts(); } catch (e) { return oops(e); }

      /* The template's own shapes — .ah2/.ai2 for what is firing, .daysep/.ev2
         for the feed — because they carry the insets the dialog was designed
         with. The rows this used to write (.arow/.erow) had two pixels either
         side, so every line sat pressed against the dialog's edge. */
      const active = root.querySelector('.active');
      const list = data.active || [];
      if (active) {
        active.classList.toggle('calm', !list.length);
        active.innerHTML = list.length
          ? `<div class="ah2">Firing now · ${list.length}</div>` +
            list.map(a => `<div class="ai2"><i>!</i><span>${esc(a)}</span></div>`).join('')
          : `<div class="ai2 ok"><i>✓</i><span>Nothing is firing right now.</span></div>`;
      }

      /* Newest first. The file keeps them oldest first, and drawing it in that
         order put the last thing that happened at the bottom of a list that
         opens at the top — the current state was the one line you had to
         scroll for. */
      const feed = root.querySelector('.evs');
      if (feed) {
        const evs = (data.events || []).slice().reverse();
        let day = '';
        feed.innerHTML = evs.map(e => {
          const d = new Date(e.time);
          const label = dayLabel(d);
          const sep = label !== day ? `<div class="daysep">${esc(label)}</div>` : '';
          day = label;
          return sep + `<div class="ev2"><span class="tm" title="${esc(d.toLocaleString())}">` +
            /* ago() takes unix seconds and the file holds RFC 3339 — handed
               the string, every line said "NaN d ago". */
            `${esc(clock(d))}<em>${esc(ago(d.getTime() / 1000))}</em></span>` +
            `<span class="ms">${esc(e.message)}</span></div>`;
        }).join('') || `<div class="ev2 none"><span class="ms">No events recorded yet.</span></div>`;
        const body = root.querySelector('.body4');
        if (body) body.scrollTop = 0;
      }
      const note = root.querySelector('.df .note');
      if (note) note.textContent = 'Kept by the monitor service · newest first.';
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

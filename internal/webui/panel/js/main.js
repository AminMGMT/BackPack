/* Boot, routing and the chrome that surrounds every screen. */

import { $, el } from './lib/dom.js';
import { paintIcons } from './lib/icons.js';
import * as store from './store.js';
import * as router from './router.js';
import { toast } from './ui/toast.js';
import { dashboard } from './views/dashboard.js';
import { overview } from './views/overview.js';
import { logsView } from './views/logs.js';
import { metricsView } from './views/metrics.js';
import { historyView } from './views/history.js';
import { linkTestView } from './views/linktest.js';
import { editView } from './views/edit.js';
import { addView } from './views/add.js';
import { settingsView } from './views/settings.js';
import { serversView } from './views/servers.js';
import { maintView, undoView } from './views/maint.js';
import { alertsView, healthView, speedView } from './views/monitor.js';
import { starView, supportView } from './views/support.js';
import { closeScreen } from './ui/screen.js';
import { mountStrip } from './ui/strip.js';
import * as api from './api.js';

/* ---- appearance ----------------------------------------------------------
   Two choices, both remembered: the ground (dark or light) and the accent. The
   accent is stored under the name the existing panel already uses, so a browser
   that has one keeps it. */
function setTheme(t) {
  document.body.dataset.t = t;
  try { localStorage.setItem('bp_theme', t); } catch (e) {}
  /* The browser paints the address bar and the notch from this, so it has to
     follow the ground rather than stay at whatever the markup shipped with. */
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute('content', t === 'light' ? '#f2f2f2' : '#070707');
  markAppearance();
}

function setAccent(a) {
  if (a === 'none') delete document.body.dataset.accent;
  else document.body.dataset.accent = a;
  try { localStorage.setItem('bp_accent', a); } catch (e) {}
  markAppearance();
}

function markAppearance() {
  const t = document.body.dataset.t || 'dark';
  const a = document.body.dataset.accent || 'none';
  document.querySelectorAll('#ap-theme button')
    .forEach(b => b.classList.toggle('on', b.dataset.theme === t));
  document.querySelectorAll('#ap-accent button')
    .forEach(b => b.classList.toggle('on', b.dataset.a === a));
}

let appearOpen = false;
function toggleAppearance() {
  const box = $('#appearance'), btn = $('#appearance-btn');
  if (!box || !btn) return;
  appearOpen = !appearOpen;
  if (appearOpen) {
    box.hidden = false;
    const r = btn.getBoundingClientRect();
    box.style.top = (r.bottom + 8) + 'px';
    box.style.right = Math.max(12, window.innerWidth - r.right) + 'px';
    requestAnimationFrame(() => {
      box.classList.add('on');
      /* Opening a popover and leaving the focus behind it means a keyboard
         cannot reach what just appeared. */
      box.querySelector('button')?.focus();
    });
  } else {
    box.classList.remove('on');
    setTimeout(() => { if (!appearOpen) box.hidden = true; }, 300);
    btn.focus();
  }
  btn.setAttribute('aria-expanded', String(appearOpen));
}

/* ---- header -------------------------------------------------------------- */
function paintHeader(state) {
  const s = state.stats;
  if (!s) return;
  $('.HH .who b').textContent = s.hostname || 'Backpack';
  $('#hdr-sub').textContent = [s.version, s.location].filter(Boolean).join(' · ') || '—';
  /* Only speak when something is wrong or actionable. */
  $('#warnbar').hidden = s.monitorRunning !== false;
}

/* ---- routes -------------------------------------------------------------- */
/* The four sections. Each renders into #view; the strip above them is chrome
   and belongs to no section. */
router.route('/', overview);
router.route('/tunnels', dashboard);
router.route('/servers', serversView);

/* A screen that opens over the fleet keeps the fleet underneath: the route
   renders the dashboard first, then puts the dialog on top of it, so closing
   the dialog lands back on the cards rather than on an empty page. */
/* The pages a dialog can be opened over. Which one is drawn underneath is the
   page you were on, not a guess — see router.setHome. */
const PAGES = { '/': overview, '/tunnels': dashboard, '/servers': serversView };

function over(view, fixed) {
  return ctx => {
    /* Resolved per visit, not once. Assigning back into the parameter cached
       the first answer in the closure, so the second time a dialog was opened
       it drew whatever page had been behind it the first time. */
    const under = fixed || PAGES[router.getHome()] || overview;
    /* Both halves are torn down, and that is the whole point of this.
     *
     * The page underneath subscribes to the store, and the subscription used to
     * be dropped on the floor here — so opening any dialog left a second view
     * alive, repainting #view on every poll for the rest of the session. It was
     * invisible while every route drew the same page underneath. With sections
     * it is not: you open Add tunnel, go to Servers, and a few seconds later
     * the poll puts the tunnels back over the top of it.
     */
    let underDown = () => {};
    let viewDown = () => {};
    under({ setTeardown: fn => { if (fn) underDown = fn; } });
    view({ ...ctx, setTeardown: fn => { if (fn) viewDown = fn; } });
    ctx.setTeardown(() => { viewDown(); underDown(); });
  };
}
router.route('/t/:name/logs',    over(logsView));
router.route('/t/:name/metrics', over(metricsView));
router.route('/t/:name/history', over(historyView));
router.route('/t/:name/link',    over(linkTestView));
router.route('/t/:name/speed',   over(speedView));
router.route('/t/:name/edit',    over(editView));
router.route('/t/:name/undo',    over(undoView));

router.route('/add',         over(addView));
router.route('/settings',    over(settingsView));

router.route('/alerts',      over(alertsView));
router.route('/health',      over(healthView));
router.route('/maintenance', over(maintView));
router.route('/support',     over(supportView));
router.route('/star',        over(starView));

/* ---- boot ----------------------------------------------------------------
   Hold the loading screen until the first read of the host and the tunnels
   resolves. A minimum keeps it from flashing on a fast connection; a maximum
   takes it down even if the API never answers, so a hung request cannot trap
   the page on the loader. */
(function boot() {
  const start = Date.now(), MIN = 700, MAX = 9000;
  let done = false, settled = 0;

  const mark = id => {
    document.getElementById(id)?.classList.add('done');
    settled++;
    const arc = $('#boot-arc');
    if (arc) arc.style.strokeDashoffset = String(194 * (1 - settled / 2));
  };

  const reveal = () => {
    if (done) return;
    done = true;
    setTimeout(() => {
      $('#panel').hidden = false;
      const b = $('#boot');
      b.classList.add('gone');
      setTimeout(() => b.remove(), 700);
      store.startPolling();
      store.loadAlerts();
    }, Math.max(0, MIN - (Date.now() - start)));
  };

  const a = store.loadStats().then(() => mark('read-stats'));
  const b = store.loadTunnels().then(() => mark('read-tunnels'));
  Promise.allSettled([a, b]).then(reveal);
  setTimeout(reveal, MAX);
})();

/* A new release is worth saying once, not on every reload — the panel that
   nags is the panel people stop reading, and this one also carries the alerts.
   So: a notification the first time a given version is seen, and after that it
   waits quietly on the Maintenance screen. */
let toldAbout = null;
try { toldAbout = localStorage.getItem('bp_seen_update'); } catch (e) {}

store.subscribe(state => {
  const tag = state.stats?.updateTag;
  if (!tag || tag === toldAbout) return;
  toldAbout = tag;
  try { localStorage.setItem('bp_seen_update', tag); } catch (e) {}
  setTimeout(() => toast(`${tag} is available — open Maintenance to install it.`), 1200);
});

store.subscribe(paintHeader);

document.addEventListener('DOMContentLoaded', () => {
  let theme = 'dark', accent = 'none';
  try {
    theme = localStorage.getItem('bp_theme') || 'dark';
    accent = localStorage.getItem('bp_accent') || 'none';
  } catch (e) {}
  setTheme(theme);
  setAccent(accent);
  paintIcons();

  /* Every binding is optional and independent.
   *
   * These all used to run in one block, so a single element that had moved —
   * a button taken out of the header, say — threw on the first line and left
   * everything after it unbound, and nothing said why. One missing node should
   * cost its own feature and nothing else. */
  const bind = (sel, ev, fn) => {
    const n = $(sel);
    if (n) n.addEventListener(ev, fn);
    else console.warn('panel: nothing matches', sel);
  };

  bind('#appearance-btn', 'click', ev => { ev.stopPropagation(); toggleAppearance(); });
  bind('#ap-theme', 'click', ev => {
    const b = ev.target.closest('[data-theme]');
    if (b) setTheme(b.dataset.theme);
  });
  bind('#ap-accent', 'click', ev => {
    const b = ev.target.closest('[data-a]');
    if (b) setAccent(b.dataset.a);
  });
  document.addEventListener('click', ev => {
    if (appearOpen && !ev.target.closest('#appearance, #appearance-btn')) toggleAppearance();
  });
  bind('#alerts-btn', 'click', () => router.go('/alerts'));
  bind('.warnbar .act', 'click', () => router.go('/health'));

  document.addEventListener('keydown', ev => {
    if (ev.key !== 'Escape') return;
    if (appearOpen) toggleAppearance();
  });

  /* The dock. Only the two sections light it up: a dialog opened over one of
     them — Add tunnel, Settings — leaves the section underneath marked, because
     that is still where you are. */
  bind('#dock', 'click', ev => {
    const b = ev.target.closest('[data-dock]');
    if (b) router.go(b.dataset.dock);
  });
  const paintDock = path => {
    /* A dialog opened over a section leaves that section marked, because it is
       still where you are. Settings is one of those dialogs now: the overview
       has a button for it, so the dock does not carry a second door to it. */
    const at = ['/servers', '/tunnels'].find(p => path.startsWith(p))
      || (path.startsWith('/t/') ? '/tunnels' : '/');
    document.querySelectorAll('#dock [data-dock]').forEach(b => {
      const on = b.dataset.dock === at;
      b.classList.toggle('on', on);
      b.setAttribute('aria-selected', String(on));
    });
  };
  store.subscribe(state => {
    const n = $('#dock-t');
    if (n) n.textContent = state.tunnels?.length ? String(state.tunnels.length) : '';
  });

  /* The other half of the dock. The tunnels are already in the store; the fleet
     is not, and asking for it once here is what stops the badge reading as
     "no servers" until you happen to open the section. It is deliberately not
     polled: a count that is one page-load stale is not worth a request every
     few seconds, and the section itself is live while you are on it. */
  api.nodes()
    .then(state => {
      const n = $('#dock-s');
      if (n) n.textContent = state.nodes?.length ? String(state.nodes.length) : '';
    })
    .catch(() => { /* the badge simply stays empty */ });

  mountStrip();
  router.start(path => {
    /* A page you can come back to is remembered; a dialog is not, or closing
       one would try to return to the one before it. */
    if (PAGES[path]) router.setHome(path);
    closeScreen();
    paintDock(path);
  });

  /* ?wide=0 goes back to the auto-filling square card, for comparison. */
  if (new URLSearchParams(location.search).get('wide') === '0') document.body.classList.remove('wide');

});

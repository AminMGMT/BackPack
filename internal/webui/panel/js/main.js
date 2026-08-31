/* Boot, routing and the chrome that surrounds every screen. */

import { $, el } from './lib/dom.js';
import { paintIcons } from './lib/icons.js';
import * as store from './store.js';
import * as router from './router.js';
import { renderMenu, toggleMenu, closeMenu, menuOpen } from './ui/menu.js';
import { toast } from './ui/toast.js';
import { dashboard } from './views/dashboard.js';
import { logsView } from './views/logs.js';
import { metricsView } from './views/metrics.js';
import { historyView } from './views/history.js';
import { linkTestView } from './views/linktest.js';
import { editView } from './views/edit.js';
import { addView } from './views/add.js';
import { settingsView } from './views/settings.js';
import { maintView, undoView } from './views/maint.js';
import { alertsView, healthView, speedView } from './views/monitor.js';
import { starView, supportView } from './views/support.js';
import { closeScreen } from './ui/screen.js';
import { MOCK } from './api.js';

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
  renderMenu(state);
}

/* ---- routes -------------------------------------------------------------- */
router.route('/', dashboard);

/* A screen that opens over the fleet keeps the fleet underneath: the route
   renders the dashboard first, then puts the dialog on top of it, so closing
   the dialog lands back on the cards rather than on an empty page. */
function over(view) {
  return ctx => {
    dashboard({ setTeardown: () => {} });
    view(ctx);
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
   waits quietly in the menu. */
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
   * everything after it unbound: the menu stopped opening and nothing said
   * why. One missing node should cost its own feature and nothing else. */
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
  bind('#menu-btn', 'click', ev => { ev.stopPropagation(); toggleMenu(); });
  bind('#menu-scrim', 'click', closeMenu);
  bind('#alerts-btn', 'click', () => router.go('/alerts'));
  bind('.warnbar .act', 'click', () => router.go('/health'));

  document.addEventListener('keydown', ev => {
    if (ev.key !== 'Escape') return;
    if (appearOpen) toggleAppearance();
    if (menuOpen()) { closeMenu(); $('#menu-btn')?.focus(); }
  });

  router.start(() => { closeMenu(); closeScreen(); });

  /* ?wide=0 goes back to the auto-filling square card, for comparison. */
  if (new URLSearchParams(location.search).get('wide') === '0') document.body.classList.remove('wide');

  if (MOCK) document.body.append(el('div', { class: 'mock-flag', text: 'mock data' }));
});

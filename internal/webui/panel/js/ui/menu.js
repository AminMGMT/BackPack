/* The main menu: one way in, instead of a row of icons nobody can tell apart.
 *
 * It opens as a profile card — the server's identity, then everything about the
 * machine that is read when a server is set up and rarely again, then the
 * destinations. Two of the facts appear only when they disagree with
 * themselves, which is the only reason they are worth a line.
 *
 * Markup follows the approved preview: .menuM / .mph / .facts / .it.
 */

import { el, $, clear } from '../lib/dom.js';
import { svg, paintIcons } from '../lib/icons.js';
import { go } from '../router.js';

let open = false;

const fact = (k, v, warn, full) =>
  el('div', { class: 'fct' + (full ? ' full' : '') }, [
    el('div', { class: 'k', text: k }),
    el('b', { text: v || '—' }),
    warn ? el('i', { text: warn }) : null,
  ]);

const item = (icon, label, onClick, extra) =>
  el('button', { class: 'it', role: 'menuitem', on: { click: onClick } }, [
    el('span', { html: svg(icon, 'ic') }),
    el('span', { class: 'lbl', text: label }),
    extra || null,
    el('span', { class: 'chev', html: svg('chev', 'chev') }),
  ]);

export function renderMenu(state) {
  const nav = $('#main-menu');
  const s = state.stats || {};
  const alertCount = (state.alerts?.active?.length || 0) + (s.updateTag ? 1 : 0);
  clear(nav);

  nav.append(
    el('div', { class: 'mph' }, [
      el('div', { class: 'mpa', html: svg('pack') }),
      el('div', { style: 'min-width:0' }, [
        el('div', { class: 'mpn', text: s.hostname || 'Backpack' }),
        el('div', { class: 'mps',
          text: [s.os, s.location].filter(Boolean).join(' · ') || 'Server panel' }),
      ]),
    ]),
    el('div', { class: 'hr' }),
    el('div', { class: 'facts' }, [
      fact('Uptime', s.uptime), fact('OS', s.os),
      fact('IPv4', s.ipv4), fact('IPv6', s.ipv6),
      fact('Location', s.location), fact('ISP', s.isp),
      s.congestion
        ? fact('Congestion', s.congestion.toUpperCase(),
            s.congestionWanted && s.congestion !== s.congestionWanted
              ? s.congestionWanted.toUpperCase() + ' not available on this kernel' : null, true)
        : null,
      s.proxyEnabled
        ? fact('Built-in proxy',
            (s.proxyType || 'proxy').toUpperCase() + (s.proxyPort ? ' :' + s.proxyPort : ''),
            s.proxyRunning ? null : 'Enabled but not running — forwarded ports to it are refused',
            true)
        : null,
    ].filter(Boolean)),
    el('div', { class: 'hr' }),
    item('gear', 'Settings', () => hop('/settings')),
    item('bell', 'Alerts', () => hop('/alerts'),
      alertCount ? el('span', { class: 'cnt', text: String(alertCount) }) : null),
    item('nodes', 'Servers', () => hop('/servers')),
    item('pulse', 'Health check', () => hop('/health')),
    item('box', 'Maintenance', () => hop('/maintenance'),
      s.updateTag ? el('span', { class: 'cnt', text: '1' }) : null),
    item('gh', 'GitHub', () =>
      window.open('https://github.com/AminMGMT/BackPack', '_blank', 'noopener')),
    el('div', { class: 'hr' }),
    /* The way back to the finished panel, while this one is still being built.
       It is a plain link rather than a call to the API on purpose: it has to
       keep working on a screen where something else has already failed, and
       the server treats ?panel=classic as both the redirect and the choice.
       See internal/webui/panel.go. */
    item('undo', 'Classic panel', () => { location.href = '/?panel=classic'; }),
    el('div', { class: 'hr' }),
    el('button', { class: 'it out', role: 'menuitem',
                   on: { click: () => { location.href = '/logout'; } } }, [
      el('span', { html: svg('logout', 'ic') }),
      el('span', { class: 'lbl', text: 'Log out' }),
    ]),
  );
  paintIcons(nav);

  /* The dot on the button says something is waiting while the menu is shut;
     the number in the row says how much once it is open. */
  const bdg = $('#alerts-bdg');
  bdg.hidden = !alertCount;
  bdg.textContent = alertCount || '';
}

function hop(path) { closeMenu(); go(path); }

export function openMenu() {
  const nav = $('#main-menu'), btn = $('#menu-btn'), scrim = $('#menu-scrim');
  nav.hidden = false; scrim.hidden = false;
  /* Measured against the button rather than anchored by CSS: the sticky header
     carries a backdrop-filter, which makes it the containing block for
     anything fixed inside it. */
  const r = btn.getBoundingClientRect();
  nav.style.top = (r.bottom + 8) + 'px';
  nav.style.right = Math.max(12, window.innerWidth - r.right) + 'px';
  requestAnimationFrame(() => { nav.classList.add('on'); scrim.classList.add('on'); });
  btn.setAttribute('aria-expanded', 'true');
  open = true;
}

export function closeMenu() {
  const nav = $('#main-menu'), btn = $('#menu-btn'), scrim = $('#menu-scrim');
  nav.classList.remove('on'); scrim.classList.remove('on');
  btn.setAttribute('aria-expanded', 'false');
  setTimeout(() => { if (!open) { nav.hidden = true; scrim.hidden = true; } }, 280);
  open = false;
}

export const toggleMenu = () => (open ? closeMenu() : openMenu());
export const menuOpen = () => open;

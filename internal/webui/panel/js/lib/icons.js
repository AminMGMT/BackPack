/* One icon set, as path data.
 *
 * Kept out of the markup so a view asks for `data-icon="pack"` and never
 * carries 200 characters of path with it. Everything is stroked and inherits
 * currentColor, which is what makes the set work in both themes.
 */

export const ICONS = {
  pack:   '<path d="M8 6V5a4 4 0 018 0v1"/><path d="M5 8h14a2 2 0 012 2v8a3 3 0 01-3 3H6a3 3 0 01-3-3v-8a2 2 0 012-2z"/><path d="M9 12h6"/>',
  burger: '<path d="M3 6h18M3 12h18M3 18h18"/>',
  close:  '<path d="M18 6L6 18M6 6l12 12"/>',
  chev:   '<path d="M9 18l6-6-6-6"/>',
  down:   '<path d="M12 5v14"/><path d="M6 13l6 6 6-6"/>',
  up:     '<path d="M12 19V5"/><path d="M6 11l6-6 6 6"/>',
  update: '<path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><path d="M17 8l-5-5-5 5"/><path d="M12 3v12"/>',
  theme:  '<circle cx="12" cy="12" r="4.2"/><path d="M12 2v2M12 20v2M2 12h2M20 12h2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M19.1 4.9l-1.4 1.4M6.3 17.7l-1.4 1.4"/>',
  gear:   '<circle cx="12" cy="12" r="3"/><path d="M12 2v3M12 19v3M2 12h3M19 12h3M4.9 4.9l2.1 2.1M17 17l2.1 2.1M19.1 4.9L17 7M7 17l-2.1 2.1"/>',
  bell:   '<path d="M18 8a6 6 0 00-12 0c0 7-3 9-3 9h18s-3-2-3-9"/><path d="M13.7 21a2 2 0 01-3.4 0"/>',
  pulse:  '<path d="M22 12h-4l-3 9L9 3l-3 9H2"/>',
  gh:     '<path d="M9 19c-5 1.5-5-2.5-7-3m14 6v-3.9a3.4 3.4 0 00-1-2.6c3-.3 6.5-1.5 6.5-7A5.4 5.4 0 0020 4.8 5 5 0 0019.9 1S18.7.6 16 2.5a13.4 13.4 0 00-7 0C6.3.6 5.1 1 5.1 1A5 5 0 005 4.8 5.4 5.4 0 003.5 8.5c0 5.5 3.5 6.7 6.5 7a3.4 3.4 0 00-1 2.6V22"/>',
  logout: '<path d="M9 21H5a2 2 0 01-2-2V5a2 2 0 012-2h4"/><path d="M16 17l5-5-5-5"/><path d="M21 12H9"/>',
  warn:   '<path d="M12 9v5"/><circle cx="12" cy="17.5" r=".6"/><path d="M10.3 3.9L2.6 17.4A2 2 0 004.3 20.4h15.4a2 2 0 001.7-3L13.7 3.9a2 2 0 00-3.4 0z"/>',
  check:  '<path d="M4 12.5l5.5 5.5L20 7"/>',
  play:   '<path d="M7 4.5l12 7.5-12 7.5z"/>',
  stop:   '<rect x="6" y="6" width="12" height="12" rx="2"/>',
  restart:'<path d="M3 8v6h6"/><path d="M3.5 14a9 9 0 103-8.5L3 8"/>',
  trash:  '<path d="M4 7h16"/><path d="M9 7V5a1 1 0 011-1h4a1 1 0 011 1v2"/><path d="M6 7l1 13a1 1 0 001 1h8a1 1 0 001-1l1-13"/>',
  edit:   '<path d="M4 20h4L19 9a2.1 2.1 0 00-3-3L5 17z"/><path d="M14.5 6.5l3 3"/>',
  logs:   '<path d="M8 3H6a2 2 0 00-2 2v14a2 2 0 002 2h9a2 2 0 002-2v-2"/><path d="M16 3h4v4"/><path d="M9 8h5M9 12h6M9 16h4"/>',
  chart:  '<path d="M3 20h18"/><path d="M6 16l4-5 3.5 3L20 6"/>',
  more:   '<circle cx="5" cy="12" r="1.4"/><circle cx="12" cy="12" r="1.4"/><circle cx="19" cy="12" r="1.4"/>',
  bot:    '<rect x="4" y="8" width="16" height="12" rx="3"/><path d="M12 8V4"/><circle cx="9" cy="14" r="1"/><circle cx="15" cy="14" r="1"/>',
  plus:   '<path d="M12 5v14M5 12h14"/>',
  link:   '<path d="M9 15l6-6"/><path d="M11 6l1-1a4.2 4.2 0 016 6l-1 1"/><path d="M13 18l-1 1a4.2 4.2 0 01-6-6l1-1"/>',
  box:    '<path d="M3 8l9-5 9 5v8l-9 5-9-5z"/><path d="M3 8l9 5 9-5"/><path d="M12 13v8"/>',
  nodes:  '<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="14" width="18" height="7" rx="2"/><path d="M7 7.5h.01"/><path d="M7 17.5h.01"/>',
  clock:  '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3.2 2"/>',
  gauge:  '<path d="M12 14l4-4"/><path d="M4.5 18a9 9 0 1115 0"/>',
  term:   '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 9l3 3-3 3"/><path d="M13 15h4"/>',
  undo:   '<path d="M3 8v6h6"/><path d="M3.5 14a9 9 0 103-8.5L3 8"/>',
};

export function svg(name, cls = '') {
  const d = ICONS[name];
  if (!d) return '';
  return `<svg class="${cls}" viewBox="0 0 24 24" aria-hidden="true">${d}</svg>`;
}

/* Fills every [data-icon] under root that has not been filled yet. Views can
   emit the placeholder and call this once instead of stitching SVG strings. */
export function paintIcons(root = document) {
  root.querySelectorAll('[data-icon]').forEach(node => {
    if (node.firstElementChild) return;
    node.innerHTML = svg(node.dataset.icon);
  });
}

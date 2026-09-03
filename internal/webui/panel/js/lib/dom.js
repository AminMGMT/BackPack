/* The three DOM helpers everything else is built from. */

export const $ = (sel, root = document) => root.querySelector(sel);
export const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

/* Build an element from a tag, an attribute bag and children.
 * `html` sets innerHTML, `on` takes a map of listeners, anything else becomes
 * an attribute — so a view reads as the markup it produces. */
export function el(tag, attrs = {}, ...kids) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null || v === undefined || v === false) continue;
    if (k === 'html') node.innerHTML = v;
    else if (k === 'text') node.textContent = v;
    else if (k === 'on') for (const [ev, fn] of Object.entries(v)) node.addEventListener(ev, fn);
    else if (k === 'class') node.className = v;
    else if (k === 'dataset') Object.assign(node.dataset, v);
    else if (v === true) node.setAttribute(k, '');
    else node.setAttribute(k, v);
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined || kid === false) continue;
    node.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return node;
}

/* Text that came from the server is never trusted into innerHTML. */
export function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

export function clear(node) { while (node.firstChild) node.removeChild(node.firstChild); }

/* One delegated listener instead of one per row: the rows are replaced on every
   poll, and listeners bound to them would be rebound just as often. */
/* Returns a disposer. #view outlives every view rendered into it, so a
 * delegated listener left on it survives the view that added it: come back to a
 * screen five times and the fifth click on one of its buttons fires five times.
 * The caller is expected to call this from its teardown. */
export function delegate(root, event, selector, handler) {
  const onEvent = ev => {
    const hit = ev.target.closest(selector);
    if (hit && root.contains(hit)) handler(ev, hit);
  };
  root.addEventListener(event, onEvent);
  return () => root.removeEventListener(event, onEvent);
}

export const reduceMotion = () =>
  window.matchMedia('(prefers-reduced-motion: reduce)').matches;

/* The preview drew every dialog's subtitle with its own server's name and its
 * own version — "ubuntu-4gb-nbg1-2 · v1.7.5" — and a screen that does not
 * overwrite it shows the operator somebody else's machine. `tail` is whatever
 * the screen wants to add after the host.
 */
export function dialogSubtitle(root, stats, tail) {
  const el = root.querySelector('.dh .ttl small');
  if (!el) return;
  const parts = [(stats && stats.hostname) || '', tail].filter(Boolean);
  el.textContent = parts.join(' · ') || '—';
}

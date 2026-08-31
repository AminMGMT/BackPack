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
export function delegate(root, event, selector, handler) {
  root.addEventListener(event, ev => {
    const hit = ev.target.closest(selector);
    if (hit && root.contains(hit)) handler(ev, hit);
  });
}

export const reduceMotion = () =>
  window.matchMedia('(prefers-reduced-motion: reduce)').matches;

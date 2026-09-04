/* Opening a screen.
 *
 * Each screen owns a stylesheet and a markup template, both lifted from the
 * preview it was designed in. The stylesheet is injected when the screen opens
 * and removed when it closes, so two screens can never fight over a rule — the
 * previews were drawn independently and some of them style the same class.
 *
 * The templates still carry the previews' inline onclick handlers; they are
 * stripped on load and the view binds real listeners instead.
 */

import { $, el } from '../lib/dom.js';

const tplCache = new Map();
let active = null;

/* The sheet is added when the screen opens and taken out again when it closes.
 * That is not tidiness — each preview was drawn on its own and every one of
 * them restyles .dlg and .scrim, some with hundreds of rules. Two sheets alive
 * at once means the second screen you open wears half of the first one's
 * layout. Exactly one is ever attached. */
function loadCSS(name) {
  document.querySelectorAll('link[data-screen]').forEach(l => l.remove());
  const link = el('link', { rel: 'stylesheet', href: `css/screens/${name}.css`,
                            dataset: { screen: name } });
  const done = new Promise(res => { link.onload = res; link.onerror = res; });
  document.head.append(link);
  return done;
}

function dropCSS() {
  document.querySelectorAll('link[data-screen]').forEach(l => l.remove());
}

async function loadTemplate(name) {
  if (!tplCache.has(name)) {
    tplCache.set(name, fetch(`views/${name}.html`).then(r => r.text()));
  }
  const raw = await tplCache.get(name);
  return raw
    /* The previews drove everything from inline onclick. Those functions do not
       exist here, but what they were *for* does — so the call is kept as data
       and re-bound below instead of being thrown away, which is what left every
       tab, drawer and Back button dead. */
    .replace(/\sonclick="([a-zA-Z_$][\w$]*)\(([^"]*)\)[;\s]*"/g,
      (_, fn, args) => ` data-fn="${fn}" data-args="${args.replace(/["]/g, '&quot;')}"`)
    .replace(/\son[a-z]+="[^"]*"/g, '')
    /* Every value= in a template is an example somebody typed while drawing it.
       Real values arrive from the binding; leaving the samples in means a field
       nothing fills shows another server's settings as if they were yours. */
    .replace(/(<input\b(?![^>]*type="(?:checkbox|radio|hidden)")[^>]*?)\svalue="[^"]*"/g, '$1');
}

/* Opens `name` as a modal. `pick` selects which block of the template to use
   when a preview held several. `bind(root, close)` wires it to data. */
export async function openScreen(name, { pick = '.dlg', bind } = {}) {
  closeScreen();
  await loadCSS(name);
  const html = await loadTemplate(name);

  const holder = el('div', { html });
  const node = holder.querySelector(pick) || holder.firstElementChild;
  if (!node) throw new Error(`screen ${name}: nothing matched ${pick}`);
  /* Several previews drew more than one dialog in one file and left all but the
     first `hidden`, which is how they showed one at a time on a static page.
     Picking one here is the decision to show it, so the attribute goes: without
     this, Support and Undo were built, wired and appended to a page that then
     refused to draw them. */
  node.hidden = false;

  const scrim = el('div', { class: 'scrim' }, el('div', { class: 'veil' }));
  scrim.append(node);
  document.body.append(scrim);
  document.body.classList.add('modal');
  requestAnimationFrame(() => scrim.classList.add('on'));

  const close = () => closeScreen();
  active = { scrim, name };

  node.querySelectorAll('.x, .x9, .x2, [data-close]')
      .forEach(b => b.addEventListener('click', close));
  wire(node, close);
  scrim.addEventListener('click', ev => { if (ev.target === scrim) close(); });
  document.addEventListener('keydown', onKey);

  if (bind) await bind(node, close);
  return node;
}

/* The behaviours every preview shared, restored once here rather than per
   screen: closing, tabs, drawers, single-choice groups and wizard steps. A view
   that needs to know a choice was made listens for the `pick` event. */
function wire(root, close) {
  const sibs = el => [...el.parentElement.children].filter(n => n.tagName === el.tagName);

  root.addEventListener('click', ev => {
    const b = ev.target.closest('[data-fn]');
    if (!b || !root.contains(b)) return;
    const fn = b.dataset.fn;
    const args = (b.dataset.args || '').split(',').map(a => a.trim().replace(/^['"]|['"]$/g, ''));

    /* closeAdd, closeEd, closeLogs, closeDetails … all mean the same thing. */
    if (/^close/i.test(fn)) { close(); return; }

    if (fn === 'tab') {
      const want = args[1] || args[0];
      sibs(b).forEach(x => x.classList.toggle('on', x === b));
      root.querySelectorAll('[data-tab]').forEach(p => { p.hidden = p.dataset.tab !== want; });
      return;
    }

    if (fn === 'setPane') {
      const want = args[1] || args[0];
      [...b.parentElement.children].forEach(x => x.classList.toggle('on', x === b));
      root.querySelectorAll('[data-p]').forEach(p => { p.hidden = p.dataset.p !== want; });
      return;
    }

    /* dr / dr2: a drawer opens under the row you pressed. */
    if (/^dr\d?$/.test(fn)) { b.parentElement.classList.toggle('open'); return; }

    /* go(1) / go(-1): the wizard's Back and Continue. */
    if (fn === 'go') {
      const steps = [...root.querySelectorAll('.step[data-s]')];
      if (!steps.length) return;
      const at = steps.findIndex(x => !x.hidden);
      const to = Math.max(0, Math.min(steps.length - 1, at + Number(args[0] || 1)));
      steps.forEach((x, i) => { x.hidden = i !== to; });
      const back = root.querySelector('#backb');
      if (back) back.disabled = to === 0;
      root.dispatchEvent(new CustomEvent('step', { detail: { at: to, of: steps.length } }));
      return;
    }

    /* Every remaining verb in the previews is "pick one of these". */
    sibs(b).forEach(x => x.classList.toggle('on', x === b));
    root.dispatchEvent(new CustomEvent('pick', {
      detail: { fn, value: args.filter(a => a && a !== 'this')[0], el: b },
    }));
  });

  /* Copy buttons sat beside the thing they copy in every preview. */
  root.querySelectorAll('button').forEach(b => {
    if (!/^copy/i.test(b.textContent.trim())) return;
    b.addEventListener('click', async () => {
      const box = b.closest('div, li, tr') || root;
      const src = box.querySelector('input, code, .mono, .addr, .ad0');
      const text = src ? (src.value ?? src.textContent).trim() : '';
      if (!text) return;
      try {
        await navigator.clipboard.writeText(text);
        const was = b.textContent;
        b.textContent = 'Copied';
        setTimeout(() => { b.textContent = was; }, 1400);
      } catch (e) { /* the browser refused; the value is still on screen */ }
    });
  });

  /* Buttons whose whole job is to shut the dialog, however they are labelled. */
  root.querySelectorAll('button').forEach(b => {
    if (b.dataset.fn) return;
    if (/^(close|cancel|done|not now)$/i.test(b.textContent.trim())) {
      b.addEventListener('click', close);
    }
  });
}

function onKey(ev) { if (ev.key === 'Escape') closeScreen(); }

export function closeScreen() {
  if (!active) return;
  const { scrim } = active;
  active = null;
  document.removeEventListener('keydown', onKey);
  scrim.classList.remove('on');
  document.body.classList.remove('modal');
  /* The sheet is dropped only if nothing has opened in the meantime.
   *
   * Going from one screen straight to another is the common case — Health to
   * Support, Maintenance to Undo — and the next screen attaches its stylesheet
   * within a few milliseconds, well inside this delay. Dropping unconditionally
   * therefore tore the sheet out from under the screen that had just opened,
   * and since each screen's CSS is what gives .scrim and .dlg their layout, the
   * result was a dialog present in the page with no size and nothing visible. */
  setTimeout(() => {
    scrim.remove();
    if (!active) dropCSS();
  }, 320);
}


/* The questions the panel has to ask, in the panel.
 *
 * The browser's own confirm() is a grey system box with the hostname in it; it
 * cannot show what is about to be removed and cannot look destructive when it
 * is. The wording stays exactly what it was. Markup follows the approved
 * preview: .cfZ / .boxZ.
 */

import { el, esc } from '../lib/dom.js';
import { svg, paintIcons } from '../lib/icons.js';

/* defaultNo puts the safe answer under the cursor and under Enter.
 *
 * The default here is to confirm, which is right for a question somebody
 * opened on purpose. It is wrong for one the panel raises on its own — a second
 * question, about a second machine, that the operator did not go looking for.
 * There the reflex to press Enter has to mean "no". */
export function confirmBox({ title, body, lines = [], go = 'Confirm', danger = false, icon,
                             defaultNo = false }) {
  return new Promise(resolve => {
    const scrim = el('div', { class: 'cfZ' }, el('div', { class: 'veilZ' }));
    const box = el('div', { class: 'boxZ' + (danger ? ' danger' : '') }, [
      el('div', { class: 'icZ', html: svg(icon || (danger ? 'warn' : 'check')) }),
      /* title and lines both land in innerHTML, so both are escaped here.
       *
       * They were not, and the callers were left to remember. Every one of them
       * did remember for the title — `<q>${esc(name)}</q>` — and none of them
       * did for the lines, which is what a convention that lives in the callers
       * rather than the sink always ends up looking like.
       *
       * It stopped being only untidy once a line could hold a name from another
       * machine. The panel lists a managed server's tunnels by reading its
       * config directory, and those names are filenames: on Linux anything but
       * "/" and NUL is legal in one, and nothing between that directory and
       * this element filtered them. A server in the fleet could put markup in a
       * filename and have it run here, in the operator's session, on the origin
       * that holds every other server's root password.
       *
       * title takes markup on purpose — the callers wrap the variable and pass
       * the tags — so it is escaped one level down, around the value, not here.
       * A line is a value and nothing else, so it is escaped whole. */
      el('h2', { html: title }),
      el('p', { text: body }),
      lines.length
        ? el('div', { class: 'listZ' },
            lines.map(l => el('div', { html: svg(l.icon || 'logs') + `<code>${esc(l.text)}</code>` })))
        : null,
      el('div', { class: 'actZ' }, [
        el('button', { on: { click: () => done(false) } }, 'Cancel'),
        el('button', { class: 'go', on: { click: () => done(true) } }, go),
      ]),
      el('div', { class: 'escZ', html: defaultNo
        ? '<kbd>Esc</kbd> and <kbd>Enter</kbd> both leave it alone'
        : '<kbd>Esc</kbd> cancels · <kbd>Enter</kbd> confirms' }),
    ]);
    scrim.append(box);
    document.body.append(scrim);
    paintIcons(scrim);
    scrim.classList.add('on');
    requestAnimationFrame(() =>
      box.querySelector(defaultNo ? '.actZ button' : '.go').focus());

    const key = ev => {
      if (ev.key === 'Escape') done(false);
      if (ev.key === 'Enter') done(!defaultNo);
    };
    function done(answer) {
      document.removeEventListener('keydown', key);
      scrim.classList.remove('on');
      setTimeout(() => scrim.remove(), 280);
      resolve(answer);
    }
    document.addEventListener('keydown', key);
    scrim.addEventListener('click', ev => { if (ev.target === scrim) done(false); });
  });
}

/* The questions the panel has to ask, in the panel.
 *
 * The browser's own confirm() is a grey system box with the hostname in it; it
 * cannot show what is about to be removed and cannot look destructive when it
 * is. The wording stays exactly what it was. Markup follows the approved
 * preview: .cfZ / .boxZ.
 */

import { el } from '../lib/dom.js';
import { svg, paintIcons } from '../lib/icons.js';

export function confirmBox({ title, body, lines = [], go = 'Confirm', danger = false, icon }) {
  return new Promise(resolve => {
    const scrim = el('div', { class: 'cfZ' }, el('div', { class: 'veilZ' }));
    const box = el('div', { class: 'boxZ' + (danger ? ' danger' : '') }, [
      el('div', { class: 'icZ', html: svg(icon || (danger ? 'warn' : 'check')) }),
      el('h2', { html: title }),
      el('p', { text: body }),
      lines.length
        ? el('div', { class: 'listZ' },
            lines.map(l => el('div', { html: svg(l.icon || 'logs') + `<code>${l.text}</code>` })))
        : null,
      el('div', { class: 'actZ' }, [
        el('button', { on: { click: () => done(false) } }, 'Cancel'),
        el('button', { class: 'go', on: { click: () => done(true) } }, go),
      ]),
      el('div', { class: 'escZ', html: '<kbd>Esc</kbd> cancels · <kbd>Enter</kbd> confirms' }),
    ]);
    scrim.append(box);
    document.body.append(scrim);
    paintIcons(scrim);
    scrim.classList.add('on');
    requestAnimationFrame(() => box.querySelector('.go').focus());

    const key = ev => {
      if (ev.key === 'Escape') done(false);
      if (ev.key === 'Enter') done(true);
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

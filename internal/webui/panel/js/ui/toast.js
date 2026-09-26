import { $ } from '../lib/dom.js';
import { svg } from '../lib/icons.js';

let timer = null;

export function toast(text, isError = false, action = null) {
  const box = $('#toast');
  $('#toast-tx').textContent = text;
  $('#toast-ic').innerHTML = svg(isError ? 'warn' : 'check');

  /* One button, replaced each time, so a toast never carries the last one's. */
  box.querySelector('.tact')?.remove();
  if (action) {
    const b = document.createElement('button');
    b.className = 'tact';
    b.textContent = action.label;
    b.addEventListener('click', () => {
      box.classList.remove('on', 'run');
      action.run();
    });
    $('#toast-tx').after(b);
  }
  box.classList.remove('run');
  void box.offsetWidth;                    /* restart the drain */
  box.classList.add('on', 'run');
  box.classList.toggle('err', isError);
  clearTimeout(timer);
  /* A toast with something to press stays longer: three and a half seconds is
     enough to read a sentence and not enough to decide to act on it. */
  timer = setTimeout(() => box.classList.remove('on', 'run'), action ? 9000 : 3600);
}

/* A refusal that carries a remedy offers it, rather than describing it.
 *
 * The link test needs the server holding the tunnel's other end, and refuses
 * when the panel does not know which server that is. The sentence said to go and link it; the
 * action is on another screen, and knowing it exists at all is the hard part.
 * So the toast carries the button. */
const FIXES = {
  'link-tunnel': { label: 'Link it', run: name => onFix.link?.(name) },
};

/* Set by the screen that can act on a fix, so this file does not have to know
   what linking a tunnel involves. */
export const onFix = {};

export const oops = (e, subject) => {
  const fix = e && e.fix && FIXES[e.fix];
  toast(e && e.message ? e.message : 'That did not work.', true,
    fix ? { label: fix.label, run: () => fix.run(subject) } : null);
};

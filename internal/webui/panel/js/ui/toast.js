import { $ } from '../lib/dom.js';
import { svg } from '../lib/icons.js';

let timer = null;

export function toast(text, isError = false) {
  const box = $('#toast');
  $('#toast-tx').textContent = text;
  $('#toast-ic').innerHTML = svg(isError ? 'warn' : 'check');
  box.classList.remove('run');
  void box.offsetWidth;                    /* restart the drain */
  box.classList.add('on', 'run');
  box.classList.toggle('err', isError);
  clearTimeout(timer);
  timer = setTimeout(() => box.classList.remove('on', 'run'), 3600);
}

export const oops = e => toast(e && e.message ? e.message : 'That did not work.', true);

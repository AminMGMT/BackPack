/* "Enjoying Backpack?" and "Support Backpack".
 * Static screens; the addresses copy on click. */

import { $$ } from '../lib/dom.js';
import { openScreen } from '../ui/screen.js';
import { toast, oops } from '../ui/toast.js';

function copyable(root) {
  $$('.wal0', root).forEach(w => w.addEventListener('click', async () => {
    const addr = w.dataset.addr || w.querySelector('.ad0')?.textContent?.trim();
    if (!addr) return;
    try {
      await navigator.clipboard.writeText(addr);
      w.classList.add('copied');
      const tip = w.querySelector('.cp0');
      if (tip) tip.textContent = 'Copied';
      setTimeout(() => {
        w.classList.remove('copied');
        if (tip) tip.textContent = 'Copy';
      }, 1500);
    } catch (e) { toast('There is nothing on the clipboard.', true); }
  }));
}

/* Both dialogs are asks, so every button either opens a link or ends the ask.
   "Do not ask again" is remembered here the way the panel remembers it. */
function links(root, close) {
  const open = url => window.open(url, '_blank', 'noopener');
  root.querySelectorAll('button').forEach(b => {
    const t = b.textContent.trim().toLowerCase();
    if (t.includes('github')) b.addEventListener('click', () => open('https://github.com/AminMGMT/BackPack'));
    else if (t.includes('telegram')) b.addEventListener('click', () => open('https://t.me/BlackProtocols'));
    else if (t === 'later') b.addEventListener('click', close);
    else if (t === 'do not ask again') b.addEventListener('click', () => {
      try { localStorage.setItem('bp_star', 'never'); } catch (e) {}
      close();
    });
  });
}

/* The version beside the attribution, read from the same place the rest of the
   panel reads it. The notice itself is in the markup rather than built here:
   NOTICE requires it to be present, and markup is what survives somebody
   trimming a script. */
function stampVersion(root) {
  const el = root.querySelector('#bpver');
  if (!el) return;
  try {
    const v = document.documentElement.dataset.version || '';
    if (v) el.textContent = v;
  } catch (e) { /* the notice stands without it */ }
}

export const starView = ctx => openScreen('support', {
  pick: '.dlg.star0',
  bind: (root, close) => { links(root, close); copyable(root); stampVersion(root); ctx.setTeardown(close); },
}).catch(oops);

export const supportView = ctx => openScreen('support', {
  pick: '.dlg.sup0',
  bind: (root, close) => { links(root, close); copyable(root); stampVersion(root); ctx.setTeardown(close); },
}).catch(oops);

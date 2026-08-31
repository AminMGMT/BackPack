/* Live log for one tunnel.
 *
 * CLI: Manage → Manage Tunnels → Live Log.
 */

import { $, $$, esc } from '../lib/dom.js';
import { flag, kindLabel } from '../lib/format.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { toast, oops } from '../ui/toast.js';
import { go } from '../router.js';

/* journald prefixes the level; the preview colours by it. */
/* journald hands the panel free text, so the level is read from the line. The
   list is the failures this tunnel actually produces, not a generic word list:
   a bind clash and a refused dial are errors however they are phrased. */
function levelOf(line) {
  const s = line.toLowerCase();
  if (/error|fatal|panic|refused|failed|address already in use|no route to host|permission denied|cannot|could not/.test(s))
    return 'error';
  if (/warn|retry|flap|dropped|overflow|timeout|reverted/.test(s)) return 'warn';
  return 'info';
}

function render(box, lines, level) {
  const rows = lines
    .map(l => ({ text: typeof l === 'string' ? l : (l.message || l.line || ''), }))
    .filter(r => r.text)
    .map(r => ({ ...r, lv: levelOf(r.text) }))
    .filter(r => level === 'all' || r.lv === level);

  box.innerHTML = rows.length
    ? rows.map(r => {
        /* Only the clock: the date is the same all the way down, and printing
           it on every row pushed the column onto two lines. */
        const m = r.text.match(/^\S+\s+\d+\s+([\d:]+)\s+(.*)$/);
        const [, when, rest] = m || [null, '', r.text];
        return `<div class="ln ${r.lv}"><span class="t">${esc(when)}</span>` +
               `<span class="lv ${r.lv}">${r.lv}</span>` +
               `<span class="msg">${esc(rest)}</span></div>`;
      }).join('')
    : `<div class="ln info"><span class="msg">Nothing at this level.</span></div>`;
}

/* Patterns worth explaining, and what to do about each. The panel only speaks
   when it recognises something — a diagnosis that is always on screen is
   decoration, and decoration that looks like a finding is worse than silence. */
const KNOWN = [
  [/too many segments/i,
   'The layer-3 read is failing',
   'too many segments means the kernel handed the tunnel a coalesced run of small packets that split into more pieces than there were buffers for it.',
   'Update to 1.7.5 or later, where a run that overflows keeps the packets that fit and the tunnel stays up. Nothing needs changing on the kharej side.'],
  [/bind: address already in use/i,
   'A port it needs is taken',
   'Something else on this machine is already listening on that port, so the tunnel cannot claim it.',
   'Find the holder with ss -ltnp, then either stop it or move this tunnel to another port from Edit.'],
  [/no route to host|connection refused/i,
   'The peer is not answering',
   'The address resolves but nothing accepts a connection on that port.',
   'Check the other side is running and its firewall lets this address in. Link Test measures the same path.'],
];

function diagnose(box, text) {
  if (!box) return;
  const hit = KNOWN.find(([rx]) => rx.test(text));
  box.hidden = !hit;
  if (!hit) return;
  const [, title, detail, fix] = hit;
  const t = box.querySelector('.tx > b');
  if (t) t.textContent = title;
  const d = box.querySelector('.detail');
  if (d) d.textContent = detail;
  const f = box.querySelector('.fix');
  if (f) f.innerHTML = '<b>What to do:</b> ' + fix;
}

export async function logsView(ctx) {
  const name = ctx.params.name;
  /* A deep link arrives before the first poll, so the tunnel may not be in the
     store yet; without this the header falls back to the preview's own text. */
  let t = store.tunnel(name);
  if (!t) { await store.loadTunnels(); t = store.tunnel(name); }

  openScreen('logs', {
    pick: '.dlg',
    bind: async (root, close) => {
      /* The header is the card's, so the dialog is obviously about that tunnel. */
      const fl = root.querySelector('.dh .fl');
      if (fl) fl.textContent = flag(t?.peerCountry) || flag(t?.country) || '·';
      const ttl = root.querySelector('.dh .ttl > div');
      if (ttl) ttl.textContent = name;
      const sub = root.querySelector('.dh .ttl small');
      if (sub && t) sub.textContent =
        [t.peerLocation, t.peerISP, kindLabel(t)].filter(Boolean).join(' · ');

      const box = root.querySelector('.log');
      let level = 'all', lines = [], follow = true;

      const segs = $$('.segs button', root);
      segs.forEach((b, i) => b.addEventListener('click', () => {
        segs.forEach(x => x.classList.toggle('on', x === b));
        level = ['all', 'warn', 'error'][i] || 'all';
        render(box, lines, level);
      }));

      const liveBtn = root.querySelector('.live');
      liveBtn?.addEventListener('click', () => {
        follow = !follow;
        liveBtn.classList.toggle('on', follow);
      });

      root.querySelector('.tool')?.addEventListener('click', async () => {
        const text = lines.join('\n');
        try { await navigator.clipboard.writeText(text); toast('Log copied.'); }
        catch (e) { toast('This browser will not let the page read the clipboard.', true); }
      });

      const diagBox = root.querySelector('.diag');
      const pull = async () => {
        try {
          const text = await api.logs(name);
          lines = String(text).split('\n').filter(Boolean);
          render(box, lines, level);
          diagnose(diagBox, text);
          if (follow) box.scrollTop = box.scrollHeight;
        } catch (e) { oops(e); }
      };
      await pull();
      const timer = setInterval(() => { if (!document.hidden) pull(); }, 3000);

      ctx.setTeardown(() => { clearInterval(timer); close(); });
    },
  }).catch(oops);

  /* Closing the dialog returns to the fleet rather than a blank route. */
  ctx.setTeardown ??= () => {};
}

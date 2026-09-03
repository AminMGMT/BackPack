/* Live log for one tunnel.
 *
 * CLI: Manage → Manage Tunnels → Live Log.
 */

import { $, $$, el, esc } from '../lib/dom.js';
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
      /* All of this screen's state in one place, and declared before anything
         that touches it. seenAt was declared beside the jump button it belongs
         to, further down — and setFollow, which runs on the way in, assigns it.
         So opening the screen threw before it had drawn anything, and the only
         sign was a toast. */
      let level = 'all', lines = [], follow = true, end = 'local', seenAt = 0;

      /* Three buttons share the .tool class, and only the first was bound.
       *
       * querySelector takes one, so Pause was wired to the copy handler and the
       * other two to nothing: pressing Pause copied the log, Copy for report did
       * nothing at all, and Download raised a toast naming a file from the
       * preview that was never written. They are bound by what they are now.
       */
      const liveBtn = root.querySelector('.live');
      const pauseBtn = root.querySelector('#pauseBtn');
      const setFollow = on => {
        follow = on;
        if (on) seenAt = lines.length;
        liveBtn?.classList.toggle('on', follow);
        if (pauseBtn) {
          pauseBtn.lastChild.textContent = follow ? ' Pause' : ' Resume';
          pauseBtn.classList.toggle('on', !follow);
        }
      };
      setFollow(follow);
      liveBtn?.addEventListener('click', () => setFollow(!follow));
      pauseBtn?.addEventListener('click', () => setFollow(!follow));

      const copyLog = async () => {
        const text = lines.join('\n');
        try { await navigator.clipboard.writeText(text); toast('Log copied.'); }
        catch (e) { toast('This browser will not let the page read the clipboard.', true); }
      };
      const tools = $$('.tool', root);
      (root.querySelector('#copyBtn')
        || tools.find(b => /copy/i.test(b.textContent)))?.addEventListener('click', copyLog);

      /* Saved from what is on screen rather than fetched again: the lines here
         are the ones the operator is looking at, filter and all. */
      tools.find(b => /download/i.test(b.textContent))?.addEventListener('click', () => {
        const blob = new Blob([lines.join('\n') + '\n'], { type: 'text/plain' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = `backpack-${name}-${new Date().toISOString().slice(0, 10)}.log`;
        document.body.append(a);
        a.click();
        a.remove();
        setTimeout(() => URL.revokeObjectURL(url), 1000);
      });

      /* "N new" — jump back to the tail.
       *
       * The preview drew this button reading "3 new" and wired it to a handler
       * that was never written, so it sat there permanently claiming three
       * lines had arrived and did nothing when pressed. It is what it says now:
       * shown only while following is off and lines have actually arrived since,
       * counting them, and putting the reader back at the bottom.
       */
      const jump = root.querySelector('#jump');
      const jumpN = root.querySelector('#jumpN');
      const paintJump = () => {
        if (!jump) return;
        const behind = follow ? 0 : Math.max(0, lines.length - seenAt);
        jump.hidden = behind === 0;
        if (jumpN) jumpN.textContent = behind === 1 ? '1 new' : `${behind} new`;
      };
      jump?.addEventListener('click', () => {
        setFollow(true);
        box.scrollTop = box.scrollHeight;
      });

      const countNote = root.querySelector('#count');
      const diagBox = root.querySelector('.diag');
      const pull = async () => {
        try {
          const text = await api.logs(end === 'peer' ? name : name, end);
          lines = String(text).split('\n').filter(Boolean);
          render(box, lines, level);
          diagnose(diagBox, text);
          if (follow) { box.scrollTop = box.scrollHeight; seenAt = lines.length; }
          paintJump();
          if (countNote) {
            countNote.textContent = `${lines.length} ${lines.length === 1 ? 'line' : 'lines'}`
              + (follow ? ' · following' : ' · paused');
          }
        } catch (e) { oops(e); }
      };
      await pull();

      /* Which end's log this is.
       *
       * A tunnel is one thing in two places, and until now this screen only
       * showed the half on this machine. The other half is where a client that
       * cannot dial, a certificate it could not read or a port already held
       * over there says so — and reading it meant logging into that server,
       * which is the second pass the fleet exists to remove.
       *
       * Only offered for a tunnel this panel built across a managed server:
       * anything else has no other end this panel can reach, and a switch that
       * cannot work is worse than no switch. */
      /* Set up after the first read, because switching ends calls pull() and a
         handler that closed over it before it was declared threw the moment the
         other end was picked — the pane just said "Reading…" for ever. */
      if (t?.node) {
        const pick = el('div', { class: 'ends' }, [
          el('button', { class: 'on', text: 'This server' }),
          el('button', { text: t.node }),
        ]);
        const segsRow = root.querySelector('.segs')?.parentElement;
        (segsRow || box.parentElement).insertBefore(pick, segsRow ? segsRow.firstChild : box);
        const [localB, peerB] = pick.querySelectorAll('button');
        const setEnd = which => {
          if (end === which) return;
          end = which;
          localB.classList.toggle('on', which === 'local');
          peerB.classList.toggle('on', which === 'peer');
          lines = [];
          box.innerHTML = `<div class="ln info"><span class="msg">Reading…</span></div>`;
          pull();
        };
        localB.addEventListener('click', () => setEnd('local'));
        peerB.addEventListener('click', () => setEnd('peer'));
      }

      const segs = $$('.segs button', root);
      segs.forEach((b, i) => b.addEventListener('click', () => {
        segs.forEach(x => x.classList.toggle('on', x === b));
        level = ['all', 'warn', 'error'][i] || 'all';
        render(box, lines, level);
      }));

      const timer = setInterval(() => { if (!document.hidden) pull(); }, 3000);

      ctx.setTeardown(() => { clearInterval(timer); close(); });
    },
  }).catch(oops);

  /* Closing the dialog returns to the fleet rather than a blank route. */
  ctx.setTeardown ??= () => {};
}

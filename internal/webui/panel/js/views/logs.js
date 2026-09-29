/* Live log for one tunnel.
 *
 * CLI: Manage → Manage Tunnels → Live Log.
 */

import { $, $$, el, esc, copyText } from '../lib/dom.js';
import { flag, kindLabel } from '../lib/format.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { toast, oops } from '../ui/toast.js';
import { go } from '../router.js';
import { parseLine } from '../lib/logline.js';

/* Taking a journal line apart lives in lib/logline.js, where it is tested. */

function render(box, lines, level) {
  const rows = lines
    .map(l => (typeof l === 'string' ? l : (l.message || l.line || '')))
    .filter(Boolean)
    .map(parseLine)
    .filter(r => level === 'all' || r.lv === level);

  box.innerHTML = rows.length
    ? rows.map(r =>
        `<div class="ln ${r.lv}"><span class="t">${esc(r.when)}</span>` +
        `<span class="lv ${r.lv}">${r.lv}</span>` +
        `<span class="msg">${esc(r.msg)}</span></div>`).join('')
    : `<div class="ln info"><span class="msg">Nothing at this level.</span></div>`;
}

/* Patterns worth explaining, and what to do about each.
 *
 * The panel only speaks when it recognises something — a diagnosis that is
 * always on screen is decoration, and decoration that looks like a finding is
 * worse than silence.
 *
 * `when` is the second half of recognising it. Matching a phrase says the line
 * appeared; it does not say the explanation applies. The segment-overflow entry
 * was the case that proved it: it told an operator on 1.7.7.5 to "update to
 * 1.7.5 or later" — advice to install something older than what they were
 * running — and it said the layer-3 read was failing on a TCP reverse tunnel,
 * which has no layer-3 read to fail. Both halves of that were confidently
 * wrong, on a screen somebody had opened precisely because they wanted to know
 * what was wrong.
 *
 * So an entry may also say what has to be true of the tunnel and the version.
 * When it cannot be, the panel says nothing, which is the honest answer. */
const KNOWN = [
  {
    rx: /too many segments/i,
    /* Only a layer-3 tunnel has the read this describes, and only a build
       older than the fix still tears down over it. */
    when: ({ kindOf, olderThan }) => kindOf('l3') && olderThan('1.7.5'),
    title: 'The layer-3 read is failing',
    detail: 'too many segments means the kernel handed the tunnel a coalesced run of '
          + 'small packets that split into more pieces than there were buffers for it.',
    fix: 'Update to 1.7.5 or later, where a run that overflows keeps the packets that fit '
       + 'and the tunnel stays up. Nothing needs changing on the kharej side.',
  },
  {
    rx: /too many segments/i,
    /* Same line, on a build that already handles it. Worth explaining, because
       it is alarming and it is not a fault. */
    when: ({ kindOf }) => kindOf('l3'),
    title: 'A burst the device could not split in one go',
    detail: 'The kernel handed the tunnel a coalesced run of small packets that split into '
          + 'more pieces than there were buffers for it. This build keeps the packets that '
          + 'fit and reads the rest next time.',
    fix: 'Nothing. The tunnel stays up; the line records a busy moment, not a failure.',
  },
  {
    rx: /bind: address already in use/i,
    title: 'A port it needs is taken',
    detail: 'Something else on this machine is already listening on that port, so the '
          + 'tunnel cannot claim it.',
    fix: 'Find the holder with ss -ltnp, then either stop it or move this tunnel to '
       + 'another port from Edit.',
  },
  {
    rx: /no route to host|connection refused/i,
    title: 'The peer is not answering',
    detail: 'The address resolves but nothing accepts a connection on that port.',
    fix: 'Check the other side is running and its firewall lets this address in. '
       + 'Link Test measures the same path.',
  },
];

/* The facts an entry's `when` is allowed to ask about. */
function diagnosisContext(tunnel) {
  const running = (store.get().stats?.version || '').replace(/^v/, '').trim();
  const parts = v => String(v).replace(/^v/, '').split('.').map(n => parseInt(n, 10) || 0);
  return {
    kindOf: want => {
      const k = ((tunnel?.carrier || tunnel?.transport || '') + ' ' + (tunnel?.direction || ''))
        .toLowerCase();
      return want === 'l3' ? /l3|gre|layer-?3/.test(k) : k.includes(want);
    },
    olderThan: v => {
      if (!running) return false; // unknown version: do not claim it is old
      const a = parts(running), b = parts(v);
      for (let i = 0; i < Math.max(a.length, b.length); i++) {
        if ((a[i] || 0) !== (b[i] || 0)) return (a[i] || 0) < (b[i] || 0);
      }
      return false;
    },
  };
}

function diagnose(box, text, tunnel) {
  if (!box) return;
  const ctx = diagnosisContext(tunnel);
  const hit = KNOWN.find(k => k.rx.test(text) && (!k.when || k.when(ctx)));
  box.hidden = !hit;
  if (!hit) return;
  const { title, detail, fix } = hit;
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
        if (await copyText(lines.join('\n'))) toast('Log copied.');
        else toast('This browser would not copy it — use Download instead.', true);
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
          diagnose(diagBox, text, t);
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

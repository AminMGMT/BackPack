/* Connection Test — which transports survive the path to a kharej server.
 *
 * The panel runs on the Iran server, which is the side a test starts from and
 * the side that judges it, so this is that side. Two panes of one size:
 *
 *   left   what to do now — the form, then the countdown and the one line to
 *          run on the kharej, then the test's own countdown, then the verdict
 *   right  the transports, waiting until the kharej joins, filling as each is
 *          tried, and at the end only the ones worth building on
 *
 * CLI: main menu → 0 Connection Test → Iran.
 */

import { $, esc, copyText, flashCopied } from '../lib/dom.js';
import * as api from '../api.js';
import { toast, oops } from '../ui/toast.js';
import { confirmBox } from '../ui/confirm.js';

const PRESETS = [
  { v: 'balance', label: 'Balance', note: 'least memory' },
  { v: 'turbo', label: 'Turbo', note: 'the default' },
  { v: 'aggressive', label: 'Aggressive', note: 'fast links' },
];

const ACTIVE = ['starting', 'waiting', 'running'];
const KINDS = [
  { k: 'reverse', label: 'Reverse', note: 'kharej dials in' },
  { k: 'direct', label: 'Direct', note: 'Iran dials out · layer 3' },
  { k: 'spoof', label: 'IP spoofing', note: 'forged source, both ways' },
];

const ICON = {
  copy: '<svg viewBox="0 0 24 24"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 012-2h10"/></svg>',
  play: '<svg viewBox="0 0 24 24"><path d="M7 5l12 7-12 7z"/></svg>',
  stop: '<svg viewBox="0 0 24 24"><rect x="6" y="6" width="12" height="12" rx="2"/></svg>',
  again: '<svg viewBox="0 0 24 24"><path d="M3 12a9 9 0 0115.5-6.3L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 01-15.5 6.3L3 16"/><path d="M3 21v-5h5"/></svg>',
  term: '<svg viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="16" rx="3"/><path d="M7 9l3 3-3 3"/><path d="M12.5 15H17"/></svg>',
  down: '<svg viewBox="0 0 24 24"><path d="M6 9l6 6 6-6"/></svg>',
  radar: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="5"/><path d="M12 12l6-6"/></svg>',
  medal: '<svg viewBox="0 0 24 24"><circle cx="12" cy="14" r="6"/><path d="M8.5 9.2L6 3h4l2 4 2-4h4l-2.5 6.2"/><path d="M12 11.5l.9 1.8 2 .3-1.45 1.4.35 2-1.8-.95-1.8.95.35-2L9.1 13.6l2-.3z"/></svg>',
};

/* The test's phase — not a tunnel's state, which lib/tstate.js owns. */
const phase = v => v.state || 'idle';
const now = () => Math.floor(Date.now() / 1000);
const JOIN = 15 * 60;

const stepOf = st => (st === 'done' ? 3 : st === 'running' ? 2 : st === 'waiting' || st === 'starting' ? 1 : 0);

let preset = 'turbo';

function stepper(st) {
  const at = stepOf(st);
  const steps = [
    ['Start here', 'Test tunnels on this server'],
    ['Run on the kharej', 'One line, as root'],
    ['Read the verdict', 'Which transports held'],
  ];
  return `<ol class="ct-steps">${steps.map(([b, s], i) => {
    const cls = i < at ? 'done' : i === at ? 'on' : '';
    return `<li class="${cls}"><span class="n">${i < at ? '✓' : i + 1}</span>
      <span class="tx"><b>${b}</b><small>${s}</small></span></li>`;
  }).join('<li class="bar" aria-hidden="true"></li>')}</ol>`;
}

/* ---- the clock ------------------------------------------------------------
   One dial for both countdowns: sixty ticks that go out as the time does, an
   arc over them, and the minutes in the middle. The ticks are the same
   drawing Manage's Auto Refresh uses, so the two read as one instrument. */
function clockSVG() {
  const ticks = Array.from({ length: 60 }, (_, i) => {
    const a = (i / 60) * Math.PI * 2 - Math.PI / 2;
    const r1 = i % 5 ? 86 : 82, r2 = 93;
    const p = n => (100 + Math.cos(a) * n).toFixed(1) + ' ' + (100 + Math.sin(a) * n).toFixed(1);
    const [x1, y1] = p(r1).split(' '), [x2, y2] = p(r2).split(' ');
    return `<line data-i="${i}" x1="${x1}" y1="${y1}" x2="${x2}" y2="${y2}"/>`;
  }).join('');
  return `<svg class="ck-svg" viewBox="0 0 200 200" aria-hidden="true">
    <g class="ck-ticks">${ticks}</g>
    <circle class="ck-trk" cx="100" cy="100" r="72"/>
    <circle class="ck-arc" cx="100" cy="100" r="72" pathLength="1000"/>
  </svg>`;
}

function clock(label) {
  return `<div class="ct-clock" id="ctClock" style="--p:1">
    <div class="ck-glow"></div>${clockSVG()}
    <div class="ck-read"><b id="ctLeft">--:--</b><small>${label}</small></div>
  </div>`;
}

const mmss = left => `${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`;

/* ---- left: what to do now ------------------------------------------------ */
function startPane(v) {
  const host = v.host || v.defaultHost || '';
  return `<div class="ct-l-in fade">
    <span class="ct-kick">New test</span>
    <div class="tl-lede"><b>Every transport, tried for real between this server and one kharej.</b>
      <span>Five minutes of stability checks, then about 8–12 seconds of speed testing per healthy tunnel.
        Download and upload use four connections and run one tunnel at a time. The test tunnels run on free ports and are
        removed when it ends — nothing is left behind on either server.</span></div>
    <label class="tl-f ct-field"><span>This server's address</span>
      <input id="ctHost" type="text" value="${esc(host)}" placeholder="the IP the kharej dials" autocomplete="off" spellcheck="false"></label>
    <div class="tl-f ct-field"><span>Preset</span>
      <div class="tl-seg" id="ctPreset">${PRESETS.map(p =>
        `<button type="button" data-p="${p.v}" class="${p.v === preset ? 'on' : ''}">
           ${p.label}<small>${p.note}</small></button>`).join('')}</div></div>
    ${v.root ? '' : `<div class="tl-note wr">The panel is not running as root, so the direct tunnels, PCK and the IP-spoofing check are left out.</div>`}
    <span class="sp"></span>
    <button class="tl-btn solid ct-go" id="ctStart">${ICON.play}Start the test</button>
  </div>`;
}

function waitPane(v) {
  const starting = phase(v) === 'starting';
  return `<div class="ct-l-in fade">
    <span class="ct-kick">Step 2 · on the kharej</span>
    ${clock('to join')}
    <div class="ct-wtitle"><b>${starting ? 'Starting the test tunnels' : 'Waiting for the kharej'}</b><i class="ct-dots"><i></i><i></i><i></i></i></div>
    ${v.command ? `<div class="ct-cmd">
        <span class="pr">${ICON.term}</span>
        <code id="ctCmd">${esc(v.command)}</code>
        <button class="tl-btn" data-copy="#ctCmd">${ICON.copy}<span>Copy</span></button>
      </div>
      <details class="ct-more">
        <summary>${ICON.down}The kharej has no Backpack yet</summary>
        <p>This installs it and runs the test in one go. Nothing stays installed as a tunnel.</p>
        <div class="ct-cmd sm"><code id="ctInst">${esc(v.install || '')}</code>
          <button class="tl-btn" data-copy="#ctInst">${ICON.copy}<span>Copy</span></button></div>
      </details>` : '<div class="ct-cmd ghost"><code>building the test tunnels…</code></div>'}
    <span class="sp"></span>
    <div class="ct-foot"><span class="tl-meta">From <b>${esc(v.host || '')}</b> · ${esc(title(v.preset))}</span>
      <button class="tl-btn ghost" id="ctStop">${ICON.stop}Stop</button></div>
  </div>`;
}

function runPane(v) {
  return `<div class="ct-l-in fade">
    <span class="ct-kick">Step 3 · testing</span>
    ${clock('left')}
    <div class="ct-wtitle"><b>Testing against ${esc(v.kharej || 'the kharej')}</b><i class="ct-dots"><i></i><i></i><i></i></i></div>
    <p class="ct-wsub" id="ctSettled">${settled(v)}</p>
    <span class="sp"></span>
    <div class="ct-foot"><span class="tl-meta">From <b>${esc(v.host || '')}</b> · ${esc(title(v.preset))}</span>
      <button class="tl-btn ghost" id="ctStop">${ICON.stop}Stop</button></div>
  </div>`;
}

function settled(v) {
  const live = (v.rows || []).filter(r => r.status !== 'skipped');
  const speed = live.find(r => r.phase === 'speed');
  if (speed) return `Measuring download and upload on ${esc(speed.name)} — ${live.filter(r => r.phase === 'speed-queued').length} healthy tunnels waiting.`;
  const done = live.filter(r => r.status !== 'testing').length;
  return live.length ? `${done} of ${live.length} transports judged — the rest are still carrying their echoes.`
    : 'The kharej is building its end of every test tunnel.';
}

const title = s => (s ? s[0].toUpperCase() + s.slice(1) : 'Turbo');

function bestPane(v) {
  const b = v.best;
  const finished = v.finished ? new Date(v.finished * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : '';
  const foot = `<span class="sp"></span>
    <div class="ct-foot"><span class="tl-meta">Tested with <b>${esc(title(v.preset))}</b>${finished ? ` · ${finished}` : ''}</span>
      <button class="tl-btn solid" id="ctNew">${ICON.again}Test again</button></div>`;
  if (!b || !b.tr) {
    return `<div class="ct-l-in ct-best none">
      <span class="ct-kick er">Verdict</span>
      <div class="tl-lede"><b>Nothing held on this path.</b>
        <span>Every transport either never connected or dropped its echoes. The rows that failed are
          in the kharej's table; a different kharej, or its provider's firewall, is the usual answer.</span></div>
      ${foot}</div>`;
  }
  const cell = (k, val, unit = '') => (val || val === 0) && val !== ''
    ? `<div class="ct-bc"><span>${k}</span><b>${esc(String(val))}${unit ? `<em>${unit}</em>` : ''}</b></div>` : '';
  return `<div class="ct-l-in ct-best">
    <div class="ct-best-head">
      <span class="ct-medal">${ICON.medal}</span>
      <div><span class="badge">Best for this path</span><b class="tr">${esc(b.tr)}</b></div>
    </div>
    <div class="ct-bgrid">
      ${cell('Download', b.mb ? b.mb.toFixed(b.mb < 10 ? 1 : 0) : '', 'Mb/s')}
      ${cell('Upload', b.upload_mb ? b.upload_mb.toFixed(b.upload_mb < 10 ? 1 : 0) : '', 'Mb/s')}
      ${cell('Round trip', b.rt, 'ms')}
      ${cell('Jitter', b.ji, 'ms')}
      ${cell('Worst', b.wo, 'ms')}
      ${cell('Loss', b.lo ? b.lo.toFixed(1) : '0', '%')}
      ${cell('Suggested preset', title(b.pr))}
      ${cell('Path MTU', b.pm)}
      ${cell('MSS', b.ms)}
      ${cell('Keepalive', b.ka, 's')}
      ${cell('Heartbeat', b.hb, 's')}
      ${b.fd ? cell('FEC', `${b.fd}+${b.fp}`) : ''}
    </div>
    <div class="tl-note">Build it from <b>Tunnels → Add tunnel</b>. The suggested preset is the one this path's
      speed and round trip call for; the test itself ran on ${esc(title(v.preset))}.</div>
    ${foot}</div>`;
}

function endPane(v) {
  const failed = phase(v) === 'failed';
  return `<div class="ct-l-in fade">
    <span class="ct-kick ${failed ? 'er' : ''}">${failed ? 'Did not finish' : 'Stopped'}</span>
    <div class="tl-lede"><b>${failed ? 'The test did not finish' : 'The test was stopped'}</b>
      <span>${v.error ? esc(v.error) : 'Every test tunnel has been removed.'}</span></div>
    <span class="sp"></span>
    <button class="tl-btn solid ct-go" id="ctNew">${ICON.again}Start a new test</button>
  </div>`;
}

/* ---- right: the transports ----------------------------------------------- */
const tone = st => ({ ok: 'ok', unstable: 'wr', down: 'er', skipped: 'sk' }[st] || 'run');
const statusWord = st => ({ ok: 'Held', unstable: 'Unstable', down: 'Down', skipped: 'Skipped', testing: 'Testing' }[st] || st);
const keyOf = r => `${r.kind}:${r.tr}`;

function rowHTML() {
  return `<span class="dot"></span>
    <span class="nm"><b></b><small></small></span>
    <span class="bar"><i></i></span>
    <span class="ec"></span>
    <span class="fg"></span>
    <span class="st"></span>`;
}

/* A row is patched, never redrawn, so its bar slides from where it was and the
   list does not flicker every two seconds. */
function fillRow(n, r) {
  const total = r.total || 60;
  const testing = r.status === 'testing';
  const share = Math.max(0, Math.min(1, testing ? (r.tried / total) : (r.ok / total)));
  n.className = `ct-row ${tone(r.status)}${r.uploadMbps ? ' has-speed' : ''}`;
  n.querySelector('.nm b').textContent = r.name;
  const sm = n.querySelector('.nm small');
  sm.textContent = r.detail || (r.uploadMbps ? '4 connections · download / upload' : '');
  sm.title = r.detail || '';
  n.querySelector('.bar i').style.setProperty('--w', (share * 100).toFixed(1) + '%');
  n.querySelector('.ec').textContent = r.status === 'skipped' ? '—' : `${testing ? r.tried : r.ok}/${total}`;
  n.querySelector('.fg').innerHTML = [
    r.rtt ? `<span><b>${r.rtt}</b> ms</span>` : '',
    r.mbps ? `<span>↓ <b>${r.mbps.toFixed(r.mbps < 10 ? 1 : 0)}</b> Mb/s</span>` : '',
    r.uploadMbps ? `<span>↑ <b>${r.uploadMbps.toFixed(r.uploadMbps < 10 ? 1 : 0)}</b> Mb/s</span>` : '',
  ].join('');
  n.querySelector('.st').textContent = r.phase === 'speed' ? 'Speed test' : r.phase === 'speed-queued' ? 'Queued' : statusWord(r.status);
}

function emptyRight(st) {
  const waiting = st === 'waiting' || st === 'starting';
  return `<div class="ct-empty ${waiting ? 'live' : ''}">
    <div class="ct-radar"><i></i><i></i><i></i><span>${ICON.radar}</span></div>
    <b>${waiting ? 'Waiting for the kharej to join' : 'Results land here'}</b>
    <small>${waiting ? 'The moment it checks in, every transport gets a row and starts filling.'
      : 'Each transport gets a row once the kharej runs its line: held, unstable or down, with its round trip and speed.'}</small>
    <div class="ct-ghosts">${'<span><i></i><i></i><i></i></span>'.repeat(4)}</div>
  </div>`;
}

export function connTestView(ctx) {
  const view = $('#view');
  view.innerHTML = `<div class="tl-page ct">
    <div class="sech2 tl-head"><h2>Connection test</h2><span class="cnt" id="ctState">—</span><span class="sp"></span></div>
    <p class="tl-sub">Which transports actually survive the route between this Iran server and a kharej — measured, not guessed.</p>
    <div id="ctSteps"></div>
    <div class="ct-duo">
      <section class="tl-card ct-l" id="ctLeft"></section>
      <section class="tl-card ct-r" id="ctRight">
        <div class="ct-rh" id="ctRHead"></div>
        <div class="ct-list" id="ctList"></div>
        <div class="ct-rf" id="ctRFoot" hidden></div>
      </section>
    </div>
  </div>`;
  const steps = $('#ctSteps', view), stateEl = $('#ctState', view);
  const left = $('#ctLeft', view), rHead = $('#ctRHead', view), list = $('#ctList', view), rFoot = $('#ctRFoot', view);
  let last = null, fresh = false, timer = null, tick = null, alive = true;
  let leftSig = '', rightMode = '', headSig = '';
  const rows = new Map();

  const dockDot = st => {
    const n = document.getElementById('dock-c');
    if (n) n.textContent = ACTIVE.includes(st) ? '●' : '';
  };

  /* The dial: which deadline it counts to depends on the phase. */
  const paintClock = () => {
    const c = $('#ctClock', left);
    if (!c || !last) return;
    const running = phase(last) === 'running';
    const end = running ? last.ends : last.deadline;
    if (!end) return;
    const span = running ? Math.max(60, end - (last.ran || end - 180)) : JOIN;
    const leftS = Math.max(0, end - now());
    const p = Math.min(1, leftS / span);
    const lbl = $('#ctLeft', c);
    if (lbl) lbl.textContent = running && leftS === 0 ? 'soon' : mmss(leftS);
    c.style.setProperty('--p', p.toFixed(4));
    const lit = Math.round(p * 60);
    c.querySelectorAll('.ck-ticks line').forEach((l, i) => l.classList.toggle('on', i < lit));
    c.classList.toggle('late', !running && leftS < 120);
  };

  const paintLeft = (v, st) => {
    const sig = JSON.stringify([fresh, st, v.command, v.error, v.kharej, v.best, st === 'idle' || fresh ? v.root : 0]);
    if (sig === leftSig) {
      if (st === 'running') { const s = $('#ctSettled', left); if (s) s.textContent = settled(v); }
      return;
    }
    leftSig = sig;
    const keep = $('#ctHost', left)?.value;
    let html;
    if (fresh || st === 'idle') html = startPane(v);
    else if (st === 'starting' || st === 'waiting') html = waitPane(v);
    else if (st === 'running') html = runPane(v);
    else if (st === 'done') html = bestPane(v);
    else html = endPane(v);
    left.innerHTML = html;
    left.dataset.ph = fresh ? 'idle' : st;
    if (keep !== undefined && $('#ctHost', left)) $('#ctHost', left).value = keep;
    paintClock();
  };

  const paintRight = (v, st) => {
    const showRows = !fresh && (st === 'running' || st === 'done' || ((st === 'failed' || st === 'stopped') && v.rows?.length));
    const mode = showRows ? 'rows' : (fresh ? 'idle' : st);
    const all = v.rows || [];
    /* At the end the list is the shortlist: what held and what wobbled. What
       did not connect is counted below it, not listed. */
    const final = st === 'done';
    const shown = showRows ? all.filter(r => !final || r.status === 'ok' || r.status === 'unstable') : [];

    if (mode !== rightMode) {
      rightMode = mode;
      rows.clear();
      list.innerHTML = showRows ? '' : emptyRight(mode);
      list.classList.toggle('rows', showRows);
    }

    // head
    const count = s => all.filter(r => r.status === s).length;
    const liveRows = all.filter(r => r.status !== 'skipped');
    const tried = liveRows.reduce((a, r) => a + (r.status === 'testing' ? r.tried : (r.total || 60)), 0);
    const whole = liveRows.reduce((a, r) => a + (r.total || 60), 0) || 1;
    const pct = Math.round((tried / whole) * 100);
    const head = !showRows
      ? `<div class="ct-rt"><b>Transports</b><small>${mode === 'waiting' || mode === 'starting' ? 'standing by' : 'none yet'}</small></div>`
      : st === 'running'
        ? `<div class="ct-rt"><b>Transports</b><small>${esc(v.kharej || '')}</small><span class="sp"></span><span class="pc">${pct}%</span></div>
           <div class="ct-track"><i style="--w:${pct}%"></i></div>`
        : `<div class="ct-rt"><b>${final ? 'Worth building on' : 'Transports'}</b><span class="sp"></span>
            <span class="tl-pill ok"><i></i>${count('ok')} held</span>
            <span class="tl-pill wr"><i></i>${count('unstable')} unstable</span></div>`;
    if (head !== headSig) {
      const bar = rHead.querySelector('.ct-track i');
      if (bar && head.includes('ct-track')) {
        bar.style.setProperty('--w', pct + '%');
        const pc = rHead.querySelector('.pc'); if (pc) pc.textContent = pct + '%';
      } else rHead.innerHTML = head;
      headSig = head;
    }

    // foot: what the shortlist leaves out
    const hidden = final ? count('down') + count('skipped') : 0;
    rFoot.hidden = !hidden;
    if (hidden) {
      rFoot.innerHTML = `<span class="tl-pill er"><i></i>${count('down')} down</span>
        ${count('skipped') ? `<span class="tl-pill"><i></i>${count('skipped')} skipped</span>` : ''}
        <span class="tl-meta">not listed — they did not hold on this path</span>`;
    }

    if (!showRows) return;
    if (final && !shown.length) {
      if (!list.querySelector('.ct-empty')) {
        list.innerHTML = `<div class="ct-empty"><b>No transport held</b><small>Every one of them was down or skipped on this path.</small></div>`;
        rows.clear();
      }
      return;
    }
    list.querySelector('.ct-empty')?.remove();

    /* Grouped by kind; best first once finished, in the order they were
       started while running so a row does not jump around under the eye. */
    const order = { ok: 0, unstable: 1, testing: 1, down: 2, skipped: 3 };
    const want = new Set(shown.map(keyOf));
    for (const [k, n] of rows) if (!want.has(k)) { n.remove(); rows.delete(k); }
    KINDS.forEach(g => {
      let items = shown.filter(r => r.kind === g.k);
      let grp = list.querySelector(`.ct-group[data-k="${g.k}"]`);
      if (!items.length) { grp?.remove(); return; }
      if (st !== 'running') {
        items = items.slice().sort((a, b) => (order[a.status] - order[b.status]) || ((b.mbps || 0) - (a.mbps || 0)));
      }
      if (!grp) {
        grp = document.createElement('div');
        grp.className = 'ct-group';
        grp.dataset.k = g.k;
        grp.innerHTML = `<div class="ct-gh"><b>${g.label}</b><small>${g.note}</small><span class="ln"></span></div>`;
        const later = KINDS.slice(KINDS.indexOf(g) + 1).map(x => list.querySelector(`.ct-group[data-k="${x.k}"]`)).find(Boolean);
        list.insertBefore(grp, later || null);
      }
      items.forEach((r, i) => {
        let n = rows.get(keyOf(r));
        if (!n) {
          n = document.createElement('div');
          n.innerHTML = rowHTML();
          n.style.setProperty('--d', `${Math.min(i, 12) * 45}ms`);
          rows.set(keyOf(r), n);
        }
        fillRow(n, r);
        const at = grp.children[i + 1];
        if (at !== n) grp.insertBefore(n, at || null);
      });
    });
  };

  const paint = v => {
    last = v;
    const st = phase(v);
    stateEl.textContent = { idle: 'Ready', starting: 'Starting', waiting: 'Waiting', running: 'Testing',
      done: 'Finished', failed: 'Failed', stopped: 'Stopped' }[st] || st;
    stateEl.className = 'cnt ' + (ACTIVE.includes(st) ? 'live' : st === 'done' ? 'ok' : st === 'failed' ? 'er' : '');
    dockDot(st);
    const stp = stepper(fresh ? 'idle' : st);
    if (steps.innerHTML !== stp) steps.innerHTML = stp;
    paintLeft(v, st);
    paintRight(v, st);
  };

  const load = async () => {
    try { paint(await api.connTest()); } catch (e) { if (!last) oops(e); }
  };

  const schedule = () => {
    clearTimeout(timer);
    if (!alive) return;
    timer = setTimeout(async () => { if (!document.hidden) await load(); schedule(); },
      ACTIVE.includes(last?.state) ? 2000 : 8000);
  };

  view.addEventListener('click', async ev => {
    const b = ev.target.closest('button');
    if (!b || !view.contains(b)) return;
    if (b.dataset.p) {
      preset = b.dataset.p;
      b.parentElement.querySelectorAll('button').forEach(x => x.classList.toggle('on', x === b));
      return;
    }
    if (b.dataset.copy) {
      const src = $(b.dataset.copy, view);
      const ok = await copyText(src?.textContent.trim() || '');
      flashCopied(b.querySelector('span') || b, ok);
      if (!ok) toast('The browser would not copy it — select the line and copy it by hand.', true);
      return;
    }
    if (b.id === 'ctNew') { fresh = true; leftSig = ''; paint(last || { state: 'idle' }); return; }
    if (b.id === 'ctStart') {
      const host = $('#ctHost', view)?.value.trim();
      if (!host) { toast('Give this server’s address — it is what the kharej dials.', true); return; }
      b.disabled = true;
      b.innerHTML = `${ICON.play}Starting…`;
      try {
        fresh = false;
        paint(await api.connTestStart({ host, preset }));
        schedule();
      } catch (e) { oops(e); b.disabled = false; b.innerHTML = `${ICON.play}Start the test`; }
      return;
    }
    if (b.id === 'ctStop') {
      if (!await confirmBox({ title: 'Stop the test?', body: 'Every test tunnel on this server is taken down. The kharej notices on its own and stops too.', go: 'Stop' })) return;
      try { paint(await api.connTestStop()); toast('Stopping the test…'); } catch (e) { oops(e); }
      setTimeout(load, 1200);
    }
  });

  /* The dial moves every second between polls. */
  tick = setInterval(paintClock, 1000);

  load().then(schedule);
  ctx.setTeardown(() => { alive = false; clearTimeout(timer); clearInterval(tick); });
}

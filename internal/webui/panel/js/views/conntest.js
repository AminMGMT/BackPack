/* Connection Test — which transports survive the path to a kharej server.
 *
 * The panel runs on the Iran server, which is the side a test starts from and
 * the side that judges it, so this is that side. Three steps and the page is
 * built around them: start here, paste one line on the kharej, read the
 * verdict. Every tunnel under test is a row that fills as its echoes come
 * back, the way the menu's table does.
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
};

/* Which of the three steps a state is on. */
/* The test's phase — not a tunnel's state, which lib/tstate.js owns. */
const phase = v => v.state || 'idle';

const stepOf = st => (st === 'running' || st === 'done' ? (st === 'done' ? 3 : 2)
  : st === 'waiting' || st === 'starting' ? 1 : 0);

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

function startForm(v) {
  const host = v.host || v.defaultHost || '';
  return `<div class="tl-card ct-start">
    <div class="tl-lede">
      <b>Every transport, tried for real between this server and one kharej.</b>
      <span>About three minutes. The test tunnels run on free ports and are removed when it ends —
        nothing is left behind on either server.</span>
    </div>
    <div class="tl-form">
      <label class="tl-f"><span>This server's address</span>
        <input id="ctHost" type="text" value="${esc(host)}" placeholder="the IP the kharej dials" autocomplete="off" spellcheck="false"></label>
      <div class="tl-f"><span>Preset</span>
        <div class="tl-seg" id="ctPreset">${PRESETS.map(p =>
          `<button type="button" data-p="${p.v}" class="${p.v === preset ? 'on' : ''}">
             ${p.label}<small>${p.note}</small></button>`).join('')}</div></div>
    </div>
    ${v.root ? '' : `<div class="tl-note wr">The panel is not running as root, so the direct tunnels, PCK and the IP-spoofing check are left out.</div>`}
    <div class="tl-actions"><button class="tl-btn solid" id="ctStart">${ICON.play}Start the test</button></div>
  </div>`;
}

function countdown(deadline) {
  const left = Math.max(0, deadline - Math.floor(Date.now() / 1000));
  const m = Math.floor(left / 60), s = left % 60;
  return `${m}:${String(s).padStart(2, '0')}`;
}

function waitingCard(v) {
  const total = 15 * 60;
  const left = Math.max(0, (v.deadline || 0) - Math.floor(Date.now() / 1000));
  const frac = Math.min(1, left / total);
  return `<div class="tl-card ct-wait">
    <div class="ct-wait-head">
      <div class="ct-ring" style="--p:${frac.toFixed(3)}"><svg viewBox="0 0 44 44"><circle class="trk" cx="22" cy="22" r="19"/>
        <circle class="arc" cx="22" cy="22" r="19"/></svg><span id="ctLeft">${countdown(v.deadline || 0)}</span></div>
      <div class="tl-lede"><b>${phase(v) === 'starting' ? 'Starting the test tunnels…' : 'Waiting for the kharej'}</b>
        <span>Run this on the kharej server, as root. It builds its end of every test tunnel,
          checks in here, and prints the same table when the test is over.</span></div>
    </div>
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
    </details>` : ''}
    <div class="tl-actions"><span class="tl-meta">Testing from <b>${esc(v.host || '')}</b> · ${esc(v.preset || 'turbo')}</span>
      <span class="sp"></span><button class="tl-btn ghost" id="ctStop">${ICON.stop}Stop</button></div>
  </div>`;
}

const tone = st => ({ ok: 'ok', unstable: 'wr', down: 'er', skipped: 'sk' }[st] || 'run');
const statusWord = st => ({ ok: 'Held', unstable: 'Unstable', down: 'Down', skipped: 'Skipped', testing: 'Testing' }[st] || st);

function row(r) {
  const total = r.total || 60;
  const testing = r.status === 'testing';
  const share = testing ? (r.tried / total) : (r.ok / total);
  const figs = [
    r.rtt ? `<span><b>${r.rtt}</b> ms</span>` : '',
    r.mbps ? `<span><b>${r.mbps.toFixed(r.mbps < 10 ? 1 : 0)}</b> Mb/s</span>` : '',
  ].join('');
  return `<div class="ct-row ${tone(r.status)}">
    <span class="dot"></span>
    <span class="nm"><b>${esc(r.name)}</b>${r.detail ? `<small title="${esc(r.detail)}">${esc(r.detail)}</small>` : ''}</span>
    <span class="bar"><i style="--w:${(Math.max(0, Math.min(1, share)) * 100).toFixed(1)}%"></i></span>
    <span class="ec">${r.status === 'skipped' ? '—' : `${testing ? r.tried : r.ok}/${total}`}</span>
    <span class="fg">${figs}</span>
    <span class="st">${statusWord(r.status)}</span>
  </div>`;
}

function board(v) {
  const rows = v.rows || [];
  if (!rows.length) return '';
  const count = st => rows.filter(r => r.status === st).length;
  const live = rows.filter(r => r.status !== 'skipped');
  const tried = live.reduce((a, r) => a + (r.status === 'testing' ? r.tried : (r.total || 60)), 0);
  const all = live.reduce((a, r) => a + (r.total || 60), 0) || 1;
  const pct = Math.round((tried / all) * 100);
  const head = phase(v) === 'running'
    ? `<div class="ct-prog"><span class="lbl">Testing against <b>${esc(v.kharej || 'the kharej')}</b></span>
        <span class="sp"></span><span class="pc">${pct}%</span>
        <span class="track"><i style="--w:${pct}%"></i></span></div>`
    : `<div class="ct-tally">
        <span class="tl-pill ok"><i></i>${count('ok')} held</span>
        <span class="tl-pill wr"><i></i>${count('unstable')} unstable</span>
        <span class="tl-pill er"><i></i>${count('down')} down</span>
        ${count('skipped') ? `<span class="tl-pill"><i></i>${count('skipped')} skipped</span>` : ''}
        <span class="sp"></span>${v.kharej ? `<span class="tl-meta">with <b>${esc(v.kharej)}</b></span>` : ''}
      </div>`;
  /* Finished rows are read best first: what held, then what wobbled, then what
     did not — which is the order the question is asked in. */
  const order = { ok: 0, unstable: 1, testing: 1, down: 2, skipped: 3 };
  const groups = KINDS.map(g => {
    let list = rows.filter(r => r.kind === g.k);
    if (!list.length) return '';
    if (phase(v) !== 'running') {
      list = list.slice().sort((a, b) => (order[a.status] - order[b.status]) || ((b.mbps || 0) - (a.mbps || 0)));
    }
    return `<div class="ct-group"><div class="ct-gh"><b>${g.label}</b><small>${g.note}</small><span class="ln"></span></div>
      ${list.map(row).join('')}</div>`;
  }).join('');
  return `<div class="tl-card ct-board">${head}${groups}</div>`;
}

function bestCard(b) {
  if (!b) return '';
  const cell = (k, val, unit = '') => val ? `<div class="ct-bc"><span>${k}</span><b>${esc(String(val))}${unit ? `<em>${unit}</em>` : ''}</b></div>` : '';
  return `<div class="tl-card ct-best">
    <div class="ct-best-head"><span class="badge">Best for this path</span>
      <b class="tr">${esc(b.tr || '')}</b></div>
    <div class="ct-bgrid">
      ${cell('Speed', b.mb ? b.mb.toFixed(b.mb < 10 ? 1 : 0) : '', 'Mb/s')}
      ${cell('Round trip', b.rt, 'ms')}
      ${cell('Jitter', b.ji, 'ms')}
      ${cell('Worst', b.wo, 'ms')}
      ${cell('Loss', b.lo ? b.lo.toFixed(1) : (b.tr ? '0' : ''), '%')}
      ${cell('Preset', b.pr)}
      ${cell('Path MTU', b.pm)}
      ${cell('MSS', b.ms)}
      ${cell('Keepalive', b.ka, 's')}
      ${cell('Heartbeat', b.hb, 's')}
      ${b.fd ? cell('FEC', `${b.fd}+${b.fp}`) : ''}
    </div>
    <div class="tl-note">Build the tunnel with these from <b>Tunnels → Add tunnel</b>. The kharej printed the same verdict.</div>
  </div>`;
}

function endCard(v) {
  if (phase(v) === 'failed' || phase(v) === 'stopped') {
    return `<div class="tl-card ct-end ${phase(v) === 'failed' ? 'er' : ''}">
      <div class="tl-lede"><b>${phase(v) === 'failed' ? 'The test did not finish' : 'The test was stopped'}</b>
        ${v.error ? `<span>${esc(v.error)}</span>` : '<span>Every test tunnel has been removed.</span>'}</div>
      <div class="tl-actions"><button class="tl-btn solid" id="ctNew">${ICON.again}Start a new test</button></div></div>`;
  }
  if (phase(v) === 'done') {
    return `<div class="tl-actions ct-again"><span class="tl-meta">Finished ${v.finished ? new Date(v.finished * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : ''}</span>
      <span class="sp"></span><button class="tl-btn" id="ctNew">${ICON.again}Test again</button></div>`;
  }
  return '';
}

export function connTestView(ctx) {
  const view = $('#view');
  view.innerHTML = `<div class="tl-page ct">
    <div class="sech2 tl-head"><h2>Connection test</h2><span class="cnt" id="ctState">—</span><span class="sp"></span></div>
    <p class="tl-sub">Which transports actually survive the route between this Iran server and a kharej — measured, not guessed.</p>
    <div id="ctSteps"></div>
    <div id="ctBody"></div>
  </div>`;
  const body = $('#ctBody', view), steps = $('#ctSteps', view), stateEl = $('#ctState', view);
  let last = null, sig = '', fresh = false, timer = null, tick = null, alive = true;

  const dockDot = st => {
    const n = document.getElementById('dock-c');
    if (n) n.textContent = ACTIVE.includes(st) ? '●' : '';
  };

  const paint = v => {
    last = v;
    const st = v.state || 'idle';
    stateEl.textContent = { idle: 'Ready', starting: 'Starting', waiting: 'Waiting', running: 'Testing',
      done: 'Finished', failed: 'Failed', stopped: 'Stopped' }[st] || st;
    stateEl.className = 'cnt ' + (ACTIVE.includes(st) ? 'live' : st === 'done' ? 'ok' : st === 'failed' ? 'er' : '');
    dockDot(st);
    steps.innerHTML = stepper(fresh ? 'idle' : st);
    let html;
    if (fresh || st === 'idle') html = startForm(v);
    else if (st === 'starting' || st === 'waiting') html = waitingCard(v);
    else html = (st === 'done' ? bestCard(v.best) : '') + board(v) + endCard(v);
    /* Rebuilt only when what it draws changed, so a form being typed into is
       not wiped by the poll and a row's bar animates from where it was. */
    const s2 = JSON.stringify([fresh, st, v.rows, v.best, v.command, v.error, v.kharej]);
    if (s2 !== sig) {
      const keep = $('#ctHost', body)?.value;
      body.innerHTML = html;
      if (keep !== undefined && $('#ctHost', body)) $('#ctHost', body).value = keep;
      sig = s2;
    }
  };

  const load = async () => {
    try { paint(await api.connTest()); } catch (e) { if (!last) oops(e); }
  };

  const schedule = () => {
    clearTimeout(timer);
    if (!alive) return;
    const st = last?.state;
    timer = setTimeout(async () => { if (!document.hidden) await load(); schedule(); },
      ACTIVE.includes(st) ? 2000 : 8000);
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
    if (b.id === 'ctNew') { fresh = true; sig = ''; paint(last || { state: 'idle' }); return; }
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

  /* The countdown moves every second between polls. */
  tick = setInterval(() => {
    const el = $('#ctLeft', view);
    if (el && last?.deadline) {
      el.textContent = countdown(last.deadline);
      const left = Math.max(0, last.deadline - Math.floor(Date.now() / 1000));
      el.parentElement.style.setProperty('--p', Math.min(1, left / 900).toFixed(3));
    }
  }, 1000);

  load().then(schedule);
  ctx.setTeardown(() => { alive = false; clearTimeout(timer); clearInterval(tick); });
}

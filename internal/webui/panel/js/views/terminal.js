/* Terminal — a root shell on this server, in the page.
 *
 * The shell is opened on purpose, with a button, and not by visiting the
 * section: every one is written to the audit record and sent to the alerts, and
 * a door that opens itself whenever somebody clicks past it is a door that is
 * always open. Once it is open it outlives the section — leaving for Tunnels
 * and coming back finds the same shell where it was, the way a terminal window
 * behind another window does.
 *
 * The emulator is xterm.js, vendored under js/vendor (MIT). Its colours are
 * read from the panel's own tokens, so it is the same ground, the same ink and
 * the same accent as everything around it, and it follows the theme when the
 * theme changes.
 */

import { $, esc } from '../lib/dom.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { toast } from '../ui/toast.js';
import { Terminal } from '../vendor/xterm.js';
import { FitAddon } from '../vendor/addon-fit.js';

/* The one shell, kept between visits. */
let sess = null;   // { term, fit, ws, host, state, node }
let fontSize = 13;
try { fontSize = Number(localStorage.getItem('bp_term_fs')) || 13; } catch (e) {}

const css = n => getComputedStyle(document.body).getPropertyValue(n).trim();

/* The panel's palette, as an xterm theme. The sixteen ANSI colours are tuned
   per ground, because a yellow that reads on #070707 disappears on #f2f2f2. */
function theme() {
  const light = document.body.dataset.t === 'light';
  const ac = css('--ac') || css('--tx');
  const base = light ? {
    black: '#1a1a1a', red: '#c4282b', green: '#1f8a3b', yellow: '#9a6700', blue: '#1f5fbf',
    magenta: '#8e3fb8', cyan: '#0f7c8c', white: '#6b6b6b',
    brightBlack: '#555555', brightRed: '#e0393c', brightGreen: '#27a148', brightYellow: '#b67d00',
    brightBlue: '#2c74e0', brightMagenta: '#a64fd6', brightCyan: '#1596a8', brightWhite: '#0a0a0a',
  } : {
    black: '#1f1f1f', red: '#ff5f59', green: '#44d17a', yellow: '#f5b83d', blue: '#5ea1ff',
    magenta: '#c68aff', cyan: '#4fd1e0', white: '#d6d6d6',
    brightBlack: '#6a6a6a', brightRed: '#ff7b75', brightGreen: '#6ee79a', brightYellow: '#ffd06b',
    brightBlue: '#86b8ff', brightMagenta: '#d9a8ff', brightCyan: '#7ce3ee', brightWhite: '#fafafa',
  };
  return {
    ...base,
    background: light ? '#fbfbfb' : '#0a0a0a',
    foreground: css('--tx') || (light ? '#0a0a0a' : '#fafafa'),
    cursor: ac, cursorAccent: light ? '#ffffff' : '#0a0a0a',
    selectionBackground: light ? 'rgba(0,0,0,.14)' : 'rgba(255,255,255,.18)',
  };
}

const FONT = 'ui-monospace, "SF Mono", "JetBrains Mono", "Cascadia Code", Menlo, Consolas, "DejaVu Sans Mono", monospace';

function setState(state, note = '') {
  if (!sess) return;
  sess.phase = state;
  const root = $('#view .tm');
  if (!root) return;
  root.dataset.state = state;
  const s = root.querySelector('#tmState');
  if (s) s.textContent = { connecting: 'Connecting…', open: 'Connected', closed: note || 'Disconnected' }[state] || state;
}

function send(data) {
  if (sess?.ws?.readyState === 1) sess.ws.send(new TextEncoder().encode(data));
}

function connect() {
  const { term, fit } = sess;
  try { fit.fit(); } catch (e) {}
  const ws = new WebSocket(api.terminalURL(term.cols, term.rows));
  ws.binaryType = 'arraybuffer';
  sess.ws = ws;
  setState('connecting');
  ws.onopen = () => {
    setState('open');
    term.focus();
    ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
  };
  ws.onmessage = ev => {
    if (typeof ev.data === 'string') {
      let m = {};
      try { m = JSON.parse(ev.data); } catch (e) {}
      if (m.type === 'exit') {
        term.write(`\r\n\x1b[2m[the shell exited${m.code ? ` with ${m.code}` : ''}]\x1b[0m\r\n`);
        setState('closed', 'Shell exited');
      } else if (m.type === 'error') {
        term.write(`\r\n\x1b[31m${m.message}\x1b[0m\r\n`);
      }
      return;
    }
    term.write(new Uint8Array(ev.data));
  };
  ws.onclose = ev => {
    if (sess?.ws !== ws) return;
    if (sess.phase !== 'closed') {
      term.write(`\r\n\x1b[2m[disconnected${ev.reason ? `: ${ev.reason}` : ''}]\x1b[0m\r\n`);
      setState('closed');
    }
  };
}

function create(host) {
  const node = document.createElement('div');
  node.className = 'tm-xterm';
  const term = new Terminal({
    fontFamily: FONT, fontSize, lineHeight: 1.18, letterSpacing: 0,
    cursorBlink: true, cursorStyle: 'bar', cursorWidth: 2,
    scrollback: 5000, allowProposedApi: false, theme: theme(),
    macOptionIsMeta: true, rightClickSelectsWord: true,
  });
  const fit = new FitAddon();
  term.loadAddon(fit);
  term.onData(d => send(d));
  term.onBinary(d => {
    if (sess?.ws?.readyState === 1) sess.ws.send(Uint8Array.from(d, c => c.charCodeAt(0)));
  });
  term.onResize(({ cols, rows }) => {
    if (sess?.ws?.readyState === 1) sess.ws.send(JSON.stringify({ type: 'resize', cols, rows }));
  });
  sess = { term, fit, ws: null, host, phase: 'connecting', node, opened: false };
  return sess;
}

/* The row of keys a phone keyboard does not have. */
const KEYS = [
  ['Esc', '\x1b'], ['Tab', '\t'], ['Ctrl C', '\x03'], ['Ctrl D', '\x04'], ['Ctrl L', '\x0c'],
  ['↑', '\x1b[A'], ['↓', '\x1b[B'], ['←', '\x1b[D'], ['→', '\x1b[C'],
  ['|', '|'], ['/', '/'], ['-', '-'], ['~', '~'],
];

const SVG = {
  power: '<svg viewBox="0 0 24 24"><path d="M12 3v8"/><path d="M6.3 7.5a8 8 0 1011.4 0"/></svg>',
  again: '<svg viewBox="0 0 24 24"><path d="M3 12a9 9 0 0115.5-6.3L21 8"/><path d="M21 3v5h-5"/></svg>',
  clear: '<svg viewBox="0 0 24 24"><path d="M4 7h16"/><path d="M9 7V4h6v3"/><path d="M6 7l1 13h10l1-13"/></svg>',
  full: '<svg viewBox="0 0 24 24"><path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5"/></svg>',
  shield: '<svg viewBox="0 0 24 24"><path d="M12 3l8 3v6c0 4.5-3.4 8.3-8 9-4.6-.7-8-4.5-8-9V6z"/><path d="M9 12l2 2 4-4"/></svg>',
};

export function terminalView(ctx) {
  const view = $('#view');
  const host = store.get().stats?.hostname || 'this server';
  view.innerHTML = `<div class="tl-page tm" data-state="${sess ? sess.phase : 'idle'}">
    <div class="sech2 tl-head"><h2>Terminal</h2><span class="cnt" id="tmState">${sess ? '' : 'Not open'}</span><span class="sp"></span>
      <div class="tm-tools">
        <button class="tl-btn ghost sm" data-fs="-1" title="Smaller text">A−</button>
        <button class="tl-btn ghost sm" data-fs="1" title="Larger text">A+</button>
        <button class="tl-btn ghost sm" id="tmClear" title="Clear the screen">${SVG.clear}</button>
        <button class="tl-btn ghost sm" id="tmFull" title="Full screen">${SVG.full}</button>
        <button class="tl-btn sm" id="tmAgain" title="Open a new shell">${SVG.again}<span>New shell</span></button>
      </div></div>
    <div class="tm-win">
      <div class="tm-bar"><span class="lights"><i></i><i></i><i></i></span>
        <span class="ttl"><b>root@${esc(host)}</b><small>login shell</small></span>
        <span class="led" title="Connection"></span></div>
      <div class="tm-body" id="tmBody">
        <div class="tm-splash" id="tmSplash">
          <div class="glyph"><span>&gt;_</span></div>
          <b>A root shell on ${esc(host)}</b>
          <p>Everything you could do over SSH, from here. Opening one is written to the audit
            record and sent to the alerts, so it is never a secret that a shell was opened.</p>
          <button class="tl-btn solid" id="tmOpen">${SVG.power}Open a shell</button>
          <small class="fine">${SVG.shield}Browser sessions only — API tokens can never open it.</small>
        </div>
      </div>
      <div class="tm-keys" id="tmKeys">${KEYS.map(([l, s]) =>
        `<button type="button" data-k="${esc(s)}">${esc(l)}</button>`).join('')}</div>
    </div>
  </div>`;

  const body = $('#tmBody', view);
  const splash = $('#tmSplash', view);

  const mount = () => {
    splash.hidden = true;
    body.append(sess.node);
    if (!sess.opened) { sess.term.open(sess.node); sess.opened = true; }
    requestAnimationFrame(() => { try { sess.fit.fit(); } catch (e) {} sess.term.focus(); });
    setState(sess.phase);
  };

  if (sess) mount();

  const open = () => {
    if (sess) { try { sess.ws?.close(); } catch (e) {} sess.term.dispose(); sess.node.remove(); sess = null; }
    create(host);
    mount();
    connect();
  };

  view.addEventListener('click', ev => {
    const b = ev.target.closest('button');
    if (!b || !view.contains(b)) return;
    if (b.id === 'tmOpen' || b.id === 'tmAgain') { open(); return; }
    if (!sess) return;
    if (b.dataset.k !== undefined) { send(b.dataset.k); sess.term.focus(); return; }
    if (b.dataset.fs) {
      fontSize = Math.max(10, Math.min(20, fontSize + Number(b.dataset.fs)));
      try { localStorage.setItem('bp_term_fs', String(fontSize)); } catch (e) {}
      sess.term.options.fontSize = fontSize;
      try { sess.fit.fit(); } catch (e) {}
      return;
    }
    if (b.id === 'tmClear') { sess.term.clear(); sess.term.focus(); return; }
    if (b.id === 'tmFull') {
      const win = $('.tm-win', view);
      if (document.fullscreenElement) document.exitFullscreen();
      else win?.requestFullscreen?.().catch(() => toast('This browser will not go full screen here.', true));
    }
  });

  /* Size follows the window, and the colours follow the theme. */
  const ro = new ResizeObserver(() => { if (sess?.opened) { try { sess.fit.fit(); } catch (e) {} } });
  ro.observe(body);
  const mo = new MutationObserver(() => { if (sess) sess.term.options.theme = theme(); });
  mo.observe(document.body, { attributes: true, attributeFilter: ['data-t', 'data-accent'] });

  ctx.setTeardown(() => {
    ro.disconnect();
    mo.disconnect();
    /* The shell stays open; only its window is taken off the page. */
    sess?.node.remove();
  });
}

/* A tunnel's setup link, drawn — the block the menu prints after a wizard and
 * under Manage Tunnels → Setup Link: the link, and the one line that builds
 * the other end with it.
 *
 * Used by the Setup link dialog and by Add tunnel once a tunnel exists only on
 * this server. The markup carries its own classes (css/screens/tools.css), so
 * it looks the same wherever it is put.
 */

import { esc, copyText, flashCopied } from '../lib/dom.js';
import { toast } from './toast.js';

const COPY = '<svg viewBox="0 0 24 24"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 012-2h10"/></svg>';
const TERM = '<svg viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="16" rx="3"/><path d="M7 9l3 3-3 3"/><path d="M12.5 15H17"/></svg>';
const LINK = '<svg viewBox="0 0 24 24"><path d="M10 13a5 5 0 007.5.5l3-3a5 5 0 00-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 00-7.5-.5l-3 3a5 5 0 007 7L12.2 19"/></svg>';

let n = 0;
const line = (icon, text, label) => {
  const id = 'sl' + (++n);
  return `<div class="ct-cmd sl-line"><span class="pr">${icon}</span><code id="${id}">${esc(text)}</code>
    <button type="button" class="tl-btn" data-sl-copy="${id}" aria-label="Copy ${esc(label)}">${COPY}<span>Copy</span></button></div>`;
};

export function setupLinkHTML(info) {
  const kharej = info.for === 'kharej';
  return `<div class="sl">
    <div class="sl-steps">
      <span class="sl-side here"><b>This server</b><small>${info.kind === 'direct' ? 'direct' : 'reverse'} · built</small></span>
      <span class="sl-wire"><i></i></span>
      <span class="sl-side there"><b>${kharej ? 'Kharej' : 'Iran'}</b><small>build it with the ${kharej ? 'line' : 'link'} below</small></span>
    </div>
    ${kharej ? `
      <div class="sl-h"><b>On the kharej, as root</b><small>Backpack already installed</small></div>
      ${line(TERM, info.apply, 'the command')}
      <details class="ct-more"><summary><svg viewBox="0 0 24 24"><path d="M6 9l6 6 6-6"/></svg>The kharej has no Backpack yet</summary>
        <p>Installs Backpack and sets this tunnel up in one go.</p>
        ${line(TERM, info.install, 'the install command')}</details>
      <div class="sl-h"><b>Or paste the link</b><small>${esc(info.where)}</small></div>
      ${line(LINK, info.link, 'the link')}`
    : `<div class="sl-h"><b>On the Iran server</b><small>${esc(info.where)}</small></div>
      ${line(LINK, info.link, 'the link')}`}
    ${info.needsAddress ? `<div class="tl-note wr">This server's public address could not be found, so the other side will ask for it.</div>` : ''}
    <div class="tl-note">The link holds the tunnel's token — send it only to the server it is for.</div>
  </div>`;
}

/* One listener for the copy buttons inside root. */
export function bindSetupLink(root) {
  root.addEventListener('click', async ev => {
    const b = ev.target.closest('[data-sl-copy]');
    if (!b || !root.contains(b)) return;
    const ok = await copyText(root.querySelector('#' + b.dataset.slCopy)?.textContent.trim() || '');
    flashCopied(b.querySelector('span') || b, ok);
    if (!ok) toast('The browser would not copy it — select the line and copy it by hand.', true);
  });
}

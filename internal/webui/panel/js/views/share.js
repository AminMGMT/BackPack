/* A tunnel's setup link — what builds its other end.
 *
 * CLI: Manage → Manage Tunnels → Setup Link.
 */

import { esc } from '../lib/dom.js';
import { flag } from '../lib/format.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { oops } from '../ui/toast.js';
import { setupLinkHTML, bindSetupLink } from '../ui/setuplink.js';

export function shareView(ctx) {
  const name = ctx.params.name;
  openScreen('share', {
    pick: '.dlg.share',
    bind: async (root, close) => {
      const t = store.tunnel(name);
      const fl = root.querySelector('.dh .fl');
      if (fl) fl.textContent = flag(t?.peerCountry) || flag(t?.country) || '·';
      const sub = root.querySelector('.dh .ttl small');
      if (sub) sub.textContent = name;
      const body = root.querySelector('#shareBody');
      bindSetupLink(body);

      const draw = async host => {
        try {
          const info = await api.tunnelLink(name, host);
          body.innerHTML = setupLinkHTML(info) + `
            <div class="sh-host"><label for="shHost">Address it names</label>
              <input id="shHost" value="${esc(info.host || '')}" placeholder="this server's IP or domain" spellcheck="false">
              <button type="button" id="shRedo">Rebuild</button></div>`;
        } catch (e) {
          body.innerHTML = `<div class="sh-wait">${esc(e.message)}</div>`;
        }
      };
      body.addEventListener('click', ev => {
        if (ev.target.closest('#shRedo')) draw(body.querySelector('#shHost')?.value.trim());
      });
      await draw('');
      ctx.setTeardown(close);
    },
  }).catch(oops);
}

/* Settings: panel access, security, the bot, and the release channel.
 *
 * CLI: 5 Web Panel, 7 Telegram Bot, 8 Update → Release channel.
 */

import { $$, esc } from '../lib/dom.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { oops, toast } from '../ui/toast.js';
import { confirmBox } from '../ui/confirm.js';

/* A <select> keeps its first option when the value it is handed matches none
   of them, which is how "turbo" quietly displayed as "Balanced". The server
   speaks in values ("turbo", "wss"); the preview's options were written with
   labels. Match either, and say so when neither fits. */
function setControl(node, v) {
  if (node.type === 'checkbox') { node.checked = !!v; return; }
  const val = Array.isArray(v) ? v.join(', ') : String(v ?? '');
  if (node.tagName !== 'SELECT') { node.value = val; return; }
  const want = val.trim().toLowerCase();
  const hit = [...node.options].find(o =>
    o.value.trim().toLowerCase() === want || o.textContent.trim().toLowerCase() === want);
  if (hit) { node.value = hit.value; return; }
  /* Nothing matched: show the server's value rather than a wrong one. */
  const opt = new Option(val, val, true, true);
  node.add(opt);
}

const dig = (o, path) => path.split('.').reduce((x, k) => (x ?? {})[k], o);

function fill(root, values, prefix = '') {
  /* Controls carry the key their endpoint reads, dotted where the payload
     nests (alerts.cpu), so one pass fills all four endpoints' fields. */
  root.querySelectorAll('[name]').forEach(n => {
    const v = dig(values, n.name);
    if (v === undefined || v === null) return;
    setControl(n, v);
  });
  for (const [k, v] of Object.entries(values || {})) {
    if (v && typeof v === 'object' && !Array.isArray(v)) { fill(root, v, k + '.'); continue; }
    const key = prefix + k;
    const node = root.querySelector(`[name="${key}"], [name="${k}"], #${CSS.escape(k)}`);
    if (!node) continue;
    if (node.type === 'checkbox') node.checked = !!v;
    else if (node.classList?.contains('sw6') || node.classList?.contains('switch')) {
      node.classList.toggle('on', !!v);
      node.setAttribute('aria-pressed', String(!!v));
    } else node.value = v ?? '';
  }
}

/* The rail's one-line summaries and the version list were drawn with example
   values. They are rewritten from what the server reports, because a summary
   that is decoration is a summary that lies the first time something changes. */
function summarise(root, { stats, sec, tg, ch, ses, ab, upd }) {
  const say = (label, text) => {
    for (const b of root.querySelectorAll('b')) {
      if (b.textContent.trim() !== label) continue;
      const line = b.nextElementSibling;
      if (line && text) line.textContent = text;
      return b;
    }
  };
  say('Panel access', [
    stats?.panelPort ? 'Port ' + stats.panelPort : null,
    location.protocol === 'https:' ? 'HTTPS' : 'Plain HTTP',
  ].filter(Boolean).join(' · '));
  say('Security', [
    sec ? (sec.twoFA ? '2FA on' : '2FA off') : null,
    ses ? `${ses.length} device${ses.length === 1 ? '' : 's'}` : null,
  ].filter(Boolean).join(' · '));
  say('Telegram bot', tg ? (tg.configured ? 'Connected' : 'Not configured') : null);
  /* The rail is one short line per group; a full sentence wraps and pushes the
     rest of the list down, so the summary is the fact, not the explanation. */
  const when = t => {
    const d = new Date(t), day = 864e5;
    const gap = Date.now() - d.getTime();
    if (gap < day) return 'today ' + d.toTimeString().slice(0, 5);
    if (gap < 2 * day) return 'yesterday ' + d.toTimeString().slice(0, 5);
    return `${Math.floor(gap / day)} days ago`;
  };
  say('Backup', ab?.last ? when(ab.last) : 'never taken');
  const tag = upd?.summary?.match(/v?[\d.]+/)?.[0];
  const u = say('Update', upd?.available ? `${tag} available` : 'up to date');
  if (u) {
    const dot = u.parentElement?.querySelector('.dot, .badge');
    if (dot) dot.hidden = !upd?.available;
  }
}

export function settingsView(ctx) {
  openScreen('settings', {
    pick: '.dlg',
    bind: async (root, close) => {
      const [sec, tg, ch, ses, ab, upd] = await Promise.allSettled([
        api.security(), api.telegram(), api.channel(), api.sessions(),
        api.autoBackup(), api.updateCheck(),
      ]);
      const val = r => (r.status === 'fulfilled' ? r.value : null);
      if (sec.status === 'fulfilled') fill(root, sec.value);
      if (tg.status === 'fulfilled') fill(root, tg.value);
      if (ch.status === 'fulfilled') fill(root, ch.value);
      summarise(root, {
        stats: store.get().stats, sec: val(sec), tg: val(tg), ch: val(ch),
        ses: val(ses), ab: val(ab), upd: val(upd),
      });

      /* Sessions are a list, not a field. The preview drew two example rows;
         they are replaced by the real ones, or by a line saying there is one. */
      if (ses.status === 'fulfilled') {
        const first = [...root.querySelectorAll('b')].find(b => b.textContent.trim() === 'This device');
        const box = first?.closest('.dl, .list, .rows') || root.querySelector('.sessions, #sessions');
        if (box) {
          box.innerHTML = (ses.value || []).map(x => `
            <div class="srow"><div><b>${esc(x.current ? 'This device' : (x.ip || 'unknown'))}</b>
              <span>${esc([x.ip, x.created].filter(Boolean).join(' · '))}</span></div>
              ${x.current ? '' : `<button class="btn6" data-revoke="${esc(x.id)}">Sign out</button>`}
            </div>`).join('');
          box.addEventListener('click', async ev => {
            const b = ev.target.closest('[data-revoke]');
            if (!b) return;
            try { await api.remoteToken('revoke'); toast('Signed out.'); } catch (e) { oops(e); }
          });
        }
      }

      /* The address the panel would answer on: this host, over https — the
         preview showed a made-up domain. */
      const uHost = root.querySelector('#uHost');
      if (uHost) uHost.textContent = location.hostname;
      const uScheme = root.querySelector('#uScheme');
      if (uScheme) uScheme.textContent = 'https://';

      /* The rail and the accordions are the preview's own handlers, rebound in
         screen.js; only the starting pane is decided here. */
      root.querySelectorAll('[data-p]').forEach(p => { p.hidden = p.dataset.p !== 'update'; });

      /* "Where you are": what runs now, and the versions there are restore
         points for. The preview listed three by hand. */
      if (upd.status === 'fulfilled') {
        const runningTag = store.get().stats?.version || '';
        const pts = await api.restorePoints().catch(() => []);
        const rows = [];
        const next = upd.value?.summary?.match(/v?[\d.]+/)?.[0];
        if (upd.value?.available && next) {
          rows.push([next, 'available',
            'Not installed yet. A restore point is taken before it installs.']);
        }
        rows.push([runningTag, 'running', 'What every tunnel here is running on.']);
        for (const p of pts.slice(0, 3)) {
          if (p.version === runningTag) continue;
          rows.push([p.version, '', `Restore point ${p.stamp} kept`]);
        }
        const first = root.querySelector('.ev');
        const list = first?.parentElement;
        if (list) {
          list.innerHTML = rows.map(([v, tagName, note]) => `
            <div class="ev ${tagName === 'available' ? 'next' : tagName === 'running' ? 'now' : ''}">
              <b>${esc(v)}${tagName ? `<span class="tag3">${esc(tagName)}</span>` : ''}</b>
              <span>${esc(note)}</span></div>`).join('');
        }
      }

      /* Restore points: the panel can list them and nothing more. RestoreSnapshot
         is reachable from the Telegram bot, not over HTTP, so a "Roll back"
         button here would be a button that cannot work. */
      {
        const label = [...root.querySelectorAll('.gl2')]
          .find(g => g.textContent.trim().toLowerCase() === 'restore points');
        const box = label?.parentElement;
        if (box) {
          const pts = await api.restorePoints().catch(() => []);
          box.innerHTML = `<div class="gl2">Restore points</div>` + (pts.length
            ? pts.map(x => `<div class="arow"><div class="tx">
                 <b>Before ${esc(x.version)}</b>
                 <span>${esc(new Date(x.created).toLocaleDateString())} · ${x.tunnels.length} tunnels</span>
               </div></div>`).join('')
            : `<div class="arow"><div class="tx"><b>None yet</b>
                 <span>One is taken automatically before every update.</span></div></div>`) +
            `<div class="hint">Rolling one back is done from the Telegram bot, under
             ♻️ Restore points — the panel lists them and has no endpoint that puts one back.</div>`;
        }
      }

      byText(/^revoke$/i).forEach(bt => bt.addEventListener('click', async () => {
        if (!await confirmBox({
          title: 'Revoke the token?',
          body: 'Every peer and scraper using it loses access.',
          go: 'Revoke', danger: true,
        })) return;
        try { await api.remoteToken('revoke'); toast('Token revoked.'); } catch (e) { oops(e); }
      }));

      byText(/sign out all other devices|^sign out$/i).forEach(bt => bt.addEventListener('click', async () => {
        if (!await confirmBox({ title: 'Sign out every other device?',
          body: 'This session stays signed in. Every other one is ended at once.',
          go: 'Sign out' })) return;
        try { await api.remoteToken('others'); toast('Every other device was signed out.'); }
        catch (e) { oops(e); }
      }));

      /* The buttons the preview drew without handlers, each on the endpoint
         that actually does the thing. Anything the panel has no endpoint for is
         left out rather than wired to nothing. */
      const byText = re => [...root.querySelectorAll('button')]
        .filter(b => re.test(b.textContent.trim()));

      byText(/^install/i).forEach(b => b.addEventListener('click', async () => {
        if (!await confirmBox({
          title: 'Install the update?',
          body: 'A restore point is saved first, and it rolls itself back if the tunnels do not come back up.',
          go: 'Install',
        })) return;
        b.disabled = true;
        try { await api.updateStart(); toast('Update started — watch it in Maintenance.'); }
        catch (e) { oops(e); }
      }));

      byText(/^changelog$/i).forEach(b => b.addEventListener('click', () =>
        window.open('https://github.com/AminMGMT/BackPack/releases', '_blank', 'noopener')));

      byText(/^download$/i).forEach(b => b.addEventListener('click', () => {
        location.href = api.backupExportURL();
      }));

      byText(/^apply certificate$/i).forEach(b => b.addEventListener('click', async () => {
        const domain = root.querySelector('[name="domain"]')?.value.trim();
        const email = root.querySelector('[name="email"]')?.value.trim();
        try { await api.panelCert({ domain, email }); toast('Certificate requested.'); }
        catch (e) { oops(e); }
      }));

      byText(/^generate$/i).forEach(b => b.addEventListener('click', async () => {
        try {
          const r = await api.remoteToken('new');
          const box = b.closest('div')?.querySelector('input, code');
          if (box && r.token) { if ('value' in box) box.value = r.token; else box.textContent = r.token; }
          toast('A new token was issued.');
        } catch (e) { oops(e); }
      }));

      /* The port and the password both end this session, so both ask first. */
      byText(/^(save|apply)$/i).forEach(b => b.addEventListener('click', async () => {
        const port = root.querySelector('[name="port"]')?.value;
        const pw = root.querySelector('[name="password"]')?.value;
        const again = root.querySelector('[name="confirm"]')?.value;
        try {
          if (pw) {
            if (pw !== again) return toast('The two passwords do not match.', true);
            if (!await confirmBox({
              title: 'Change the panel password?',
              body: 'Every signed-in device is signed out, including this one.',
              go: 'Change it', danger: true,
            })) return;
            await api.setPassword({ password: pw });
            toast('Password changed — signing you out.');
            setTimeout(() => { location.href = '/logout'; }, 1200);
            return;
          }
          if (port) {
            if (!await confirmBox({
              title: `Move the panel to port ${port}?`,
              body: 'You will be redirected to the new address.',
              go: 'Move',
            })) return;
            await api.setPanelPort(port);
            toast('Moving — the panel restarts on the new port.');
          }
        } catch (e) { oops(e); }
      }));

      byText(/^send test$/i).forEach(bt => bt.addEventListener('click', async () => {
        bt.disabled = true;
        try { await api.telegramTest(); toast('Test report sent.'); } catch (e) { oops(e); }
        bt.disabled = false;
      }));

      /* The bot's own settings save separately from the panel's. */
      byText(/^(save bot|connect|update bot)$/i).forEach(bt => bt.addEventListener('click', async () => {
        const get = n => root.querySelector(`[name="${n}"]`)?.value?.trim() || '';
        try {
          await api.telegramSave({
            token: get('token'), adminId: get('adminId'),
            intervalHours: Number(get('intervalHours')) || 6,
            alerts: { cpu: Number(get('alerts.cpu')) || 0,
                      mem: Number(get('alerts.mem')) || 0,
                      disk: Number(get('alerts.disk')) || 0 },
          });
          toast('Bot settings saved.');
        } catch (e) { oops(e); }
      }));

      ctx.setTeardown(close);
    },
  }).catch(oops);
}

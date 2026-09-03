/* Settings: panel access, security, the bot, and the release channel.
 *
 * CLI: 5 Web Panel, 7 Telegram Bot, 8 Update → Release channel.
 */

import { $$, el, esc, dialogSubtitle } from '../lib/dom.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { oops, toast } from '../ui/toast.js';
import { confirmBox } from '../ui/confirm.js';
import { isUp } from '../lib/tstate.js';

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
      dialogSubtitle(root, store.get().stats,
        'changes apply as you make them unless a control says otherwise');
      const [sec, tg, ch, ses, ab, upd] = await Promise.allSettled([
        api.security(), api.telegram(), api.channel(), api.sessions(),
        api.autoBackup(), api.updateCheck(),
      ]);
      const val = r => (r.status === 'fulfilled' ? r.value : null);

      /* Buttons matched by what they say, because the preview gave most of them
         no id. Declared here rather than beside its first use: it was a const
         further down the file and two calls sat above it, so the whole bind
         threw on "Cannot access 'byText' before initialization" — and every
         control after that line, from Revoke to Save bot, was never wired. */
      const byText = re => [...root.querySelectorAll('button')]
        .filter(b => re.test(b.textContent.trim()));

      /* Two panes both call their button "Save" — the panel's port and the
         Telegram bot's settings. Matching on the word alone gave both of them
         the port handler, so the bot's token, admin and thresholds had no way
         to be saved at all, and pressing Save under Telegram with a port typed
         in would have moved the panel. Panes are how they are told apart. */
      const inPane = k => root.querySelector(`[data-p="${k}"]`);
      const byTextIn = (k, re) => {
        const pane = inPane(k);
        return pane ? [...pane.querySelectorAll('button')].filter(b => re.test(b.textContent.trim())) : [];
      };

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
        /* The rows are the preview's two invented devices — "Chrome on macOS ·
           Tehran · now" and an iPhone — until they are replaced. They were
           looked for under .dl/.list/.rows, and this markup has none of those,
           so the replacement never ran and every panel showed the same two
           strangers as its signed-in devices. They are .arow inside the group
           the heading names. */
        const group = [...root.querySelectorAll('[data-p="security"] .grp2')]
          .find(g => /signed-in devices/i.test(g.querySelector('.gl2')?.textContent || ''));
        if (group) {
          const rows = ses.value || [];
          group.querySelectorAll('.arow').forEach(r => r.remove());
          const others = group.querySelector('button');
          const html = rows.length ? rows.map(x => `
            <div class="arow"><div class="tx">
              <b>${esc(x.current ? 'This device' : (x.ip || 'unknown address'))}</b>
              <span>${esc([x.ip, x.created].filter(Boolean).join(' · ') || 'signed in')}</span>
            </div>${x.current
              ? '<button class="btn2" disabled>Current</button>'
              : `<button class="btn2 dgr" data-revoke="${esc(x.id)}">Sign out</button>`}</div>`).join('')
            : `<div class="arow"><div class="tx"><b>No other device is signed in</b>
                 <span>Only this one.</span></div></div>`;
          if (others) others.insertAdjacentHTML('beforebegin', html);
          else group.insertAdjacentHTML('beforeend', html);
          if (others) others.hidden = !rows.some(x => !x.current);

          group.addEventListener('click', async ev => {
            const b = ev.target.closest('[data-revoke]');
            if (!b) return;
            try {
              await api.sessionRevoke(b.dataset.revoke);
              toast('Signed out.');
              b.closest('.arow')?.remove();
            } catch (e) { oops(e); }
          });
        }
      }

      /* The certificate section, which was a drawing.
       *
       * Three options are drawn — plain HTTP, self-signed, Let's Encrypt — and
       * none of them was a control: nothing selected one, nothing read one, and
       * Apply posted a domain and an email with no mode at all, which is the one
       * field the endpoint cannot do without. Every attempt came back "unknown
       * mode", so the panel could not be put behind a certificate of any kind
       * from this screen, Let's Encrypt included. The address above it was the
       * preview's made-up domain on the preview's port.
       */
      const certOpts = [...root.querySelectorAll('.opt2[data-mode]')];
      let certMode = 'self';
      /* Held, not looked up: the label changes with the mode, and finding it by
         its text again afterwards finds nothing. */
      const applyCertBtn = byText(/^apply certificate$/i)[0];

      const paintCert = snap => {
        certOpts.forEach(o => o.classList.toggle('on', o.dataset.mode === certMode));
        /* A domain belongs to Let's Encrypt; on self-signed the same field is an
           optional extra name, and on plain HTTP it means nothing at all. */
        const domRow = root.querySelector('[name="domain"]')?.closest('.f2b');
        const mailRow = root.querySelector('[name="email"]')?.closest('.f2b');
        if (domRow) domRow.hidden = certMode === 'http';
        if (mailRow) mailRow.hidden = certMode !== 'acme';
        if (applyCertBtn) {
          applyCertBtn.textContent = certMode === 'http' ? 'Turn HTTPS off' : 'Apply certificate';
        }

        if (!snap) return;
        /* "-" is what the server reports when it could not work out its own
           public address; the address the operator actually reached this page
           on is a better answer than a dash. */
        const pub = snap.publicIp && snap.publicIp !== '-' ? snap.publicIp : '';
        const host = snap.domain || pub || location.hostname;
        const sch = certMode === 'http' ? 'http://' : 'https://';
        const uScheme = root.querySelector('#uScheme');
        const uHost = root.querySelector('#uHost');
        const uPort = root.querySelector('#uPort');
        if (uScheme) uScheme.textContent = sch;
        if (uHost) uHost.textContent = host;
        if (uPort) uPort.textContent = snap.port ? ':' + snap.port : '';
        const lock = root.querySelector('#lockw');
        if (lock) lock.className = 'lockw ' + (certMode === 'acme' ? 'safe' : certMode === 'self' ? 'warn' : 'off');
        const note = root.querySelector('#uNote');
        if (note) {
          note.innerHTML = certMode === 'acme'
            ? `<b>Trusted by every browser.</b> ${esc(snap.acmeNote || '')}`
            : certMode === 'self'
              ? '<b>The browser warns once.</b> It works on a bare IP, and the warning '
                + 'is accepted per device.'
              : '<b>No certificate.</b> The page travels in the clear and cannot be '
                + 'installed as an app.';
        }
        /* An option that cannot work here says so rather than failing after the
           restart, which is when the panel is already unreachable. */
        const acme = certOpts.find(o => o.dataset.mode === 'acme');
        if (acme) {
          const why = acme.querySelector('i');
          if (!snap.acmePath) {
            acme.classList.add('off');
            if (why) why.textContent = snap.acmeNote || 'Let’s Encrypt cannot verify this server right now.';
          } else if (why) {
            why.textContent = 'A trusted certificate, renewed on its own. ' + (snap.acmeNote || '');
          }
        }
      };

      let certSnap = null;
      try {
        certSnap = await api.panelCertRead();
        certMode = certSnap.mode || 'self';
        const d = root.querySelector('[name="domain"]');
        if (d) d.value = certSnap.domain || certSnap.selfHost || '';
        const e2 = root.querySelector('[name="email"]');
        if (e2) e2.value = certSnap.email || '';
        const pp = root.querySelector('[name="port"]');
        if (pp && certSnap.port) pp.value = certSnap.port;
      } catch (e) { /* the section still selects, it just starts on self-signed */ }
      certOpts.forEach(o => o.addEventListener('click', () => {
        certMode = o.dataset.mode;
        paintCert(certSnap);
      }));
      paintCert(certSnap);

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

      /* The token has one place it is shown, and it is not inside the button's
         own row: the preview put it in the row below, with a made-up value that
         every panel displayed as if it were theirs. It is read on open, written
         when one is issued and cleared when one is revoked, and the Copy beside
         it copies whatever is actually there. */
      const tokenBox = () => root.querySelector('[data-p="security"] .mono2');
      const showToken = t => {
        const box = tokenBox();
        if (!box) return;
        box.textContent = t || 'none issued yet';
        box.dataset.token = t || '';
      };
      api.remoteTokenRead().then(r => showToken(r && r.token)).catch(() => showToken(''));

      const tokenRow = tokenBox()?.closest('.arow');
      tokenRow?.querySelector('button')?.addEventListener('click', async () => {
        const t = tokenBox()?.dataset.token || '';
        if (!t) return toast('There is no token to copy yet.', true);
        try { await navigator.clipboard.writeText(t); toast('Token copied.'); }
        catch (e) { toast('There is nothing on the clipboard.', true); }
      });

      byText(/^revoke$/i).forEach(bt => bt.addEventListener('click', async () => {
        if (!await confirmBox({
          title: 'Revoke the token?',
          body: 'Every peer and scraper using it loses access.',
          go: 'Revoke', danger: true,
        })) return;
        try { await api.remoteToken('revoke'); showToken(''); toast('Token revoked.'); } catch (e) { oops(e); }
      }));

      /* Only the one that means every other device. The rows painted above have
         a "Sign out" of their own and their own handler, and matching that text
         here as well gave one device's button the meaning of all of them. */
      byText(/sign out all other devices/i).forEach(bt => bt.addEventListener('click', async () => {
        if (!await confirmBox({ title: 'Sign out every other device?',
          body: 'This session stays signed in. Every other one is ended at once.',
          go: 'Sign out' })) return;
        try { await api.sessionRevokeOthers(); toast('Every other device was signed out.'); }
        catch (e) { oops(e); }
      }));

      /* The buttons the preview drew without handlers, each on the endpoint
         that actually does the thing. Anything the panel has no endpoint for is
         left out rather than wired to nothing. */

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

      applyCertBtn?.addEventListener('click', async () => {
        const domain = root.querySelector('[name="domain"]')?.value.trim() || '';
        const email = root.querySelector('[name="email"]')?.value.trim() || '';
        if (certMode === 'acme' && !domain) return toast('Let’s Encrypt needs a domain pointed at this server.', true);
        if (!await confirmBox({
          title: certMode === 'http' ? 'Serve the panel over plain HTTP?'
               : certMode === 'self' ? 'Use a self-signed certificate?'
               : 'Get a certificate from Let’s Encrypt?',
          body: 'The panel restarts and its address changes. This page follows it; '
              + 'if it does not, open the address shown above.',
          go: 'Apply', danger: certMode === 'http',
        })) return;
        try {
          const r = await api.panelCert({ mode: certMode, domain, email });
          if (r.status === 'unchanged') return toast('That is already how the panel is served.');
          toast(r.issues
            ? 'Asking Let’s Encrypt for a certificate — the panel is restarting.'
            : 'Applied — the panel is restarting.');
          /* It answers before it restarts, so the new address is known here and
             nowhere else: without this the browser sits on an address that has
             just stopped answering. */
          if (r.url) setTimeout(() => { location.href = r.url; }, 2500);
        } catch (e) { oops(e); }
      });

      byText(/^generate$/i).forEach(b => b.addEventListener('click', async () => {
        try {
          const r = await api.remoteToken('generate');
          showToken(r && r.token);
          toast('A new token was issued.');
        } catch (e) { oops(e); }
      }));

      /* The port and the password both end this session, so both ask first. */
      byTextIn('access', /^(save|apply)$/i).forEach(b => b.addEventListener('click', async () => {
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

      /* Its own button, because it sits in its own group with its own two
         fields: the generic Save is the panel-access one, and matching on
         "save" alone left this one bound to nothing at all. */
      byText(/^save password$/i).forEach(b => b.addEventListener('click', async () => {
        const pw = root.querySelector('[name="password"]')?.value || '';
        const again = root.querySelector('[name="confirm"]')?.value || '';
        if (!pw) return toast('Type the new password first.', true);
        if (pw !== again) return toast('The two passwords do not match.', true);
        if (!await confirmBox({
          title: 'Change the panel password?',
          body: 'Every signed-in device is signed out, including this one.',
          go: 'Change it', danger: true,
        })) return;
        try {
          await api.setPassword({ password: pw });
          toast('Password changed — signing you out.');
          setTimeout(() => { location.href = '/logout'; }, 1200);
        } catch (e) { oops(e); }
      }));

      /* Restoring needs a file, and the screen was drawn without an input to
         pick one — so the button opens one made here. */
      byText(/^restore/i).forEach(b => b.addEventListener('click', () => {
        const pick = el('input', { type: 'file', accept: '.tar.gz,.tgz,application/gzip' });
        pick.style.display = 'none';
        root.append(pick);
        pick.addEventListener('change', async () => {
          const file = pick.files && pick.files[0];
          if (!file) return;
          if (!await confirmBox({
            title: 'Restore from this backup?',
            body: 'It replaces the tunnels, the panel password and the bot settings on this '
                + 'server with the ones in the file, and restarts the tunnels.',
            lines: [{ text: file.name }],
            go: 'Restore', danger: true,
          })) return;
          try { await api.backupImport(file); toast('Restored — the tunnels are coming back up.'); }
          catch (e) { oops(e); }
          pick.remove();
        });
        pick.click();
      }));

      byText(/^send test$/i).forEach(bt => bt.addEventListener('click', async () => {
        bt.disabled = true;
        try { await api.telegramTest(); toast('Test report sent.'); } catch (e) { oops(e); }
        bt.disabled = false;
      }));

      /* The bot's own settings save separately from the panel's. */
      byTextIn('telegram', /^(save|save bot|connect|update bot)$/i).forEach(bt => bt.addEventListener('click', async () => {
        const get = n => root.querySelector(`[name="${n}"]`)?.value?.trim() || '';
        try {
          /* Flat, and under the handler's own names. A nested `alerts` object
             was stringified into one "[object Object]" field, so the three
             thresholds on this screen were posted and read by nothing. */
          await api.telegramSave({
            token: get('token'), adminId: get('adminId'),
            intervalHours: Number(get('intervalHours')) || 6,
            alertCPU: Number(get('alerts.cpu')) || 0,
            alertMem: Number(get('alerts.mem')) || 0,
            alertDisk: Number(get('alerts.disk')) || 0,
          });
          toast('Bot settings saved.');
        } catch (e) { oops(e); }
      }));

      /* The switches and the menus were drawings.
       *
       * Nine of them: two-factor, the sign-in notice, the three Telegram
       * notices, the bot language, the relay, the update channel and the weekly
       * backup. Every one rendered, none did anything — api.setChannel and
       * api.setAutoBackup were never called from anywhere, /api/security was
       * only ever read, and telegramSave posted JSON at a handler that reads
       * form values, so it reported success and changed nothing.
       *
       * Each control now carries the handler's own form key as data-name, and
       * saves the moment it is used: a settings screen with no Save button
       * should not have settings that need one.
       */
      const ctlOf = n => root.querySelector(`[data-name="${n}"]`);
      const isOn = n => !!ctlOf(n)?.classList.contains('on');
      const valOf = n => ctlOf(n)?.dataset.value ?? '';

      const saveSecurity = async () => {
        try {
          await api.setSecurity({ twoFA: isOn('twoFA'), loginNotify: isOn('loginNotify') });
          toast('Sign-in settings saved.');
        } catch (e) { oops(e); }
      };
      /* The bot's own settings travel together — the handler reads the whole
         form and keeps what is missing, so sending one key alone is fine, but
         sending them together is what the Save button already does. */
      const saveTelegram = async () => {
        try {
          await api.telegramSave({
            alertsEnabled: isOn('alertsEnabled'),
            alertTunnelDown: isOn('alertTunnelDown'),
            alertNewRelease: isOn('alertNewRelease'),
            relayMode: valOf('relayMode'), lang: valOf('lang'),
          });
          toast('Bot settings saved.');
        } catch (e) { oops(e); }
      };

      const onFlip = {
        twoFA: saveSecurity, loginNotify: saveSecurity,
        alertsEnabled: saveTelegram, alertTunnelDown: saveTelegram,
        alertNewRelease: saveTelegram,
        autoBackup: async on => {
          try { await api.setAutoBackup(on); toast(on ? 'Weekly backups on.' : 'Weekly backups off.'); }
          catch (e) { oops(e); }
        },
      };

      const kind = n => (n.className || '').split(' ')[0];
      root.querySelectorAll('[data-name]').forEach(sw => {
        if (sw.dataset.wired || !/^sw/.test(kind(sw))) return;
        sw.dataset.wired = '1';
        sw.setAttribute('role', 'switch');
        sw.tabIndex = 0;
        const flip = async () => {
          sw.classList.toggle('on');
          const on = sw.classList.contains('on');
          sw.setAttribute('aria-checked', String(on));
          const fn = onFlip[sw.dataset.name];
          if (fn) await fn(on);
        };
        sw.addEventListener('click', flip);
        sw.addEventListener('keydown', ev => {
          if (ev.key === ' ' || ev.key === 'Enter') { ev.preventDefault(); flip(); }
        });
      });

      const menuChoices = {
        channelBeta: [{ value: '0', label: 'Stable — finished releases only (recommended)' },
                      { value: '1', label: 'Beta — pre-releases as they land' }],
        relayMode: [{ value: 'auto', label: 'Automatic — through a tunnel when one is up' },
                    { value: 'direct', label: 'Direct — never through a tunnel' }],
        lang: [{ value: 'en', label: 'English' }, { value: 'fa', label: 'فارسی' }],
      };

      /* Naming one tunnel is the third answer this setting has, and the one the
         panel never offered: the handler takes a tunnel name and opens a SOCKS
         port on it, and /api/relays exists to say which tunnels can do that.
         Without this the menu had two of its three options. */
      try {
        for (const t of (await api.relays()) || []) {
          if (!t || !t.name) continue;
          menuChoices.relayMode.push({
            value: t.name,
            label: `${t.name} — through this tunnel${isUp(t) ? '' : ' (not up right now)'}`,
          });
        }
      } catch (e) { /* the two fixed answers are still there */ }

      root.querySelectorAll('[data-name]').forEach(sel => {
        if (!/^sel/.test(kind(sel))) return;
        const name = sel.dataset.name;
        const list = menuChoices[name];
        if (!list || sel.dataset.wired) return;
        sel.dataset.wired = '1';
        sel.setAttribute('role', 'combobox');
        sel.tabIndex = 0;
        const menu = el('div', { class: 'selmenu', hidden: true });
        list.forEach(c => {
          const b = el('button', { type: 'button', class: 'selopt', text: c.label });
          b.addEventListener('click', async ev => {
            ev.stopPropagation();
            sel.dataset.value = c.value;
            sel.childNodes[0].textContent = c.label;
            menu.hidden = true;
            sel.classList.remove('open');
            try {
              if (name === 'channelBeta') {
                await api.setChannel(c.value === '1');
                toast(c.value === '1' ? 'Beta channel.' : 'Stable channel.');
              } else {
                await saveTelegram();
              }
            } catch (e) { oops(e); }
          });
          menu.append(b);
        });
        sel.append(menu);
        sel.addEventListener('click', ev => {
          if (ev.target.closest('.selopt')) return;
          const open = menu.hidden;
          root.querySelectorAll('.selmenu').forEach(m => { m.hidden = true; });
          menu.hidden = !open;
          sel.classList.toggle('open', open);
        });
      });
      root.addEventListener('click', ev => {
        if (ev.target.closest('.sel')) return;
        root.querySelectorAll('.selmenu').forEach(m => { m.hidden = true; });
        root.querySelectorAll('.sel.open').forEach(x => x.classList.remove('open'));
      });

      /* What the server already has, reflected onto the faces. */
      const secNow = val(sec) || {}, tgNow = val(tg) || {}, chNow = val(ch) || {}, abNow = val(ab) || {};
      const setSw = (n, on) => {
        const c = ctlOf(n);
        if (c) { c.classList.toggle('on', !!on); c.setAttribute('aria-checked', String(!!on)); }
      };
      setSw('twoFA', secNow.twoFA);
      setSw('loginNotify', secNow.loginNotify);
      setSw('alertsEnabled', tgNow.alerts?.enabled);
      setSw('alertTunnelDown', tgNow.alerts?.tunnelDown);
      setSw('alertNewRelease', tgNow.alerts?.newRelease);
      setSw('autoBackup', abNow.enabled);
      /* The line above the backup pane claimed a backup was taken "yesterday at
         03:00 · 2.4 MB". Nothing reports that — /api/autobackup answers whether
         the weekly backup is on and nothing else — so it was a date and a size
         invented by the preview and shown as fact. It now says the one thing
         that is actually known. */
      /* The bot's status line named a bot — "Connected as @my_backpack_bot" —
         that belongs to whoever drew the screen. What is known is whether a
         token and an admin are set, and the masked hint for the token. */
      const tgState = root.querySelector('[data-p="telegram"] .state2');
      if (tgState) {
        tgState.classList.toggle('ok', !!tgNow.configured);
        tgState.textContent = tgNow.configured
          ? 'Connected' + (tgNow.tokenHint ? ' — token ' + tgNow.tokenHint : '')
          : 'Not configured — add a bot token and an admin id below.';
      }

      const bkState = root.querySelector('[data-p="backup"] .state2');
      if (bkState) {
        bkState.classList.toggle('ok', !!abNow.enabled);
        bkState.textContent = abNow.enabled
          ? 'The weekly backup is on — one is taken automatically and kept on this server.'
          : 'The weekly backup is off. Download one below, or turn it on.';
      }
      const setSel = (n, v, label) => {
        const c = ctlOf(n);
        if (!c || v === undefined) return;
        c.dataset.value = String(v);
        if (label) c.childNodes[0].textContent = label;
      };
      const onBeta = (chNow.channel || 'stable') === 'beta';
      setSel('channelBeta', onBeta ? '1' : '0',
        onBeta ? 'Beta — pre-releases as they land' : 'Stable — finished releases only (recommended)');
      setSel('relayMode', tgNow.relayMode);
      setSel('lang', tgNow.lang);

      /* The search box.
       *
       * It was drawn with a placeholder promising to find every setting, a hit
       * counter beside each section and an esc hint on the field — and nothing
       * bound to it, so typing in it did nothing at all. A control that
       * advertises a behaviour that badly has to have it.
       *
       * Rows are matched on what they say rather than on a keyword list: the
       * label and the hint under it are the words the operator would search
       * for, and a list kept beside them is a list that goes stale.
       */
      const q = root.querySelector('#setq');
      if (q) {
        const rows = [...root.querySelectorAll('.f2b, .tg2, .arow, .line3')];
        const total = rows.length;
        const cnt = root.querySelector('#setcnt');
        const groups = [...root.querySelectorAll('.grp2')];

        const apply = () => {
          const term = q.value.trim().toLowerCase();
          let shown = 0;
          rows.forEach(r => {
            const hit = !term || (r.innerText || '').toLowerCase().includes(term);
            r.hidden = !hit;
            if (hit) shown++;
          });
          // A heading over nothing is worse than no heading.
          groups.forEach(g => {
            g.hidden = !!term && ![...g.querySelectorAll('.f2b, .tg2, .arow, .line3')]
              .some(r => !r.hidden);
          });
          // Each section says how many of its own settings matched, which is
          // what the counter beside it was drawn for.
          root.querySelectorAll('.rail3 [data-k]').forEach(a => {
            const pane = root.querySelector(`[data-p="${a.dataset.k}"]`);
            const n = pane
              ? [...pane.querySelectorAll('.f2b, .tg2, .arow, .line3')].filter(r => !r.hidden).length
              : 0;
            const hits = a.querySelector('.hits');
            if (hits) hits.textContent = term && n ? String(n) : '';
            a.classList.toggle('nohit', !!term && !n);
          });
          if (cnt) {
            cnt.textContent = term
              ? `${shown} of ${total} settings`
              : `${total} settings`;
          }
        };

        q.addEventListener('input', apply);
        q.addEventListener('keydown', ev => {
          if (ev.key === 'Escape') { ev.stopPropagation(); q.value = ''; apply(); }
        });
        apply();
      }

      ctx.setTeardown(close);
    },
  }).catch(oops);
}

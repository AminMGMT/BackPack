/* Update, restore points and backup — the machine-level chores.
 *
 * CLI: 4 Backup & Restore, and 8 Update.
 */

import { $$, esc, dialogSubtitle } from '../lib/dom.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { oops, toast } from '../ui/toast.js';
import { confirmBox } from '../ui/confirm.js';

export async function maintView(ctx) {
  /* Opened straight from a link, this runs before the first poll, so the
     installed version would be blank in the very bar that compares it. */
  if (!store.get().stats) await store.loadStats();

  openScreen('maint', {
    pick: '.dlg[data-d="maint"]',
    bind: async (root, close) => {
      dialogSubtitle(root, store.get().stats, (store.get().stats || {}).version);
      const s = store.get().stats || {};
      const now = root.querySelector('.now6');
      if (now) now.textContent = s.version || '—';

      /* ---- update ---- */
      const next = root.querySelector('.new6');
      const hint = root.querySelector('#mhint');
      const install = root.querySelector('#install');
      try {
        const u = await api.updateCheck();
        /* The release the check found. When there is nothing to install the
           summary still names it — "v1.8.5 is newer than the latest release
           (v1.8.4)" — and this used to repeat the installed version in green,
           as if that were the latest. */
        const vs = (u.summary || '').match(/v\d+(?:\.\d+)+/g) || [];
        if (next) next.textContent = (u.available ? vs[0] : vs[vs.length - 1]) || s.version || '—';
        if (hint) hint.textContent = u.summary || '';
        if (install) {
          install.hidden = !u.available;
          install.textContent = 'Install ' + (next?.textContent || '');
        }
      } catch (e) { if (hint) hint.textContent = 'Could not reach the release list.'; }

      /* The backup pane's status line was written by the preview — a date and a
         kind, both invented. Nothing reports when the last automatic backup ran;
         /api/autobackup answers whether the weekly one is on. Say that. */
      const bkNote = [...root.querySelectorAll('[data-p="bk"] .note6')]
        .find(n => /last backup/i.test(n.textContent));
      if (bkNote) {
        try {
          const ab = await api.autoBackup();
          bkNote.textContent = ab.enabled
            ? 'The weekly backup is on. Each result is filed with the alerts, so a failure is not silent.'
            : 'The weekly backup is off — nothing is taken automatically.';
        } catch (e) {
          bkNote.textContent = 'Whether the weekly backup is on could not be read.';
        }
      }

      const log = root.querySelector('#log6');
      const list = root.querySelector('#lgl');
      const lgttl = root.querySelector('#lgttl');
      let poll = null;

      const drain = async () => {
        try {
          const st = await api.updateStatus();
          log.hidden = false;
          log.classList.toggle('run', st.running);
          lgttl.textContent = st.running ? 'Installing' : 'Finished';
          list.innerHTML = (st.log || []).map(l => {
            const cls = /fail|error|cannot|refus/i.test(l) ? 'bad6'
                      : /verified|passed|complete/i.test(l) ? 'ok6'
                      : /warn|rolling/i.test(l) ? 'wr6' : '';
            return `<li class="${cls}"><span>${esc(l)}</span></li>`;
          }).join('');
          list.scrollTop = list.scrollHeight;
          if (!st.running) {
            clearInterval(poll); poll = null;
            install.disabled = false;
            if (st.error) toast(st.error, true);
          }
        } catch (e) { clearInterval(poll); oops(e); }
      };

      install?.addEventListener('click', async () => {
        if (!await confirmBox({
          title: 'Install the update?',
          body: 'A restore point is saved first, and it rolls itself back if the tunnels do not come back up.',
          go: 'Install',
        })) return;
        install.disabled = true;
        try { await api.updateStart(); } catch (e) { return oops(e); }
        poll = setInterval(drain, 900);
        drain();
      });

      root.querySelector('#recheck')?.addEventListener('click', async () => {
        try { const u = await api.updateCheck(); hint.textContent = u.summary; }
        catch (e) { oops(e); }
      });

      /* ---- restore points ----
         Each one can be rolled back to now — Update → Restore Points in the
         menu. It said "done from the Telegram bot" because the panel had no
         endpoint; it has one, on the same log an update writes to. */
      const rpBox = root.querySelector('.pane6[data-p="rp"]');
      const drawPoints = async () => {
        let pts = [];
        try { pts = await api.restorePoints(); } catch (e) { /* stays empty */ }
        const tab = root.querySelector('.tabs [data-t6="rp"] .n');
        if (tab) tab.textContent = String(pts.length);
        if (!rpBox) return;
        rpBox.innerHTML = (pts.length ? pts.map(p => {
          const tuns = p.tunnels || [];
          return `<div class="rp6"><div class="keel"><i></i><u></u></div>
            <div class="t6" style="flex:1;min-width:0">
              <div class="stamp6">${esc(p.stamp)}</div>
              <div class="meta6">${esc(p.reason || 'snapshot')} · ${esc(new Date(p.created).toLocaleString())} · ${tuns.length} tunnel${tuns.length === 1 ? '' : 's'} captured</div>
              ${tuns.length ? `<div class="tags6">${tuns.map(n => `<span>${esc(n)}</span>`).join('')}</div>` : ''}
            </div><div class="rpend"><span class="vtag">${esc(p.version)}</span>
            <button class="btn6" data-roll="${esc(p.stamp)}" data-ver="${esc(p.version)}">Roll back</button></div></div>`;
        }).join('') : `<div class="row6"><div class="t6"><b>No restore points yet</b>
            <span>One is taken automatically before every update and every rollback.</span></div></div>`) +
          `<div class="note6">Five are kept on disk; taking a sixth drops the oldest. Each holds the binary
            that was running and the whole config directory. Rolling back restarts every tunnel and this panel.</div>`;
      };
      await drawPoints();
      rpBox?.addEventListener('click', async ev => {
        const b = ev.target.closest('[data-roll]');
        if (!b) return;
        if (!await confirmBox({
          title: `Roll back to ${esc(b.dataset.ver)}?`,
          body: `The binary and every config go back to how they were at ${esc(b.dataset.roll)}. Every tunnel restarts, and so does this panel.`,
          go: 'Roll back', danger: true,
        })) return;
        try { await api.rollback(b.dataset.roll); } catch (e) { return oops(e); }
        toast('Rolling back — follow it under Update.');
        root.querySelector('.tabs [data-t6="upd"]')?.click();
        poll = setInterval(drain, 900);
        drain();
      });

      /* ---- install from a file ----
         For the servers that cannot reach the release host: the archive is
         uploaded from this computer to where the menu's Install From A File
         looks, and installed the same way — verified if a SHA256SUMS comes
         with it, snapshot first, rolled back if a tunnel does not return. */
      const updPane = root.querySelector('.pane6[data-p="upd"]');
      if (updPane) {
        const box = document.createElement('div');
        box.className = 'lf6';
        updPane.append(box);
        const drawLocal = async () => {
          let v;
          try { v = await api.localUpdate(); } catch (e) { box.innerHTML = ''; return; }
          const f = v.found;
          box.innerHTML = `<div class="lfh"><b>Install from a file</b>
              <span>No route to GitHub? Download <code>${esc(v.asset)}</code> (and its <code>SHA256SUMS</code>) on any computer and drop them here.</span></div>
            ${f ? `<div class="row6"><div class="t6"><b>${esc(f.version || 'Unknown version')} is ready</b>
                <span>${esc(f.path)} · ${(f.size / 1048576).toFixed(1)} MB · ${f.verified ? 'checksum will be verified' : 'no SHA256SUMS — installed unverified'}</span></div>
                <button class="btn6 solid" id="lfgo">Install it</button></div>` : ''}
            <div class="drop6 sm" id="lfdrop"><b>${f ? 'Replace it — drop' : 'Drop'} the archive here, or choose files</b>
              <span>${esc(v.asset)} · SHA256SUMS optional · up to 256 MB</span></div>`;
          const pick = document.createElement('input');
          pick.type = 'file'; pick.multiple = true; pick.hidden = true;
          box.append(pick);
          const drop = box.querySelector('#lfdrop');
          const take = async files => {
            const list = [...(files || [])];
            const arch = list.find(x => /\.tar\.gz$/i.test(x.name));
            const sums = list.find(x => /sha256sums/i.test(x.name));
            if (!arch) { toast(`Choose ${v.asset}.`, true); return; }
            drop.classList.add('busy');
            drop.querySelector('b').textContent = `Uploading ${arch.name}…`;
            try { await api.uploadUpdate(arch, sums); toast('Uploaded — check the version, then install it.'); }
            catch (e) { oops(e); }
            drawLocal();
          };
          drop.addEventListener('click', () => pick.click());
          pick.addEventListener('change', () => take(pick.files));
          ['dragenter', 'dragover'].forEach(k => drop.addEventListener(k, e => { e.preventDefault(); drop.classList.add('over'); }));
          ['dragleave', 'drop'].forEach(k => drop.addEventListener(k, e => { e.preventDefault(); drop.classList.remove('over'); }));
          drop.addEventListener('drop', e => take(e.dataTransfer.files));
          box.querySelector('#lfgo')?.addEventListener('click', async ev => {
            if (!await confirmBox({
              title: `Install ${esc(f.version || 'this archive')}?`,
              body: 'A restore point is saved first, and it rolls itself back if the tunnels do not come back up.',
              go: 'Install',
            })) return;
            ev.target.disabled = true;
            try { await api.installLocalUpdate(); } catch (e) { ev.target.disabled = false; return oops(e); }
            poll = setInterval(drain, 900);
            drain();
          });
        };
        drawLocal();
      }

      /* ---- backup ---- */
      root.querySelector('#dlbtn')?.addEventListener('click', () => {
        location.href = api.backupExportURL();
      });

      const drop = root.querySelector('#drop6');
      if (drop) {
        const file = document.createElement('input');
        file.type = 'file'; file.accept = '.gz,.tar.gz'; file.hidden = true;
        root.append(file);
        const take = async f => {
          if (!f) return;
          if (!await confirmBox({
            title: `Restore from <q>${esc(f.name)}</q>?`,
            body: 'This replaces the current tunnels and settings, and you will be signed out.',
            go: 'Restore', danger: true,
          })) return;
          try {
            const r = await api.backupImport(f);
            root.querySelector('#rres').hidden = false;
            root.querySelector('#rrestxt').textContent =
              `${r.files} config files written · ${(r.tunnels || []).length} tunnels re-registered · ` +
              `${r.started} started · ${r.failed} failed. Every session was cleared, so you will be ` +
              `asked to sign in again.`;
          } catch (e) { oops(e); }
        };
        drop.addEventListener('click', () => file.click());
        file.addEventListener('change', () => take(file.files[0]));
        ['dragenter', 'dragover'].forEach(k => drop.addEventListener(k, e => {
          e.preventDefault(); drop.classList.add('over');
        }));
        ['dragleave', 'drop'].forEach(k => drop.addEventListener(k, e => {
          e.preventDefault(); drop.classList.remove('over');
        }));
        drop.addEventListener('drop', e => take(e.dataTransfer.files[0]));
      }

      const ab = root.querySelector('#ab6');
      if (ab) {
        try {
          const st = await api.autoBackup();
          ab.classList.toggle('on', !!st.enabled);
          ab.setAttribute('aria-pressed', String(!!st.enabled));
        } catch (e) {}
        ab.addEventListener('click', async () => {
          const on = !ab.classList.contains('on');
          ab.classList.toggle('on', on);
          ab.setAttribute('aria-pressed', String(on));
          try { await api.setAutoBackup(on); toast(on ? 'Weekly backups on.' : 'Weekly backups off.'); }
          catch (e) { oops(e); }
        });
      }

      /* ---- backups kept on this server, and the off-site copy ----
         Backup & Restore in the menu keeps archives in the backups folder and
         can prove one restores without restoring it. The panel only ever
         streamed one to the browser. */
      const bkPane = root.querySelector('.pane6[data-p="bk"]');
      if (bkPane) {
        const box = document.createElement('div');
        box.className = 'bs6';
        bkPane.prepend(box);
        const kb = n => n > 1048576 ? (n / 1048576).toFixed(1) + ' MB' : Math.max(1, Math.round(n / 1024)) + ' KB';
        const drawSaved = async (note) => {
          let v;
          try { v = await api.backups(); } catch (e) { box.innerHTML = ''; return; }
          box.innerHTML = `<div class="lfh"><b>Kept on this server</b><span>${esc(v.dir)}</span>
              <button class="btn6 solid" id="bsnew">Back up now</button></div>
            ${v.files.length ? `<div class="bsl">${v.files.map(f => `<div class="bsr" data-n="${esc(f.name)}">
                <div class="t6"><b>${esc(f.name)}</b><span>${esc(new Date(f.modified * 1000).toLocaleString())} · ${kb(f.size)}</span></div>
                <button class="btn6" data-b="test">Test</button>
                <a class="btn6" href="${esc(api.savedBackupURL(f.name))}">Download</a>
                <button class="btn6 warn" data-b="restore">Restore</button>
                <button class="btn6 ghost6" data-b="delete" title="Delete">✕</button>
                <div class="bsout" hidden></div></div>`).join('')}</div>`
              : '<div class="note6">None yet — the weekly backup and "Back up now" put them here.</div>'}
            <div class="lfh"><b>Off-site copy</b><span>A command run on every new backup. <code>{}</code> is the file — e.g. <code>rclone copy {} remote:backpack/</code>.</span></div>
            <div class="osrow"><input id="oscmd" value="${esc(v.offsite || '')}" placeholder="blank — backups stay on this machine" spellcheck="false">
              <button class="btn6" id="ossave">Save</button><button class="btn6" id="ossend"${v.offsite ? '' : ' disabled'}>Send the newest</button></div>
            ${note ? `<div class="note6">${note}</div>` : ''}
            <div style="height:14px"></div>`;
        };
        await drawSaved();
        box.addEventListener('click', async ev => {
          const b = ev.target.closest('button');
          if (!b) return;
          const row = b.closest('.bsr');
          const name = row?.dataset.n;
          try {
            if (b.id === 'bsnew') {
              b.disabled = true;
              const r = await api.backupCreate();
              toast(`Backed up: ${r.created}`);
              return drawSaved();
            }
            if (b.id === 'ossave') {
              await api.setOffsite(box.querySelector('#oscmd').value.trim());
              toast('Saved.');
              return drawSaved();
            }
            if (b.id === 'ossend') {
              b.disabled = true;
              const r = await api.sendOffsite();
              return drawSaved(`Sent ${esc(r.sent)} — the weekly backups go there too.`);
            }
            if (!name) return;
            if (b.dataset.b === 'test') {
              const out = row.querySelector('.bsout');
              out.hidden = false;
              out.innerHTML = 'Checking it like a real restore — nothing is changed…';
              const r = await api.backupTest(name);
              const bits = [`${r.files} files`, `${(r.tunnels || []).length} tunnels${(r.tunnels || []).length ? ': ' + r.tunnels.join(', ') : ''}`];
              if (r.certificates) bits.push(`${r.certificates} certificates`);
              if (r.webui) bits.push('the panel settings');
              if (r.telegram) bits.push('the Telegram bot');
              out.innerHTML = `<b class="${(r.warnings || []).length ? 'wr' : 'ok'}">${(r.warnings || []).length ? 'Would restore, with notes' : 'Would restore cleanly'}</b>
                <span>${esc(bits.join(' · '))}</span>${(r.warnings || []).map(w => `<span class="w">! ${esc(w)}</span>`).join('')}
                ${r.fleetSealed ? '<span class="w">! Managed-server passwords are sealed with this machine\'s fleet key, which is not in the archive — on another machine, restore the key from the menu first.</span>' : ''}`;
              return;
            }
            if (b.dataset.b === 'restore') {
              if (!await confirmBox({
                title: `Restore <q>${esc(name)}</q>?`,
                body: 'This replaces the current tunnels and settings, and you will be signed out.',
                go: 'Restore', danger: true,
              })) return;
              const r = await api.backupRestoreSaved(name);
              toast(`Restored: ${r.files} files, ${(r.tunnels || []).length} tunnels. Signing you out…`);
              setTimeout(() => { location.href = api.base() + '/login'; }, 1800);
              return;
            }
            if (b.dataset.b === 'delete') {
              if (!await confirmBox({ title: `Delete <q>${esc(name)}</q>?`, body: 'The archive is removed from this server.', go: 'Delete', danger: true })) return;
              await api.backupDelete(name);
              return drawSaved();
            }
          } catch (e) { oops(e); b.disabled = false; }
        });
      }

      /* tabs */
      const tabs = $$('.tabs button', root);
      const panes = $$('.pane6', root);
      panes.forEach((p, i) => { p.hidden = i > 0; });
      tabs.forEach(b => b.addEventListener('click', () => {
        tabs.forEach(x => x.classList.toggle('on', x === b));
        panes.forEach(p => { p.hidden = p.dataset.p !== b.dataset.t6; });
      }));

      ctx.setTeardown(() => { if (poll) clearInterval(poll); close(); });
    },
  }).catch(oops);
}

/* Undo a change — the per-tunnel half of the same idea.
 * CLI: per-tunnel → Undo a change. */
export function undoView(ctx) {
  const name = ctx.params.name;
  openScreen('maint', {
    pick: '.dlg[data-d="undo"]',
    bind: async (root, close) => {
      const sub = root.querySelector('.dh .ttl small');
      if (sub) sub.textContent = name;

      let changes = [];
      try { changes = (await api.confHistory(name)).changes || []; } catch (e) { return oops(e); }

      const tl = root.querySelector('.tl6');
      if (!changes.length) {
        tl.innerHTML = `<div class="ent6 now"><div class="rail"><i></i><u></u></div>
          <div class="bd6"><div class="card6"><div class="t6"><b>Nothing to go back to</b>
          <span>Nothing has been changed on this tunnel yet, so there is no earlier
          configuration kept.</span></div></div></div></div>`;
        ctx.setTeardown(close);
        return;
      }

      tl.innerHTML = `<div class="ent6 now"><div class="rail"><i></i><u></u></div>
        <div class="bd6"><div class="card6"><div class="t6"><b>Now</b>
        <span>the configuration the tunnel is running on</span></div></div></div></div>` +
        changes.map((c, i) => `
        <div class="ent6" data-e="${i}" data-at="${c.at}">
          <div class="rail"><i></i><u></u></div>
          <div class="bd6">
            <div class="card6"><div class="t6">
              <b>Before ${esc(c.when)}</b>
              <span>${esc(c.note || 'the configuration in place before this')}</span>
            </div><button class="btn6" data-arm="${i}">Restore</button></div>
            <div class="prog6"><i></i></div>
            <div class="cf6"><div class="in6">
              <p>Put back the configuration from <b>${esc(c.when)}</b>? The tunnel restarts on it.
                 If it does not come up within ten seconds it is reverted, exactly like any other
                 change.</p>
              <div class="btns6"><button class="btn6 solid" data-go="${i}">Put it back</button>
                <button class="btn6" data-cancel="${i}">Cancel</button></div>
            </div></div>
          </div></div>`).join('');

      tl.addEventListener('click', async ev => {
        const arm = ev.target.closest('[data-arm]');
        const cancel = ev.target.closest('[data-cancel]');
        const run = ev.target.closest('[data-go]');
        if (arm) {
          const row = tl.querySelector(`.ent6[data-e="${arm.dataset.arm}"]`);
          $$('.ent6.arm', tl).forEach(r => { if (r !== row) r.classList.remove('arm'); });
          row.classList.toggle('arm');
        }
        if (cancel) tl.querySelector(`.ent6[data-e="${cancel.dataset.cancel}"]`).classList.remove('arm');
        if (run) {
          const row = tl.querySelector(`.ent6[data-e="${run.dataset.go}"]`);
          row.classList.remove('arm'); row.classList.add('busy6');
          try {
            await api.confRestore(name, row.dataset.at);
            row.classList.add('done6');
            row.querySelector('span').textContent = 'put back — the tunnel came up on it';
            toast('Restored, and the tunnel came up on it.');
            store.refresh();
          } catch (e) {
            row.classList.add('fail6');
            row.querySelector('span').textContent = e.message;
          }
          row.classList.remove('busy6');
        }
      });
      ctx.setTeardown(close);
    },
  }).catch(oops);
}

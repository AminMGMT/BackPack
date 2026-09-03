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
        if (next) next.textContent = u.available ? u.summary.match(/v[\d.]+/)?.[0] || '' : s.version;
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

      /* ---- restore points ---- */
      try {
        const pts = await api.restorePoints();
        const box = root.querySelector('.pane6[data-p="rp"]');
        if (box) {
          box.innerHTML = pts.map(p => `
            <div class="rp6"><div class="keel"><i></i><u></u></div>
              <div class="t6" style="flex:1;min-width:0">
                <div class="stamp6">${esc(p.stamp)}</div>
                <div class="meta6">${esc(p.reason)} · ${esc(new Date(p.created).toLocaleString())} · ${p.tunnels.length} tunnels captured</div>
                <div class="tags6">${p.tunnels.map(n => `<span>${esc(n)}</span>`).join('')}</div>
              </div><span class="vtag">${esc(p.version)}</span></div>`).join('') + `
            <div class="note6">Five are kept on disk; taking a sixth drops the oldest.</div>
            <div class="warn6"><p>Rolling one back is done from the Telegram bot, under
              <b>♻️ Restore points</b>. The panel lists them and has no endpoint that puts one back.</p></div>`;
        }
      } catch (e) { /* the tab simply stays empty */ }

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
            await api.confRestore(name, Number(row.dataset.at));
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

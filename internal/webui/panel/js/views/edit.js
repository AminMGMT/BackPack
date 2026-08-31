/* Edit one tunnel.
 *
 * The form is the preview's; the values come from /api/tunnel/settings and go
 * back through /api/tunnel/edit — the same call the CLI's edit screen makes, so
 * a tunnel edited here is byte-for-byte a tunnel edited in the terminal.
 *
 * CLI: Manage → Manage Tunnels → Edit.
 */

import { $$, esc } from '../lib/dom.js';
import { kindLabel, flag } from '../lib/format.js';
import * as api from '../api.js';
import * as store from '../store.js';
import { openScreen } from '../ui/screen.js';
import { oops, toast } from '../ui/toast.js';
import { go } from '../router.js';

/* The form's controls are named after the keys the server reads — dotted where
   the payload nests (tune.keepAlive, limits.bandwidthMbps) — so filling the
   form and reading it back are both one pass with no field list to drift.
   Five controls in the preview have no field behind them on the server; they
   carry data-unwired and are left alone rather than posted as invented keys. */
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

const dig = (obj, path) => path.split('.').reduce((o, k) => (o ?? {})[k], obj);

function plant(obj, path, value) {
  const keys = path.split('.');
  const last = keys.pop();
  let at = obj;
  for (const k of keys) at = at[k] ??= {};
  at[last] = value;
}

function fill(root, values) {
  root.querySelectorAll('[name]').forEach(node => {
    const v = dig(values, node.name);
    if (v === undefined || v === null) return;
    setControl(node, v);
  });
  root.querySelectorAll('[data-unwired]').forEach(n => {
    n.disabled = true;
    n.title = 'Not settable over the API — use the CLI';
  });
}

function read(root) {
  const out = {};
  root.querySelectorAll('input[name], select[name], textarea[name]').forEach(n => {
    const raw = n.type === 'checkbox' ? n.checked
              : /^-?\d+$/.test(n.value.trim()) ? Number(n.value.trim())
              : n.value;
    plant(out, n.name, raw);
  });
  return out;
}

export async function editView(ctx) {
  const name = ctx.params.name;
  /* A deep link arrives before the first poll, so the tunnel may not be in the
     store yet; without this the header falls back to the preview's own text. */
  let t = store.tunnel(name);
  if (!t) { await store.loadTunnels(); t = store.tunnel(name); }

  openScreen('edit', {
    pick: '.dlg',
    bind: async (root, close) => {
      const fl = root.querySelector('.dh .fl');
      if (fl) fl.textContent = flag(t?.peerCountry) || '·';
      const ttl = root.querySelector('.dh .ttl input, .dh .ttl > div');
      if (ttl) { if ('value' in ttl) ttl.value = name; else ttl.textContent = name; }
      const sub = root.querySelector('.dh .ttl small');
      if (sub && t) sub.textContent = kindLabel(t) + ' · ' + (t.addr || '');

      let settings = {};
      try {
        settings = await api.tunnelSettings(name);
        if (settings.kind === 'direct') settings = settings.direct || {};
      } catch (e) { oops(e); }
      fill(root, settings);

      /* Tabs and drawers are the preview's own handlers, rebound in screen.js. */

      /* The History button has been in this dialog since it was drawn; it goes
         to the screen that lists what this tunnel used to be. */
      root.querySelector('.hist')?.addEventListener('click', () => {
        close();
        go(`/t/${encodeURIComponent(name)}/undo`);
      });

      /* Random asks the server for a free port rather than rolling one here. */
      [...root.querySelectorAll('button')]
        .filter(b => /^random$/i.test(b.textContent.trim()))
        .forEach(b => b.addEventListener('click', async () => {
          try {
            const r = await api.tunnelSuggest();
            const field = b.closest('div')?.querySelector('input') ||
                          root.querySelector('[name="tunnelPort"]');
            if (field && r.port) field.value = r.port;
          } catch (e) { oops(e); }
        }));

      const save = root.querySelector('.save, [data-save], .primary');
      save?.addEventListener('click', async () => {
        const payload = { name, ...read(root) };
        save.disabled = true;
        try {
          await api.tunnelEdit(payload);
          toast('Saved — the tunnel restarted on the new settings.');
          store.refresh();
          close();
        } catch (e) { oops(e); }
        save.disabled = false;
      });

      ctx.setTeardown(close);
    },
  }).catch(oops);
}

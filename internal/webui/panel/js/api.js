/* The only place the panel talks to the server.
 *
 * Every function here is one route the Go server already serves, named after
 * it. Nothing else in the panel calls fetch(), so a route that changes shape
 * breaks in one file instead of ten.
 *
 * There is one code path and it is the real one. The panel used to carry a
 * second — a directory of fixtures, chosen by a flag — which was how the screens
 * were drawn before the server existed. It outlived its purpose twice over: the
 * fixtures were never shipped inside the binary, so forcing that mode on a real
 * panel fetched files that are not there and drew an empty server over a busy
 * one; and having somewhere for made-up numbers to come from is what let so
 * many of them survive on screens that were supposed to have been wired up.
 */

async function get(path) {
  const r = await fetch(path, { cache: 'no-store' });
  if (!r.ok) throw new Error(await r.text() || r.statusText);
  return r.json();
}

async function post(path, body) {
  const opts = { method: 'POST' };
  if (body instanceof FormData || body instanceof URLSearchParams) opts.body = body;
  else if (body !== undefined) {
    opts.headers = { 'Content-Type': 'application/json' };
    opts.body = JSON.stringify(body);
  }
  const r = await fetch(path, opts);
  if (!r.ok) throw new Error(await r.text() || r.statusText);
  const text = await r.text();
  return text ? JSON.parse(text) : {};
}

/* ---- CLI: Manage → Status ------------------------------------------------ */
export const stats   = () => get('/api/stats');
export const tunnels = () => get('/api/tunnels');

/* ---- CLI: Manage → Manage Tunnels ---------------------------------------- */
export const tunnelAction = (name, action) =>
  post('/api/tunnel/action', new URLSearchParams({ name, action }));
export const restartAll = () =>
  post('/api/tunnel/action', new URLSearchParams({ action: 'restartall' }));
export const tunnelSettings = name =>
  get('/api/tunnel/settings?name=' + encodeURIComponent(name));
export const tunnelEdit  = payload => post('/api/tunnel/edit', payload);
export const tunnelOptions = () => get('/api/tunnel/options');

/* ---- Handing a tunnel's paired settings to the other server --------------- */
/* One string carrying everything the two ends must agree on. The mirroring —
   which side becomes which, which addresses swap — is the server's, so the
   panel never holds a second idea of what "the other side" means. */
/* The setup link is gone. It existed so a second panel on the other server
   could be filled in from this one; the panel writes that end itself now. The
   mirroring it fed still happens, on the server side, in pushPeerEnd. */
/* Managed servers. The four actions share one endpoint because they are one
   thing — the fleet — and each returns the state that follows, so the screen
   never has to guess what changed. */
export const nodes = () => get('/api/nodes');
const nodePost = form => post('/api/nodes', new URLSearchParams(form));
export const nodeListenerOn  = () => nodePost({ action: 'enable' });
export const nodeListenerOff = () => nodePost({ action: 'disable' });
export const nodeRemove = name => nodePost({ action: 'remove', name });
/* Adding reaches the server while the operator waits, and installs Backpack on
   it if it has none, so this is the one node call that can take minutes. */
export const nodeAdd = fields => nodePost({ action: 'add', ...fields });
export const nodeCredentials = fields => nodePost({ action: 'credentials', ...fields });
export const nodeUpgrade = name => nodePost({ action: 'upgrade', name });
export const nodeUpgradeAll = () => nodePost({ action: 'upgradeall' });

export const nodeTunnels = name =>
  get('/api/node/tunnels?node=' + encodeURIComponent(name));
/* Both ends in one submission: this end is created here, and the other is
   derived from it and applied on the node. See handleNodePair. */
export const nodePair = body => post('/api/node/pair', body);
/* What a preset actually produces, for the drawer that calls itself "preset
   defaults": the three arguments decide the answer, and asking without them
   describes some other tunnel. */
export const tunnelDefaults = ({ preset = '', role = '', transport = '' } = {}) =>
  get(`/api/tunnel/defaults?preset=${encodeURIComponent(preset)}`
    + `&role=${encodeURIComponent(role)}&transport=${encodeURIComponent(transport)}`);
/* A token both ends will hold. Generated rather than typed wherever the panel
   writes both ends itself — nobody has to read it, so nobody should. */
export const tunnelToken = () => get('/api/tunnel/suggest?what=token');
/* what=port, because the endpoint answers two questions and refuses one that
   names neither: without it every Random port button got a 400. */
export const tunnelSuggest = () => get('/api/tunnel/suggest?what=port');

/* ---- CLI: 1 Setup Iran / 2 Setup Kharej ---------------------------------- */
export const tunnelCreate = payload => post('/api/tunnel/create', payload);
export const directOptions  = () => get('/api/direct/options');
export const directDefaults = () => get('/api/direct/defaults');
export const directCreate   = payload => post('/api/direct/create', payload);

/* ---- CLI: Manage → Health Check / Link Test / Speed Test ------------------ */
export const health = () => get('/api/health');
export const linkTestStatus = () => get('/api/linktest');
export const linkTestRun = name =>
  post('/api/linktest?name=' + encodeURIComponent(name));
export const speedPlan = name =>
  get('/api/speedtest/plan?name=' + encodeURIComponent(name));
export const speedRun = body => post('/api/speedtest', body);

/* ---- CLI: Manage → Tunnel Metrics, and the long view --------------------- */
/* Plain text, not JSON — the handler writes journald's own output. */
/* Either end's journal, as text rather than JSON — a log is lines, and the
   panel renders them itself.

   end === 'peer' asks the managed server holding the other end. A tunnel is one
   thing in two places and its log is not: half of what went wrong is on the
   other machine, and reading it used to mean logging into it. */
export const logs = async (name, end) => {
  const url = '/api/logs?name=' + encodeURIComponent(name) + (end === 'peer' ? '&end=peer' : '');
  const r = await fetch(url, { cache: 'no-store' });
  if (!r.ok) throw new Error(await r.text() || r.statusText);
  return r.text();
};
/* days is optional and the server clamps it to 30 — the store keeps a month of
   hourly buckets, and the endpoint answers a week unless asked for more. */
export const history = (name, days) =>
  get('/api/history?name=' + encodeURIComponent(name) + (days ? '&days=' + days : ''));

/* ---- CLI: per-tunnel → Undo a change ------------------------------------- */
export const confHistory = name =>
  get('/api/confhist?name=' + encodeURIComponent(name));
export const confRestore = (name, at) => post('/api/confhist/restore', { name, at });

/* ---- CLI: 4 Backup & Restore --------------------------------------------- */
export const backupExportURL = () => '/api/backup/export';
export const backupImport = file => {
  const fd = new FormData();
  fd.append('backup', file);
  return post('/api/backup/import', fd);
};
export const autoBackup    = () => get('/api/autobackup');
/* enabled, not on — the same kind of miss as setChannel, and just as invisible
   while nothing called it. */
export const setAutoBackup = on =>
  post('/api/autobackup', new URLSearchParams({ enabled: on ? '1' : '0' }));

/* ---- CLI: 5 Web Panel ---------------------------------------------------- */
export const sessions   = () => get('/api/sessions');
/* Ending one device's session, and ending every other one. */
export const sessionRevoke = id =>
  post('/api/sessions', new URLSearchParams({ action: 'revoke', id }));
export const sessionRevokeOthers = () =>
  post('/api/sessions', new URLSearchParams({ action: 'others' }));
export const setPassword = payload => post('/api/password', payload);
export const setPanelPort = port => post('/api/panelport', new URLSearchParams({ port }));
export const panelCertRead = () => get('/api/panelcert');
/* Form-encoded, because the handler reads r.FormValue. `mode` is not optional:
   without it the endpoint has nothing to apply and refuses the whole request. */
export const panelCert   = ({ mode, domain = '', email = '' }) =>
  post('/api/panelcert', new URLSearchParams({ mode, domain, email }));

/* ---- CLI: 7 Telegram Bot ------------------------------------------------- */
export const telegram     = () => get('/api/telegram');
/* Form-encoded, because the handler reads r.FormValue. It was posting JSON, so
   every key arrived empty and applyTelegramForm kept the values it already had
   — a save that reported success and changed nothing. */
export const telegramSave = fields =>
  post('/api/telegram', new URLSearchParams(
    Object.fromEntries(Object.entries(fields)
      .filter(([, v]) => v !== undefined && v !== null && v !== '')
      .map(([k, v]) => [k, typeof v === 'boolean' ? (v ? '1' : '0') : String(v)]))));
export const telegramTest = () => post('/api/telegram/test', undefined);
export const relays       = () => get('/api/relays');

/* ---- CLI: 8 Update ------------------------------------------------------- */
export const updateCheck  = () => get('/api/update');
export const updateStart  = () => post('/api/update', undefined);
export const updateStatus = () => get('/api/update/status');
export const restorePoints = () => get('/api/restorepoints');
export const channel      = () => get('/api/channel');
/* The handler takes the channel by name and answers with the name it set;
   this was sending beta=1, which it does not read, so the channel never moved.
   Nothing called it, so nothing noticed. */
export const setChannel   = beta =>
  post('/api/channel', new URLSearchParams({ channel: beta ? 'beta' : 'stable' }));

/* ---- alerts -------------------------------------------------------------- */
export const alerts = () => get('/api/alerts');

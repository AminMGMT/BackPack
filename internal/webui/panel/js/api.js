/* The only place the panel talks to the server.
 *
 * Every function here is one route the Go server already serves, named after
 * it. Nothing else in the panel calls fetch(), so switching between the mock
 * and the real server is the MOCK flag below and nothing more — and a route
 * that changes shape breaks in one file instead of ten.
 *
 * MOCK is on when the panel is opened from a static server (the preview) and
 * off when it is served by the Go binary, which is decided by whether the
 * session cookie endpoint answers. It can be forced either way with
 * ?mock=1 / ?mock=0 in the URL.
 */

const params = new URLSearchParams(location.search);
export const MOCK = params.has('mock')
  ? params.get('mock') !== '0'
  : !window.__BACKPACK_LIVE__;

const MOCK_DELAY = 260;   // enough to see the loading states behave

async function mock(file) {
  const r = await fetch(`mock/${file}`, { cache: 'no-store' });
  if (!r.ok) throw new Error(`mock/${file}: ${r.status}`);
  const data = await r.json();
  await new Promise(res => setTimeout(res, MOCK_DELAY));
  return data;
}

async function get(path, mockFile) {
  if (MOCK) return mock(mockFile);
  const r = await fetch(path, { cache: 'no-store' });
  if (!r.ok) throw new Error(await r.text() || r.statusText);
  return r.json();
}

async function post(path, body, mockResult) {
  if (MOCK) {
    await new Promise(res => setTimeout(res, MOCK_DELAY));
    return typeof mockResult === 'function' ? mockResult() : (mockResult ?? { status: 'ok' });
  }
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
export const stats   = () => get('/api/stats', 'stats.json');
export const tunnels = () => get('/api/tunnels', 'tunnels.json');

/* ---- CLI: Manage → Manage Tunnels ---------------------------------------- */
export const tunnelAction = (name, action) =>
  post('/api/tunnel/action', new URLSearchParams({ name, action }), { status: 'ok' });
export const restartAll = () =>
  post('/api/tunnel/action', new URLSearchParams({ action: 'restartall' }),
       { restarted: 5, failed: 1 });
export const tunnelSettings = name =>
  get('/api/tunnel/settings?name=' + encodeURIComponent(name), 'settings.json');
export const tunnelEdit  = payload => post('/api/tunnel/edit', payload);
export const tunnelOptions = () => get('/api/tunnel/options', 'options.json');

/* ---- Handing a tunnel's paired settings to the other server --------------- */
/* One string carrying everything the two ends must agree on. The mirroring —
   which side becomes which, which addresses swap — is the server's, so the
   panel never holds a second idea of what "the other side" means. */
export const shareLink = name =>
  get('/api/tunnel/sharelink?name=' + encodeURIComponent(name), 'sharelink.json');
export const shareLinkDecode = link =>
  post('/api/tunnel/sharelink', { link }, () => ({
    kind: 'reverse', side: 'kharej', transport: 'tcp', token: 'a-mock-token',
    tunnelPort: '8443', serverAddr: '185.4.28.11', paired: ['token', 'tunnelPort', 'transport'],
  }));
export const tunnelDefaults = () => get('/api/tunnel/defaults', 'defaults.json');
/* The mock has to roll a fresh port each time or "Random" looks broken; the
   real endpoint picks a free one on the machine. */
export const tunnelSuggest = async () => {
  if (!MOCK) return get('/api/tunnel/suggest', 'suggest.json');
  await new Promise(r => setTimeout(r, 120));
  return { port: 1024 + Math.floor(Math.random() * 64000) };
};

/* ---- CLI: 1 Setup Iran / 2 Setup Kharej ---------------------------------- */
export const tunnelCreate = payload => post('/api/tunnel/create', payload,
  { status: 'ok', active: true });
export const directOptions  = () => get('/api/direct/options', 'directoptions.json');
export const directDefaults = () => get('/api/direct/defaults', 'directdefaults.json');
export const directCreate   = payload => post('/api/direct/create', payload,
  { status: 'ok', active: true });

/* ---- CLI: Manage → Health Check / Link Test / Speed Test ------------------ */
export const health = () => get('/api/health', 'health.json');
export const linkTestStatus = () => get('/api/linktest', 'linktest.json');
export const linkTestRun = name =>
  post('/api/linktest?name=' + encodeURIComponent(name), undefined, { status: 'started' });
export const speedPlan = name =>
  get('/api/speedtest/plan?name=' + encodeURIComponent(name), 'speedplan.json');
export const speedRun = payload => post('/api/speedtest', payload, { status: 'started' });

/* ---- CLI: Manage → Tunnel Metrics, and the long view --------------------- */
/* Plain text, not JSON — the handler writes journald's own output. */
export const logs = async name => {
  const path = MOCK ? 'mock/logs.txt'
                    : '/api/logs?name=' + encodeURIComponent(name);
  const r = await fetch(path, { cache: 'no-store' });
  if (!r.ok) throw new Error(await r.text() || r.statusText);
  return r.text();
};
/* days is optional and the server clamps it to 30 — the store keeps a month of
   hourly buckets, and the endpoint answers a week unless asked for more. */
export const history = (name, days) =>
  get('/api/history?name=' + encodeURIComponent(name) + (days ? '&days=' + days : ''),
      days && days > 7 ? 'history30.json' : 'history.json');

/* ---- CLI: per-tunnel → Undo a change ------------------------------------- */
export const confHistory = name =>
  get('/api/confhist?name=' + encodeURIComponent(name), 'confhist.json');
export const confRestore = (name, at) => post('/api/confhist/restore', { name, at }, { ok: true });

/* ---- CLI: 4 Backup & Restore --------------------------------------------- */
export const backupExportURL = () => '/api/backup/export';
export const backupImport = file => {
  const fd = new FormData();
  fd.append('backup', file);
  return post('/api/backup/import', fd,
    { status: 'ok', files: 41, tunnels: ['fr-relay', 'de-edge'], started: 5, failed: 1 });
};
export const autoBackup    = () => get('/api/autobackup', 'autobackup.json');
export const setAutoBackup = on => post('/api/autobackup', new URLSearchParams({ on: on ? '1' : '0' }));

/* ---- CLI: 5 Web Panel ---------------------------------------------------- */
export const security   = () => get('/api/security', 'security.json');
export const sessions   = () => get('/api/sessions', 'sessions.json');
export const setPassword = payload => post('/api/password', payload);
export const setPanelPort = port => post('/api/panelport', new URLSearchParams({ port }));
export const panelCert   = payload => post('/api/panelcert', payload);
export const remoteToken = action => post('/api/remotetoken', new URLSearchParams({ action }));

/* ---- CLI: 7 Telegram Bot ------------------------------------------------- */
export const telegram     = () => get('/api/telegram', 'telegram.json');
export const telegramSave = payload => post('/api/telegram', payload);
export const telegramTest = () => post('/api/telegram/test', undefined, { ok: true });
export const relays       = () => get('/api/relays', 'relays.json');

/* ---- CLI: 8 Update ------------------------------------------------------- */
export const updateCheck  = () => get('/api/update', 'update.json');
export const updateStart  = () => post('/api/update', undefined, { status: 'started' });
export const updateStatus = () => get('/api/update/status', 'updatestatus.json');
export const restorePoints = () => get('/api/restorepoints', 'restorepoints.json');
export const channel      = () => get('/api/channel', 'channel.json');
export const setChannel   = beta => post('/api/channel', new URLSearchParams({ beta: beta ? '1' : '0' }));

/* ---- alerts -------------------------------------------------------------- */
export const alerts = () => get('/api/alerts', 'alerts.json');

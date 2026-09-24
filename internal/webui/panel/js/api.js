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

/* Where this panel is served from.
 *
 * The whole panel lives under one unguessable path segment, so every address
 * it asks for has to carry it. It is read from the document rather than
 * assumed, because the server is what decides it — and it is applied here,
 * once, because this is the only file that talks to the server. A view that
 * built its own URL would be the one that breaks. */
const BASE = document.documentElement.dataset.base || '';

/* at() turns a route into an address on this panel. Every path in this file is
   written as the route the Go server registers, and this is what puts it where
   the panel actually answers. */
const at = path => BASE + path;

/* A refusal that has one specific remedy says so in a header, and it is carried
   on the error rather than read back out of the sentence. The words are for a
   person; rewording them must not quietly take the button away. */
async function refusal(r) {
  const e = new Error(await r.text() || r.statusText);
  e.fix = r.headers.get('X-Backpack-Fix') || '';
  e.status = r.status;
  return e;
}

async function get(path) {
  const r = await fetch(at(path), { cache: 'no-store' });
  if (!r.ok) throw await refusal(r);
  return r.json();
}

async function post(path, body) {
  const opts = { method: 'POST' };
  if (body instanceof FormData || body instanceof URLSearchParams) opts.body = body;
  else if (body !== undefined) {
    opts.headers = { 'Content-Type': 'application/json' };
    opts.body = JSON.stringify(body);
  }
  const r = await fetch(at(path), opts);
  if (!r.ok) throw await refusal(r);
  const text = await r.text();
  return text ? JSON.parse(text) : {};
}

/* ---- CLI: Manage → Status ------------------------------------------------ */
export const stats   = () => get('/api/stats');
export const tunnels = () => get('/api/tunnels');

/* ---- CLI: Manage → Manage Tunnels ---------------------------------------- */
export const tunnelAction = (name, action, extra = {}) =>
  post('/api/tunnel/action', new URLSearchParams({ name, action, ...extra }));

/* Linking a tunnel that already exists to the server holding its other end.
   The GET lists what that server has, with the ones that could be this
   tunnel's other half marked; the POST records the operator's choice. */
export const adoptCandidates = (name, node) =>
  get('/api/tunnel/adopt?name=' + encodeURIComponent(name) + '&node=' + encodeURIComponent(node));
export const adoptTunnel = (name, node, peerName) =>
  post('/api/tunnel/adopt', new URLSearchParams({ name, node, peerName }));
export const unlinkTunnel = async name => {
  const r = await fetch(at('/api/tunnel/adopt?name=' + encodeURIComponent(name)), { method: 'DELETE' });
  if (!r.ok) throw new Error(await r.text() || r.statusText);
  return r.json();
};
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

/* What the fleet is supposed to be running, against what it is.
 *
 * Every fleet operation used to be imperative: the panel told a server to
 * create a tunnel and nothing remembered the instruction, so nothing could
 * notice it had stopped being true. This is the other half — it changes
 * nothing, it only reports. */
export const fleetDrift = () => get('/api/fleet/drift');
/* The same fleet, answered from what the panel already knows and contacting no
   server. The fleet page draws this first — otherwise the page stands empty
   until the slowest machine in the fleet has answered — and then replaces it
   with the live listing above. */
export const nodesCached = () => get('/api/nodes?cached=1');
const nodePost = form => post('/api/nodes', new URLSearchParams(form));
export const nodeRemove = name => nodePost({ action: 'remove', name });
/* Adding reaches the server while the operator waits, and installs Backpack on
   it if it has none, so this is the one node call that can take minutes. */
export const nodeAdd = fields => nodePost({ action: 'add', ...fields });
export const nodeCredentials = fields => nodePost({ action: 'credentials', ...fields });
export const nodeUpgrade = name => nodePost({ action: 'upgrade', name });
/* Ask one server again now, rather than waiting for its answer to go stale. */
export const nodeRefresh = name => nodePost({ action: 'refresh', name });
/* What an upgrade-all would do, without doing it. A rollout whose shape can
   only be discovered by starting it is the thing staging exists to fix, so the
   plan is its own call and the button reads it out first. */
export const nodeRolloutPlan = () => nodePost({ action: 'rolloutplan' });
/* Staged: one canary, soaked and checked, then waves, halting on a failure.
   It answers immediately and runs for minutes — poll nodeRolloutStatus. */
export const nodeUpgradeAll = () => nodePost({ action: 'upgradeall' });
export const nodeRolloutStatus = () => nodePost({ action: 'rolloutstatus' });
export const nodeRolloutCancel = () => nodePost({ action: 'rolloutcancel' });
/* Hold one server back from rollouts, with the reason recorded beside it. */
export const nodePin = (name, reason) => nodePost({ action: 'pin', name, reason });
export const nodeUnpin = name => nodePost({ action: 'unpin', name });

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
/* What a direct tunnel on this side should start out as: a subnet nothing here
   is using, an interface name that is free, and a preset. The endpoint and its
   handler were both registered and this wrapper was never written, so nothing
   in the panel ever asked — and the subnet fields, which are the two an
   operator is least able to guess, opened empty on a form that refuses to
   create without them. */
export const directDefaults = side =>
  get('/api/direct/defaults?side=' + encodeURIComponent(side || ''));
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
  // at(), like every other call here. It read the route directly and so asked
  // the root of the origin, which is the one address the panel does not answer
  // — every Logs button opened on "404 page not found", both ends of every
  // tunnel, and the viewer sat on "Reading" for a request that had already
  // failed.
  const r = await fetch(at(url), { cache: 'no-store' });
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
/* A download is a navigation rather than a fetch, so it does not go through
   get/post — which is exactly how it came to be the one address in this file
   written without at(). Both Download buttons pointed at /api/backup/export on
   a panel that answers nothing outside its base path, so the backup an operator
   was told to take before an update was a 404. */
export const backupExportURL = () => at('/api/backup/export');
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

/* Two-factor. The panel is root on this machine and a password is the
   credential most likely to be reused or phished, so the second factor is the
   one thing here that is about the panel itself rather than about a tunnel.

   Four calls because it is four separate decisions: look at the state, begin
   enrolling, prove the app holds the secret, and turn it off again — and the
   last two are the ones that must not be one call, because confirming needs a
   code and disabling needs the password. */
export const totp         = () => get('/api/totp');
export const totpStart    = () => post('/api/totp', new URLSearchParams({ action: 'start' }));
export const totpConfirm  = code => post('/api/totp', new URLSearchParams({ action: 'confirm', code }));
export const totpDisable  = password => post('/api/totp', new URLSearchParams({ action: 'disable', password }));
export const totpRecovery = password => post('/api/totp', new URLSearchParams({ action: 'recovery', password }));
export const setPanelPort = port => post('/api/panelport', new URLSearchParams({ port }));
export const panelCertRead = () => get('/api/panelcert');
/* Form-encoded, because the handler reads r.FormValue. `mode` is not optional:
   without it the endpoint has nothing to apply and refuses the whole request. */
export const panelCert   = ({ mode, domain = '', email = '', certFile = '', keyFile = '' }) =>
  post('/api/panelcert', new URLSearchParams({ mode, domain, email, certFile, keyFile }));

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

/* The panel's own base, for the few places that build an address outside the
   fetch helpers above — a link, a form action. */
export const base = () => BASE;

/* Access control.
 *
 * Tokens are for callers that are not browsers — a Prometheus scraper has no
 * cookie, and /metrics is an endpoint built for scrapers. The secret comes back
 * exactly once, in the response that creates it. */
export const tokens = () => get('/api/tokens');
export const tokenIssue = ({ name, scope, days }) =>
  post('/api/tokens', new URLSearchParams({ name, scope, days: String(days) }));
export const tokenRevoke = name =>
  post('/api/tokens', new URLSearchParams({ action: 'revoke', name }));
/* What has been done through this panel. Written by the authorisation guard,
   so an action cannot be permitted without being recorded. */
export const audit = (limit = 200) => get(`/api/audit?limit=${limit}`);

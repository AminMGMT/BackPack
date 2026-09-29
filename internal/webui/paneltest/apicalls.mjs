// Calls every export of panel/js/api.js once, with the arguments the views
// actually pass, and prints each request it made as JSON — for
// TestEveryPanelCallReachesAHandlerThatReadsWhatItSends, which checks them
// against the Go routes.
//
// It is not a *.test.js file, so `node --test` does not pick it up: on its own
// it proves nothing, and only the Go side knows what the handlers read.

// The panel is served under a base path, and every address must carry it: a
// call that forgot at() asked the origin's root, which the panel never answers.
const BASE = '/p4n3l';
globalThis.document = { documentElement: { dataset: { base: BASE } } };
const unbased = [];
const route = (name, u) => {
  if (!u.pathname.startsWith(BASE + '/')) unbased.push(`${name} (${u.pathname})`);
  return u.pathname.slice(BASE.length);
};

const calls = [];
globalThis.fetch = async (url, opts = {}) => {
  const u = new URL(url, 'http://panel.invalid');
  const call = {
    method: opts.method || 'GET',
    path: u.pathname,
    query: [...u.searchParams.entries()],
    body: 'none',
    fields: [],
  };
  const b = opts.body;
  if (b instanceof URLSearchParams) {
    call.body = 'form';
    call.fields = [...b.entries()].map(([k, v]) => [k, v, 'string']);
  } else if (b instanceof FormData) {
    call.body = 'multipart';
    call.fields = [...b.keys()].map(k => [k, '', 'file']);
  } else if (typeof b === 'string' && opts.headers?.['Content-Type'] === 'application/json') {
    call.body = 'json';
    call.fields = Object.entries(JSON.parse(b))
      .map(([k, v]) => [k, typeof v === 'string' ? v : '', v === null ? 'null' : typeof v]);
  } else if (b !== undefined) {
    call.body = 'unknown:' + Object.prototype.toString.call(b);
  }
  calls.push(call);
  return {
    ok: true, status: 200, statusText: 'OK',
    headers: { get: () => '' },
    json: async () => ({}),
    text: async () => '{}',
  };
};

// The arguments each export is called with. Where a view builds the argument,
// it is the view's own shape (servers.js, settings.js, dashboard.js), so a
// field renamed on one side shows up here. A new export with no entry fails
// the run: a call nobody checks is how the ones this guards against got in.
const node = { name: 'n1', host: '203.0.113.9', sshPort: '22', user: 'root', password: 'pw' };
const samples = {
  stats: [[]], tunnels: [[]],
  tunnelAction: ['start', 'stop', 'restart', 'delete'].map(a => ['t1', a])
    .concat([['t1', 'delete', { alsoFarEnd: '1' }]]),
  adoptCandidates: [['t1', 'n1']], adoptTunnel: [['t1', 'n1', 'p1']], unlinkTunnel: [['t1']],
  restartAll: [[]], tunnelSettings: [['t1']], tunnelEdit: [[{}]], tunnelOptions: [[]],
  nodes: [[]], fleetDrift: [[]], nodesCached: [[]],
  nodeRemove: [['n1']], nodeAdd: [[node]], nodeCredentials: [[node]],
  nodeUpgrade: [['n1']], nodeRefresh: [['n1']], nodeRolloutPlan: [[]], nodeUpgradeAll: [[]],
  nodeRolloutStatus: [[]], nodeRolloutCancel: [[]], nodePin: [['n1', 'soak']], nodeUnpin: [['n1']],
  nodePair: [[{}]],
  tunnelDefaults: [[{ preset: 'balanced', role: 'server', transport: 'tcp' }]],
  tunnelToken: [[]], tunnelSuggest: [[]],
  tunnelCreate: [[{}]], directOptions: [[]], directDefaults: [['iran']], directCreate: [[{}]],
  health: [[]], linkTestStatus: [[]], linkTestRun: [['t1']],
  logs: [['t1'], ['t1', 'peer']], history: [['t1'], ['t1', 30]],
  confHistory: [['t1']], confRestore: [['t1', 1790000000]],
  backupImport: [[new Blob(['{}'])]], autoBackup: [[]], setAutoBackup: [[true], [false]],
  sessions: [[]], sessionRevoke: [['s1']], sessionRevokeOthers: [[]],
  setPassword: [[{ password: 'a-new-password' }]],
  totp: [[]], totpStart: [[]], totpConfirm: [['123456']], totpDisable: [['pw']], totpRecovery: [['pw']],
  setPanelPort: [['8443']], panelCertRead: [[]],
  panelCert: [[{ mode: 'acme', domain: 'panel.example', email: 'a@example.org' }]],
  telegram: [[]],
  telegramSave: [
    [{ token: '1:x', adminId: '1', intervalHours: 6, alertCPU: 90, alertMem: 90, alertDisk: 90 }],
    [{ alertsEnabled: true, alertTunnelDown: false, alertNewRelease: true, relayMode: 'auto', lang: 'en' }],
  ],
  telegramTest: [[]], relays: [[]],
  updateCheck: [[]], updateStart: [[]], updateStatus: [[]], restorePoints: [[]],
  channel: [[]], setChannel: [[true], [false]],
  alerts: [[]],
  tokens: [[]], tokenIssue: [[{ name: 'ci', scope: 'read', days: 30 }]], tokenRevoke: [['ci']],
  audit: [[]],
};
// Exports that build an address instead of fetching one; the address is
// checked as the GET a browser makes when it follows it.
const addresses = { backupExportURL: [[]] };
const notRoutes = new Set(['base']);

const api = await import(new URL('../panel/js/api.js', import.meta.url));
const missing = [];
for (const [name, fn] of Object.entries(api)) {
  if (notRoutes.has(name)) continue;
  if (addresses[name]) {
    for (const args of addresses[name]) {
      const u = new URL(fn(...args), 'http://panel.invalid');
      calls.push({ fn: name, method: 'GET', path: route(name, u), query: [...u.searchParams.entries()], body: 'none', fields: [] });
    }
    continue;
  }
  if (!samples[name]) { missing.push(name); continue; }
  for (const args of samples[name]) {
    const before = calls.length;
    await fn(...args);
    if (calls.length === before) missing.push(name + ' (made no request)');
    for (const c of calls.slice(before)) {
      c.fn = name;
      c.path = route(name, new URL(c.path, 'http://panel.invalid'));
    }
  }
}
if (unbased.length) {
  console.error('api.js addresses outside the panel\'s base path — at() is missing: ' + unbased.join(', '));
  process.exit(1);
}
if (missing.length) {
  console.error('api.js exports with no sample call in paneltest/apicalls.mjs: ' + missing.join(', '));
  process.exit(1);
}
console.log(JSON.stringify(calls));

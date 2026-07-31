// Service worker for the installed panel.
//
// It caches nothing, on purpose. This is a live monitoring dashboard behind a
// login: a cached /api/stats is a lie about a server, and a cached "/" is an
// authenticated page that would keep rendering after the session ended or for
// whoever picks the phone up next. Neither is worth an offline mode nobody
// asked for.
//
// What it is for is installability. A browser will not offer "install" for a
// site without a service worker that handles fetch, so this one handles fetch
// by getting out of the way — every request goes to the network exactly as it
// would without it — and answers a failed *navigation* with a small offline
// card, so a phone out of signal shows something explanatory instead of the
// browser's dinosaur.

const OFFLINE = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<title>Backpack · Offline</title>
<style>
  :root{color-scheme:dark}
  body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;
    background:#0a0a0b;color:#f5f5f7;
    font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Inter,Roboto,sans-serif}
  .card{max-width:340px;text-align:center}
  .mark{width:64px;height:64px;margin:0 auto 18px;border-radius:20px;display:grid;place-items:center;
    background:linear-gradient(145deg,#ff453a,#a12219)}
  svg{width:32px;height:32px;fill:none;stroke:#fff;stroke-width:2;stroke-linecap:round;stroke-linejoin:round}
  h1{font-size:18px;margin:0 0 8px}
  p{font-size:13.5px;line-height:1.6;color:#9a9aa0;margin:0 0 20px}
  button{min-height:46px;padding:0 22px;border:0;border-radius:12px;cursor:pointer;
    font:600 14px inherit;color:#fff;background:#ff453a}
</style></head>
<body><div class="card">
  <div class="mark"><svg viewBox="0 0 24 24"><path d="M8 6V5a4 4 0 018 0v1"/><path d="M5 8h14a2 2 0 012 2v8a3 3 0 01-3 3H6a3 3 0 01-3-3v-8a2 2 0 012-2z"/><path d="M9 12h6"/></svg></div>
  <h1>No connection</h1>
  <p>The panel could not be reached. It runs on your server, so this usually
     means this device is offline &mdash; or the server is.</p>
  <button onclick="location.reload()">Try again</button>
</div></body></html>`;

// Take over immediately: an updated worker should not wait for every tab to
// close first, and there is no cached state for a new one to be inconsistent
// with.
self.addEventListener('install', e => e.waitUntil(self.skipWaiting()));
self.addEventListener('activate', e => e.waitUntil(self.clients.claim()));

self.addEventListener('fetch', event => {
  const req = event.request;
  // Everything but a page load is passed through untouched — including the
  // failure, so the dashboard's own fetch handlers see the real error and can
  // say so in place rather than being handed a page they cannot parse.
  if (req.mode !== 'navigate') return;

  event.respondWith(
    fetch(req).catch(() =>
      new Response(OFFLINE, {
        status: 503,
        headers: {'Content-Type': 'text/html; charset=utf-8'},
      })
    )
  );
});

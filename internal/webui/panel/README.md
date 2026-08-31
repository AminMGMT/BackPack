# panel

A second web UI, built from scratch alongside the existing one. Nothing here
touches `assets/dashboard.html` — that panel keeps serving as it does until
this one is finished and wired into `server.go`.

## Running the mock

```sh
cd internal/webui/panel
python3 -m http.server 8791
# then open http://127.0.0.1:8791/
```

It has to be served rather than opened as a file: ES modules and `fetch` are
both blocked on `file://`.

Everything on screen comes from `mock/*.json`, whose shapes are copied from the
Go structs field for field — `SystemStats`, `TunnelInfo`, `manage.Check`,
`alerthist.State`, `confChange`, `SnapshotMeta`. A screen that renders here
renders against the real server.

`?mock=0` forces the live API, `?mock=1` forces the mock. With neither, the
panel uses the mock unless the page was served with `window.__BACKPACK_LIVE__`
set, which is what the Go handler will do.

## Where the design comes from

The CSS is not written here — it is lifted out of the previews that were signed
off, rule for rule, with the class names intact (`.HH` header, `.RZ` server
strip, `.c7` tunnel card, `.menuM`, `.toastZ`, `.cfZ`). The only things dropped
are the preview's own scaffolding: the device frames, the explanatory notes and
the light/dark demo switch. So a screen here looks like the mock-up because it
*is* the mock-up, wired to data.

## Layout

```
index.html          the shell: boot screen, header, menu, toast — nothing else
css/
  tokens.css        colours, radii, timing; the two themes
  base.css          reset + the three baseline rules everything depends on
  layout.css        shell, header, boot, page grid
  components/*.css  one file per component
js/
  main.js           boot, routes, header, theme
  router.js         hash routing with :params
  api.js            every server route, and the only place fetch() is called
  store.js          one polled copy of the state, shared by every screen
  lib/              dom, format, icons
  ui/               toast, confirm, menu
  views/            one file per screen
mock/               one JSON per endpoint
```

Three rules keep it that way: no screen calls `fetch` (it asks `api.js`), no
screen polls for itself (it subscribes to `store.js`), and no markup carries an
inline SVG (it emits `data-icon` and calls `paintIcons`).

## The CLI menu, and where each item lives

The panel is meant to reach everything `sudo backpack` reaches. This is the map,
and it is also the checklist.

| CLI | panel | endpoint |
|---|---|---|
| 1 Setup Iran | `#/add` (side: iran) | `/api/tunnel/create`, `/api/direct/create` |
| 2 Setup Kharej | `#/add` (side: kharej) | same |
| 3 Manage → Manage Tunnels | `#/` cards + `#/t/:name/edit` | `/api/tunnels`, `/api/tunnel/action`, `/api/tunnel/edit` |
| 3 → Status | `#/` | `/api/stats`, `/api/tunnels` |
| 3 → Health Check | `#/health` | `/api/health` |
| 3 → Link Test | `#/t/:name/link` | `/api/linktest` |
| 3 → Speed Test | `#/t/:name/speed` | `/api/speedtest`, `/api/speedtest/plan` |
| 3 → Tunnel Metrics | `#/t/:name/metrics` | `/api/tunnels` (snapshot), `/api/history` |
| 3 → Restart ALL | header action | `/api/tunnel/action?action=restartall` |
| per-tunnel → Live Log | `#/t/:name/logs` | `/api/logs` |
| per-tunnel → Undo a change | `#/t/:name/history` | `/api/confhist`, `/api/confhist/restore` |
| 4 Backup & Restore | `#/maintenance` → Backup | `/api/backup/export`, `/api/backup/import`, `/api/autobackup` |
| 5 Web Panel | `#/settings` → Panel access | `/api/panelport`, `/api/password`, `/api/panelcert`, `/api/security`, `/api/sessions` |
| 7 Telegram Bot | `#/settings` → Telegram | `/api/telegram`, `/api/telegram/test`, `/api/relays` |
| 8 Update | `#/maintenance` → Update | `/api/update`, `/api/update/status`, `/api/restorepoints`, `/api/channel` |

`api.js` covers **every** `/api/` route the server serves — verified against
`mux.HandleFunc` in `server.go`.

Four CLI entries have no HTTP endpoint at all, so the panel cannot do them and
does not pretend to: **Game Latency Test**, **Exit Health**, **IP Spoofing
Tester** and **Uninstall**. **Auto Refresh**, **Built-in Proxy**, **Optimize**
and **File Locations** are readable from `/api/stats` but not settable over
HTTP; they are shown read-only, pointing at the CLI.

## State

Every screen is built, routed and bound to the endpoint behind it.

| screen | route | reads | writes |
|---|---|---|---|
| Dashboard | `#/` | `/api/stats`, `/api/tunnels` | `/api/tunnel/action` |
| Add tunnel | `#/add` | `/api/tunnel/options` | `/api/tunnel/create`, `/api/direct/create` |
| Edit | `#/t/:name/edit` | `/api/tunnel/settings` | `/api/tunnel/edit` |
| Logs | `#/t/:name/logs` | `/api/logs` | — |
| Metrics | `#/t/:name/metrics` | `/api/tunnels` snapshot | — |
| History | `#/t/:name/history` | `/api/history` | — |
| Link test | `#/t/:name/link` | `/api/linktest` | `/api/linktest` |
| Speed test | `#/t/:name/speed` | `/api/speedtest/plan` | `/api/speedtest` |
| Undo a change | `#/t/:name/undo` | `/api/confhist` | `/api/confhist/restore` |
| Alerts | `#/alerts` | `/api/alerts` | — |
| Health check | `#/health` | `/api/health` | — |
| Maintenance | `#/maintenance` | `/api/update`, `/api/restorepoints`, `/api/autobackup` | `/api/update`, `/api/backup/import`, `/api/autobackup` |
| Settings | `#/settings` | `/api/security`, `/api/telegram`, `/api/sessions`, `/api/channel` | `/api/panelport`, `/api/password`, `/api/panelcert`, `/api/telegram`, `/api/remotetoken` |
| Support | `#/support` | — | — |
| Sign in | `login.html` | — | `POST /login` |

### What is not wired, and why

Five controls in the Edit form — idle timeout, dial timeout, TTL, padding and
source port range — were drawn in the preview but have no field on the server.
They carry `data-unwired`, are disabled, and say so on hover. Posting them as
invented keys would be worse than not offering them.

Four CLI entries have no HTTP endpoint at all, so the panel does not pretend to
do them: **Game Latency Test**, **Exit Health**, **IP Spoofing Tester** and
**Uninstall**. **Auto Refresh**, **Built-in Proxy**, **Optimize** and **File
Locations** are readable from `/api/stats` but not settable over HTTP; they are
shown read-only, pointing at the CLI.


## Add a tunnel, against the source

Every rule the wizard follows is a predicate in the Go, not a guess. This is the
map, so the two can be checked against each other.

### Which transports exist, and in which family

`internal/manage/setup.go` → `transportGroups`. The wizard does not hold this
list; it paints it from `/api/tunnel/options`, so a transport added to the CLI
menu shows up here on its own.

| family | transports |
|---|---|
| TCP | `tcp`, `tcpmux`, `stealth`, `pck` |
| UDP | `udp`, `kcp`, `quic` |
| WebSocket | `ws`, `wsmux`, `wss`, `wssmux` |
| Experimental | `xdi`, `spoof` |

### Which settings each transport actually has

`internal/manage/config.go`. A field outside its transport's set is not shown,
because writing it would put a key in the config that transport never reads.

| shown when | predicate | transports |
|---|---|---|
| the `mux_*` knobs | `isMux` | `tcpmux`, `wsmux`, `wssmux`, `kcp`, `xdi`, `spoof`, `pck` |
| the `kcp_*` knobs and FEC | `isKCP` | `kcp`, `xdi`, `spoof`, `pck` |
| a certificate | `needsTLS` | `wss`, `wssmux` |
| the IP-spoofing drawer | — | `spoof` only |
| the packet-carrier drawer | — | `pck` only |

`isKCP` is the one worth reading twice: KCP is not only the `kcp` transport. It
also carries `xdi` (over ICMP echo), `spoof` (over forged raw IP) and `pck`
(over hand-built TCP segments), and all four are tuned by the same knobs and the
same presets.

### Presets

`internal/manage/preset.go` → `presetOptions`, in that order: **Balance,
Turbo, Aggressive, Throughput**. `presetSuitsTransport` allows Throughput only
when the transport is `kcp`, so it is hidden everywhere else.

### Which side is asked for what

The comments on `NewDirectTunnel` and `NewTunnel` decide this, not the layout.

| field | direct | reverse |
|---|---|---|
| `peerAddr` / `serverAddr` | Iran only — the kharej server it dials | kharej only — the Iran server it dials |
| `ports` | Iran only | Iran only |
| `spoofPeerIp` | kharej only, and only with the `spoof` carrier | — |
| `localIp` / `peerIp` | both; empty picks a free /30 | — |
| `greKey`, `maxConnections`, `bandwidthMbps` | both | `limits.*` |

`spoofPeerIp` is kharej-only for a reason worth keeping in the interface: every
packet that side receives carries a forged source, so it cannot learn where its
peer is and has to be told.

### The last step

A reverse tunnel is set up on Iran first and finished on kharej; a direct one
waits on kharej and is finished from Iran. The side that finishes it has nothing
left to carry anywhere, so it watches the tunnel come up instead of being shown
a summary it does not need. The other side gets the handoff.

### Verified

Every screen was rendered in headless Chrome and read back, not just eyeballed
in code. Three faults came out of that and are fixed:

- **One stylesheet at a time.** Each screen's sheet restyles `.dlg` and
  `.scrim` — some with hundreds of rules — and they were never removed, so the
  second screen you opened wore half the first one's layout.
- **A comment ate its rule.** The porter attached a preceding `/* ... */` to the
  rule after it, then dropped anything starting with a comment. Roughly two
  hundred rules were missing across the screens, which is why bars stacked and
  headers collapsed. Comments are stripped before splitting now.
- **`/api/logs` is text, not JSON.** It was being parsed as JSON, so the log
  would have failed against a real server every time.

And the previews' own sample values are gone: every `value=` in a template is
stripped on load, and each screen fills its fields from the server. A screen
now shows either the truth or nothing — a section with no data behind it is
removed rather than left showing another server's numbers.

### Still to do

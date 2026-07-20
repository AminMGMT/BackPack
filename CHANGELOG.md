# Changelog

All notable changes to Backpack are documented here.

## v1.5.0 — 2026-07-18

### Added
- **New transport: TCP + Stealth.** A TCP tunnel wrapped in an encrypted record layer
  (Noise, NNpsk0) that has **no handshake to fingerprint** — on the wire it is
  two short bursts of what looks like random data, followed by an encrypted
  stream that looks the same. There is no TLS ClientHello and no recognisable
  protocol for deep packet inspection to match against, which is the failure
  mode the TLS-based transports are increasingly hitting on filtered routes.

  The pre-shared key is derived from the tunnel token, so the transport needs no
  key of its own, and because that key is mixed in from the very first message,
  a peer without the token cannot produce a message the server will accept: it
  is dropped with no reply, so a probe or a port scan finds a dead port rather
  than a service to fingerprint. It carries TCP like the plain transport — PROXY
  protocol, per-tunnel limits and metrics all apply — with slightly more CPU for
  the encryption. Pick it under **Setup → Stealth**, or switch an existing tunnel
  to it from **Edit**. Reach for it where filtering is heavy; TCP Mux or WSS
  remain the lighter choice on an open route.
- **WSS and WSS Mux now send a browser TLS fingerprint.** A WSS tunnel is meant
  to look like ordinary HTTPS, and at the HTTP layer it already did — a real
  User-Agent, a plausible path. But the TLS ClientHello underneath was Go's, and
  Go's ClientHello has a fingerprint of its own (its cipher list, its curves, the
  order of its extensions) that filtering can pick out even when everything above
  looks right. The handshake now carries the fingerprint of a current Chrome
  build instead, so it blends into ordinary browser traffic. Nothing above TLS
  changes, and trust is unchanged — the certificate is still not verified,
  because the tunnel authenticates with its token. It applies automatically to
  every wss/wssmux tunnel; there is nothing to configure. (Where **Stealth**
  looks like nothing, this looks like a browser.)
- **New transport: UDP + KCP** — a reliable, retransmitting protocol inside UDP
  datagrams, with **forward error correction**: for every 10 packets it sends 3
  (or 4) parity packets, so losses are repaired instantly instead of waiting a
  full round trip for a retransmit. This is the transport to use when the route
  loses packets and TCP keeps backing off. Datagrams are encrypted with a key
  derived from the tunnel token.

  KCP runs over UDP. **If your provider filters UDP it will not help** — test
  before committing to it.
- **Real client IP (PROXY protocol v2).** The service behind the tunnel normally
  sees every connection as coming from the tunnel itself, so a VPN panel counts
  all users as one device and per-user device limits stop working. Turning this
  on prefixes each forwarded connection with a PROXY protocol v2 header carrying
  the user's real IP and port. Available on TCP, TCP Mux, KCP, WS Mux and WSS Mux
  (the plain websocket and raw UDP transports have nowhere to put it). **Off by
  default, and the backend must be set to accept it first** — otherwise it reads
  the header as traffic and every connection breaks.
- **Performance presets: Balance, Turbo and Aggressive**, applied to every
  transport instead of the old yes/no "Best Performance" question.
  - **Balance** — light on CPU and RAM, for a small or shared VPS.
  - **Turbo** — the recommended default. **It is byte-for-byte identical to the
    old Best Performance preset**, so upgrading changes nothing about an
    existing tunnel.
  - **Aggressive** — maximum throughput and noticeably more CPU.

  A tunnel's preset can be changed later from **Edit → Change performance
  preset**. Configs written before this release carry no preset field and are
  left exactly as they are.
- **Link Test** (**Manage → Link Test**): measures latency, jitter and packet
  loss to the far server over TCP (never ICMP — many networks on this route drop
  ping while carrying tunnel traffic fine), then **recommends a transport** and
  explains why: KCP when the link loses packets, TCP Mux when it is jittery or
  clean, WSS when nothing answers at all. It also derives **liveness timers**
  from the measured round trip instead of the fixed 75s/40s defaults, and offers
  to apply them.
- **Load balancing across backup addresses.** Previously the backup addresses
  were only spares. With balancing on, the tunnel's data connections are spread
  over all of them at once, so a single throttled route slows only its own share
  of the traffic. The control channel stays pinned to one address, since it is
  what identifies the peer. **Every address must reach the same server** — a
  second IP of it, another of its ports, or a CDN edge in front of it.
- **Setup menus are grouped by transport family** — TCP, UDP and WebSocket —
  so the choice is made in two short steps instead of one long list.

- **Per-tunnel limits.** A cap on simultaneous forwarded connections and a cap
  on total throughput, for when several services or customers share one link and
  none of them should be able to take all of it. Both off by default —
  **Edit → Limits**.
- **Structured JSON logging** (`log_format = "json"`), for anyone feeding these
  logs to a collector or a script. The default stays human-readable, since the
  usual reader is a person running `journalctl`.
- **You get told when a new version is out.** The CLI shows a line under the
  logo — and marks the Update entry — as soon as a newer release exists, and the
  Telegram bot messages you once per version.

  The check runs in the background and its answer is cached on disk, so nothing
  on the display path ever waits for GitHub: the menu cannot stall on a redraw,
  which matters on a route where the request may fail over through several
  mirrors first. A failed check leaves the previous answer in place rather than
  erasing it. The "already announced" mark is stored on disk too, so restarting
  the panel does not re-announce a version you have already been told about, and
  the notice clears itself once the update is applied. Switch it off under
  **Telegram Bot → Alerts**.
- **Telegram alerts.** The bot no longer only answers when asked — it messages
  you on its own when the processor, memory or disk crosses a threshold, and
  when a tunnel goes down or comes back. Every alert has a matching recovery
  message, because knowing a problem started is only half of it.

  Two things keep it from becoming noise, which is what makes people mute a
  monitoring bot and then miss the outage that mattered. A reading has to fall
  clearly below its threshold before the alert clears, so a value hovering on
  the line produces one message rather than dozens; and a condition that
  persists is repeated at most once per cooldown. The first pass after a restart
  only records tunnel state instead of announcing all of it.

  Defaults: processor 85%, memory 85%, disk 90%, tunnel up/down on, checked
  every 60s, repeated at most every 30 minutes. Existing installs get these on
  upgrade — a bot that never warns you is the thing being fixed — and all of it
  is editable under **Telegram Bot → Alerts**, where 0 turns a threshold off.
  Alerts are watched by the backpack-monitor service (see below), which runs
  independently of the web panel.
- **The Telegram bot reports much more.** Alongside Status it now has **System**
  (processor, memory, disk, swap, load and uptime, with bars), **Tunnels**
  (per-tunnel state, including whether the peer is really connected rather than
  just whether systemd is happy), **Metrics** (traffic, packet loss and FEC
  repairs) and **Alerts**. Everything is reachable both as a button and as a
  command — `/status`, `/system`, `/tunnels`, `/metrics`, `/alerts`, `/webui`,
  `/help` — and the two share one implementation, so they cannot drift apart.
- **Let's Encrypt certificates for wss and wssmux** (**Edit → Certificate**).
  Self-signed stays the default, because it works on a bare IP and most setups
  have no domain.

  The reason to want a real one is not encryption — the client is Backpack's own
  code and does not verify the certificate either way. It is how the connection
  looks from outside: genuine HTTPS on port 443 is never self-signed, so a
  self-signed certificate is a distinguishing mark on a route where being
  distinguishable is the whole problem. A real one removes it, and a CDN in
  front of the tunnel requires one.

  Validation works over the tunnel's own listener when it is on port 443
  (TLS-ALPN), so usually nothing extra needs opening; otherwise an HTTP-01
  responder runs on port 80. Renewal is automatic and needs no restart — the
  listener asks for the current certificate per handshake rather than holding
  the one it started with. The CLI checks that the domain resolves to this
  server before saving, so a typo is caught while the old certificate is still
  in place rather than after a restart.
- **Tunnel Metrics** (**Manage → Tunnel Metrics**): traffic and connection
  counts per tunnel, and for KCP the numbers that actually explain a slow link —
  retransmits, lost and duplicated segments, and **how many packets forward
  error correction repaired**. That last one is the direct answer to "is KCP
  earning its overhead on my route?"
- **Release channels.** The updater can follow **stable** (default) or **beta**,
  so pre-releases can be tested without being pushed to everyone. Switch under
  **Update → Release channel**.
- **Downloaded releases are checksum-verified.** The installer and the updater
  both check the asset's SHA-256 against the published `SHA256SUMS` before
  installing it, and **refuse to install anything they cannot verify** — see
  *Security* below.
- **The Telegram bot picks its own way out, and re-picks it when that breaks.**
  Reaching Telegram from Iran means going out through a tunnel, and choosing
  which one was a question you should never have been asked. The relay is set to
  **Automatic** by default: the bot forwards through whichever tunnel is up, and
  when that tunnel goes down it moves to the next live one on its own. A specific
  tunnel can still be pinned if you want one.
- **Relay diagnosis** (**Telegram Bot → Diagnose**). When the bot cannot reach
  Telegram, the error it surfaces is whatever the HTTP client saw — usually a
  bare `EOF` — and that names the wrong machine. The chain has five links across
  two servers. This walks them in order — bot configured, relay tunnel chosen,
  that tunnel up, relay port open, the peer's own internet, Telegram itself —
  and stops at the first one that is actually wrong. When something other than
  Telegram answers on the relay port, it reads the reply and **says what that
  was** (an HTTP server, an SSH server, a stale SOCKS proxy, or nothing at all)
  instead of reporting a failed handshake.
- **Backup import and export from the web panel** (**Settings**), alongside the
  CLI. Configs can be pulled down and pushed back without SSH.
- **Telegram setup from the web panel** (**Settings**) — token, admin ID, alert
  thresholds and relay choice, all previously CLI-only.
- **Setup checks the address you give it.** Before saving a client tunnel it
  resolves the server address and warns about the two things that silently break
  a tunnel that looks correctly configured: an address that resolves into a
  **CDN** (matched against published IP ranges, not reverse DNS — Cloudflare's
  addresses carry no PTR record naming it), and a domain carrying **both an A
  and an AAAA record**, where the tunnel may connect over IPv6 and fail if IPv6
  does not reach the server or the port is only open for IPv4. That second one
  is the reason a bare IP can work where its own domain does not.

### Changed
- **Monitoring is now its own service, independent of the web panel.** The
  watchdog, the Telegram bot and the alerts used to run inside the panel
  process, which made the panel a dependency of monitoring — backwards. Stopping
  the panel, or the panel crashing, or turning it off because you only wanted
  the CLI, silently stopped dropped tunnels being restarted and stopped every
  alert. Nothing visibly broke; it just quietly stopped watching, which is the
  worst way for a monitor to fail.

  They now run as `backpack-monitor.service`, which depends on nothing but the
  machine being up and restarts itself if it dies. Existing installs pick it up
  automatically — the CLI installs it on launch and the updater installs it as
  part of an update — so there is nothing to do by hand. **Health Check** reports
  on it, and says plainly that dropped tunnels will not be restarted if it is
  down.
- **The web panel now has one fixed theme, matching the CLI.** The accent is the
  same red-orange used by the menu, and the colour picker is gone — the panel and
  the terminal should look like one product rather than two. The CPU, RAM, disk
  and swap gauges follow that accent instead of a green-amber-red scale;
  **green now means exactly one thing, a tunnel that is up**, with amber for one
  that is down. Load is still readable at a glance: a gauge past 85% brightens
  rather than changing colour. An accent saved by an older build is cleared on
  first load, so an upgraded install does not keep a colour the panel no longer
  offers.
- **The panel's tunnel cards were cut back to what you actually read.** State is
  a single dot rather than a word, ports are split into **Tunnel Port** and
  **Forwarded Ports** instead of one undifferentiated list, and the country flag
  is derived from the peer's address rather than being something to configure.
  Sign out moved to the bottom of **Settings**, and Support is pinned to the
  bottom-right corner so it stays put while the page scrolls.
- **The Telegram bot's messages were rewritten.** **Status** leads with the
  things that answer "is it working" — flag, preset, ports, traffic — **System**
  was cut to the numbers worth reading on a phone, and the Tunnels and Metrics
  sections were removed rather than kept as walls of text. `/help` lists what the
  bot can actually do, and a **Backup** button pulls the configs down through
  Telegram. Internal plumbing — the relay port, the SOCKS port, the API host —
  no longer appears anywhere in a message.
- Building from source now requires **Go 1.24 or newer**; the installer checks
  for this and installs a suitable toolchain if needed. Installing from a
  release asset is unaffected — it is a prebuilt binary.

### Security
- **WSS/WSS Mux now serve a decoy website to anything that is not a tunnel.** A
  WSS tunnel is meant to be indistinguishable from an ordinary HTTPS site, but
  answering a browser, a scanner or an active probe with a `401` or a blank close
  gives it away. Every request that is not a genuine tunnel connection — a
  WebSocket upgrade, on a tunnel path, with a valid credential — is now answered
  with a plausible "Welcome to nginx!" page (`200`, `Server: nginx`), so the
  server looks like a normal website. Built in and always on; nothing to
  configure. Combined with the Let's Encrypt certificate and the Chrome TLS
  fingerprint, the server presents as a real HTTPS website to anyone probing it.
- **The WSS credential is bound to the TLS session instead of being sent.** WSS
  and WSS Mux dial with the certificate unverified — the tunnel trusts its token,
  not a CA, and the certificate is often self-signed. That is fine against a
  passive observer but leaves a gap against an active one: on a path the operator
  does not control, something can present its own certificate, terminate the TLS,
  and read the bearer token the client sends next — which is all an impostor
  needs. So the token is no longer sent. Each side derives RFC 5705 keying
  material from its own side of the TLS session, and the client proves it holds
  the token by sending `HMAC(token, keying material)`. A man in the middle that
  terminated the TLS has a different session with each side, so the proof it
  received from the client does not match what the server expects, and it never
  learns the token to forge one. It works the same for self-signed and Let's
  Encrypt certificates, and costs nothing on the wire. **Both ends of a
  wss/wssmux tunnel must be on this version.**
- **The Telegram relay port now listens on loopback only.** The bot reaches
  Telegram by having a server tunnel forward a local port straight to
  `api.telegram.org:443`. That mapping was written as a bare port number, which
  binds every interface — so the port was reachable from the internet on the
  Iran server's public address, and nothing authenticates a forwarded connection
  (the tunnel token guards the tunnel's own channel, not the ports it exposes).
  Anyone who found the port had a free, unauthenticated TCP relay to Telegram
  going out through the peer's IP. The port is only a random number in a
  40 000-wide range, which a port scan finds in seconds, and the mapping is
  hidden from every port listing, so nobody was going to spot it.

  New tunnels bind `127.0.0.1`. **Existing tunnels are migrated automatically**
  the next time the bot resolves its relay — the mapping is rewritten and the
  tunnel restarted — because it is not visible for you to fix by hand.
- **Updates now refuse to install an archive they cannot verify.** The checksum
  published with a release was checked when it was available and skipped with a
  warning when it was not, and the warning was discarded entirely by the web
  panel. Since the binary is replaced and run as root, an unverifiable download
  is now an error instead: the update stops and points at the offline install.
- **Third-party GitHub proxies were removed from downloads and updates.** The
  archive and its `SHA256SUMS` travelled through the same proxy, so a proxy
  serving a modified binary could serve a matching checksum with it — the
  verification proved nothing in exactly the situation that made a proxy get
  used. Downloads now go direct to GitHub or through the tunnel relay, both of
  which terminate TLS at GitHub. A server that can reach neither installs
  offline; the README has the steps, including a by-hand sequence for anyone who
  would rather not run a script.

### Fixed
- **The updater could not find its own tunnel relay.** It looked for the relay
  mapping by the fixed port 1080, but the port has been derived from the tunnel
  token since it stopped colliding with whatever else was already on 1080. No
  mapping written since then matched, so the relay was never offered and the
  updater was left with a direct connection to GitHub — precisely what a server
  in Iran does not have. Both forms are now recognised.
- **Traffic counts read zero on every transport except KCP.** Tunnel Metrics
  showed real numbers for KCP and nothing at all for TCP, TCP Mux, WebSocket and
  the rest. KCP was the only one being counted, and not by Backpack — the KCP
  library keeps its own counters, so those numbers arrived for free while nobody
  had ever counted the others. Bytes are now counted on every transport.
- **Traffic totals reset to zero whenever a tunnel restarted.** They lived only
  in memory, so a restart, an update or a reboot wiped the history. They are now
  written to disk and **survive a backup restore**: restoring picks up from the
  totals in the backup rather than starting again from zero.
- **The web panel now reports what an update actually did.** It fired the update
  off and reloaded after a fixed delay, discarding the log and any error, so a
  refused or failed update left you looking at the old version with nothing
  explaining why. It now follows the update and shows the outcome.
- **The web panel showed working KCP and UDP tunnels as offline.** It decided
  whether a tunnel was up by looking for connected peers in the TCP socket
  table, which is right for the TCP-based transports and meaningless for the
  datagram ones: a KCP listener is a single unconnected UDP socket that keeps no
  record of who is talking to it, so there was never anything to find. The
  tunnel was carrying traffic the whole time.

  The watchdog already handled this correctly. The panel got it wrong because it
  was answering the same question with its own separate code — so both now go
  through one function, and the panel cannot drift away from it again.
- **"connection refused" now says what to do about it.** When the far side
  cannot reach the service it forwards to, the log used to read
  `local dialer: dial tcp <nil>->127.0.0.1:4545: connect: connection refused` —
  accurate, and almost useless. It does not say which of the two machines the
  fault is on, that the tunnel itself worked, or what to check. The reasonable
  conclusion is that the tunnel is broken, and the reasonable next step is to
  uninstall it.

  It now names the machine, says plainly that the tunnel delivered the
  connection, gives the two causes that actually produce it (the service is not
  running, or it is bound to a public IP rather than 127.0.0.1), and prints the
  command that tells them apart. Timeouts get their own wording, since a
  firewall is a different problem from a missing service. Repeats are suppressed
  for 30 seconds per address: a client retrying once a second used to bury
  everything else in the log.
- **Setup now shows what the far side must be listening on.** The port mapping
  is entered on the Iran server but describes something on the kharej one, and
  that indirection is where it goes wrong. After entering the ports, setup
  prints each one resolved — `443 → 127.0.0.1:443` — so a bare port is concrete
  before the tunnel is built rather than a mystery afterwards.
- **pprof listened on every interface.** When the profiling endpoint was
  enabled in a tunnel config it bound `0.0.0.0`, unauthenticated — and a pprof
  heap dump contains whatever is in memory, including the tunnel token, which is
  all an attacker needs to connect. It is now bound to loopback; reach it with
  `ssh -L 6060:127.0.0.1:6060`. It is off by default and the CLI never enables
  it, so an install that has not hand-edited a config was never exposed.
- **Config files could be read while half written.** Backpack runs as several
  processes and they share these files: the CLI writes them, the panel and the
  monitor read them on a timer. A plain write truncates first, so a reader
  landing in that window saw an empty file — read as "the bot is not
  configured", which for the monitor is a cycle with no alerts. They are now
  written to a temporary file and renamed into place, which is atomic.
- **An update left the monitor running the old binary.** The service unit does
  not change between versions, only the binary it points at, so the
  install-if-missing check correctly found nothing to do — and `systemctl start`
  does nothing to a service that is already running. The update and rollback
  paths now restart it explicitly, and the post-update health check judges it,
  so a version whose monitor cannot start is rolled back instead of kept.
- **A SOCKS5 reply was parsed without checking for a short read.** The bound
  address at the end of the handshake was consumed with the error discarded. It
  never failed there; it failed afterwards, when the caller read the leftover
  bytes as the start of its own response — a Telegram request returning garbage
  rather than an honest connection error.
- **A data race on the control channel, in every transport.** The field was
  written by the handshake goroutine and read by the accept loop, the heartbeat
  and the restart path with no synchronisation, so a reader could observe a
  stale or half-published value — the accept loop refusing connections it should
  have allowed. On the client side `Restart()` replaced the context, the control
  channel, the usage monitor and the counters while the previous generation's
  goroutines were still reading them. Both are now published behind a lock, and
  the race detector runs on every CI build.
- **A possible crash when a peer disconnected mid-check.** The "suspicious
  packet" check asked whether a control channel existed and then asked for its
  address as two separate steps; if it was cleared in between, the address came
  back nil and the type assertion panicked. The address is now read once, and
  compared in a way that is correct for IPv6.
- **IPv6 addresses were built by string concatenation** in three places (the
  server bind address, the client's server address, and the CDN edge address),
  which produces something unresolvable for an IPv6 literal. All now use
  `net.JoinHostPort`. There are end-to-end tests running whole tunnels over IPv6.
- **The watchdog could not see UDP-based tunnels.** It read only the TCP socket
  table, so a UDP tunnel never registered as connected. Client tunnels are now
  checked against connected UDP sockets; for a server, a UDP listener genuinely
  cannot report its peers, so the health screen says that plainly instead of
  implying the tunnel is down.
- **Health Check no longer reports a false failure on UDP transports.** A TCP
  connect cannot test a UDP port, so that check now says so rather than showing
  a ✗ for a working tunnel.

### Notes
- **QUIC was built, tested on a real Iran route, and removed.** It never
  completed a handshake there while KCP on the same link worked at full speed,
  so it was dropped rather than shipped as an option that looks available and
  silently fails. The UDP menu offers UDP and UDP + KCP.
- **Compression was considered and deliberately left out.** Almost everything
  these tunnels carry is already encrypted (VPN or TLS traffic), which does not
  compress — enabling it would burn CPU for no gain while appearing to be a
  speed feature.


## v1.4.0 — 2026-07-18

### Added
- **Automatic failover to backup server addresses.** A client tunnel can hold a
  list of extra server addresses (a second IP, a different port, a CDN edge).
  When the main address stops answering — a filtered IP, a blocked port — the
  client rotates to the next one automatically until something connects, and all
  data connections follow it. Set it during **Setup Client** or later from
  **Manage → Manage Tunnels → Edit → Backup server addresses**.
- **Safe updates with automatic rollback.** Every update first saves a **restore
  point** (the binary plus every config), installs the release, then health-checks
  the panel and all tunnels. If anything fails to come back up it restores the
  previous version by itself. Restore points are also listed under
  **Update → Restore points** so you can roll back on demand.
- **Safe edits.** Changing a port, address or transport keeps the previous config,
  verifies the tunnel actually came back up, and **reverts automatically** if it
  did not — reporting the reason from the log (e.g. "address already in use").
  A bad edit can no longer leave a dead tunnel and a lost config behind.
- **Change transport on an existing tunnel** (tcp ↔ tcpmux ↔ udp ↔ ws ↔ wss ↔
  wsmux ↔ wssmux) without recreating it: the name, token and forwarded ports stay
  as they are, mux settings are filled in, and a TLS certificate is generated
  automatically when switching to wss/wssmux.
- **Health Check** (**Manage → Health Check**): one screen that checks the server
  (BBR, queue discipline, socket buffers, open-file limit, binary, root, systemd),
  the web panel (service, port, firewall hint) and every tunnel (state, listening
  port, port syntax, real TCP reachability, TLS certificate expiry, token
  strength) — with a ✓ / ! / ✗ per item and a plain-language fix for each problem.
- **File Locations** (**Manage → File Locations**): every config, service, backup
  and certificate path with a ✓/✗ so you can see what is installed and where.

### Changed
- Reachability is measured over **TCP, never ICMP** — networks that drop ping no
  longer look "offline" when the tunnel port works fine.
- Backups are pruned to the newest 10 archives, and restore points to the newest
  5, so neither can fill the disk.



## v1.3.0 — 2026-07-14

### Added
- **Edit tunnel ports from the CLI.** Every tunnel now has an **Edit** action
  (Manage → Manage Tunnels → tunnel → Edit): change the **tunnel (control)
  port**, the **forwarded ports** (server) or the **server address** (client).
  Changes rewrite the config and restart the tunnel automatically; the hidden
  Telegram/SOCKS relay mapping is preserved.
- **Change the web-panel port** from the CLI (Web Panel → Change panel port)
  and from the panel itself (Settings → Panel port, with auto-redirect).
- **Release-based install & updates.** `install.sh` now installs the prebuilt
  `backpack_linux_amd64.tar.gz` / `backpack_linux_arm64.tar.gz` release assets
  into **`/root/BackPack`**, and the in-app **Update** detects newer versions
  from GitHub releases and installs them — trying **direct → tunnel SOCKS relay
  → public mirrors**, so it works from Iran without Go or git on the server.
  Works for old clone-based installs too: run Update once from ≤ v1.2.0 (final
  git pull + rebuild) and every update after that comes from the releases.
- **Backups folder.** Backups now live in **`/root/BackPack/backups`** by
  default, and Restore lists the archives there so you just pick one.
- Port entries are **validated** before they reach a config (`443`, `400-450`,
  `443=1.1.1.1:443`, …) — a bad entry used to crash-loop the tunnel service.
  Tunnel names are validated too.

### Changed
- **CLI restyled and reorganized.** Three-color theme (red / white / gray),
  a gray description beside **every** menu option, and a cleaner layout:
  Setup Server, Setup Client, Manage (tunnels · status · restart all · auto
  refresh), Backup & Restore, Web Panel, Optimize, Telegram Bot, Update,
  Uninstall, Exit. The big status header is gone — the panel link & login code
  now live inside the **Web Panel** section.
- **The web panel is monitoring-only** (recommended on the IRAN server): live
  system metrics, tunnel state/ping/logs. Tunnel creation/management, Telegram,
  auto-refresh and backup moved to the CLI; Settings keeps theme, update,
  panel port and password. Support stays.
- **Telegram bot defaults to the tunnel relay.** Configuration now asks which
  tunnel to relay through (a random SOCKS5 relay port is added to it), since
  Iran servers can't reach Telegram directly; “direct” remains available for
  kharej-side setups.
- Watchdog client health-check now matches the peer IP (not just the port), so
  an unrelated outbound connection can no longer mask a dropped tunnel.

### Removed
- Web-panel tunnel create/edit/actions, Telegram setup, auto-refresh and
  backup/restore endpoints (moved to the CLI).
- The `prerequisite/` offline bundle (release assets replaced it).



## v1.2.0 — 2026-07-13

### Added
- **Full backup & restore.** Bundle every tunnel (with its token), the web-panel
  password, Telegram settings, TLS certificates, per-tunnel metadata and the
  auto-refresh schedule into a single portable `.tar.gz` — from the CLI
  (**Backup & Restore**) or the web panel (**Settings → Backup &
  restore**) — and restore it on any server. Restore re-registers and starts
  every tunnel, brings the panel back up, and restores the schedule. The archive
  extractor is hardened against path traversal, and the machine-specific
  `install_path` is never overwritten on the target host.

### Changed
- **Friendlier CLI.** The main menu now shows a short description beside each
  option, and the header shows the web-panel URL, login code, tunnel counts,
  auto-refresh status and the version at a glance.
- **Web panel starts on launch.** The panel is brought up as soon as the menu
  opens, instead of only after the first tunnel is created.

### Security
- **Tokens are no longer written to logs.** Invalid-token handshakes previously
  logged the token value (visible via `journalctl` and the panel log drawer);
  the value is now redacted on both the server and client sides.

### Notes
- No new dependencies — the binary still builds from the Go standard library
  plus the existing modules, so one-click updates keep working on restricted
  (e.g. Iran) networks.

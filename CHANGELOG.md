# Changelog

All notable changes to Backpack are documented here.

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
  (**Manage → Backup & Restore**) or the web panel (**Settings → Backup &
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

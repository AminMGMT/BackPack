# The CLI menu — complete reference

Every option in every menu, including the advanced ones. Open the menu as root:

```bash
sudo backpack
```

Each option carries a short gray description in the terminal; this page is the
long form. For *how to set a tunnel up*, use the
[tutorials](../tutorial/README.md) instead — this page is what each thing **is**.

---

## Main menu

| # | Option | What it does |
|---|--------|--------------|
| 1 | **Setup Iran** | Create the **Iran-side** tunnel that exposes ports. Asks the direction first — reverse or direct. |
| 2 | **Setup Kharej** | Create the **kharej-side** tunnel that holds the real service. Asks the direction first. |
| 3 | **Manage** | Everything about existing tunnels, plus the diagnostics. [↓](#3-manage) |
| 4 | **Backup & Restore** | The whole configuration as one `.tar.gz`. [↓](#4-backup--restore) |
| 5 | **Web Panel** | The monitoring dashboard — port, login, certificate. [↓](#5-web-panel) |
| 6 | **Optimize** | Applies system-wide kernel/network tuning: BBR + fq, socket-buffer ceilings, file-descriptor limits. Answer yes and it prints each change. A reboot is recommended for the file-limit changes. |
| 7 | **Telegram Bot** | Reports, alerts and control from Iran. [↓](#7-telegram-bot) |
| 8 | **Update** | Verified update with automatic rollback. [↓](#8-update) |
| 9 | **Uninstall** | Removes everything Backpack installed. |
| 10 | **Exit** | |

A red banner above the menu appears when a newer release exists. It reads a
cached answer, so a slow or blocked GitHub never delays the menu.

---

## 1 & 2 — the setup wizards

The questions, in the order they are asked. `[Y/n]` marks the default.

### Setup Iran

Both setup entries ask **Reverse or Direct** before anything else. The table
below is the reverse flow; the direct flow is much shorter and is listed after
it.

| Prompt | Notes |
|---|---|
| **Select transport family** | TCP / UDP / WebSocket / Experimental. [Transports](transports.md) |
| **Select … transport** | the variant within that family |
| **Tunnel (control) port** | what the client dials. Refused if already in use for that protocol |
| **Listen on IPv6 as well** `[y/N]` | binds `::`, which accepts IPv4 too on a dual-stack host — "as well", not "instead" |
| **Tunnel name** | names the service (`backpack-<name>`) and the config file |
| **Security token** | a 64-char token is suggested; the client needs the identical string |
| **Exposed ports** | `443`, `443=127.0.0.1:2096`, `443=a:1\|b:2`, comma separated. Setup prints the resolved targets |
| **Carry UDP as well as TCP** `[y/N]` | off by default. [Forwarded UDP](forwarded-udp.md) |
| **TLS certificate** | wss/wssmux only — self-signed, Let's Encrypt, or existing files |
| **Simple token auth** `[y/N]` | wss/wssmux only — for a TLS-terminating proxy in front |
| **IP Spoofing** (4 steps) | spoof only. [IP Spoofing](ip-spoofing.md) |
| **Flag pattern** / interface override | pck only. [TCP + PCK](tcp-pck.md) |
| **Enable PROXY protocol** `[y/N]` | [Real client IP](real-client-ip.md) |
| **Performance preset** | [Presets](performance-presets.md) |
| **Fine-tune the advanced settings by hand** `[y/N]` | [↓ the advanced settings](#the-advanced-settings-fine-tune) |

### Setup Iran / Setup Kharej → Direct

A direct tunnel is always a full IP tunnel wrapped in Backpack's own GRE, so
there is nothing to choose about the shape or the framing — only how it travels.
[Direct tunnel](l3-direct-tunnel.md)

| Prompt | Notes |
|---|---|
| **How should the packets travel?** | **PCK** (looks like an ordinary TCP flow, no socket a firewall can hold), **UDP** (plain, where the path does not interfere), **Spoof** (forged source — test it on your route) |
| **Kharej server address** | Iran side only. Iran dials out, so it needs no inbound port of its own |
| **Tunnel port** | what kharej binds and Iran reaches |
| **Private addresses** | the two ends of the tunnel's own subnet. A free `10.10.N.0/30` is suggested, so a second tunnel does not collide with the first |
| **Tunnel name** | names the service and the config file |
| **Security token** | suggested on the **kharej** side only, and pasted on the Iran side — offering one on both ends is how two different tokens happen |
| **Ports to expose here** | Iran side only, optional. Without them the tunnel just routes |
| **The Iran server's real IP** | spoof carrier, kharej side only: the peer forges every source, so this side has to be told where replies go |
| **How should the tunnel be tuned?** | Turbo / Balance / Aggressive — the queue and the socket buffers. [Presets](performance-presets.md) |
| **Fine-tune the advanced settings by hand** `[y/N]` | starting MTU, interface name, GRE key, segment cap, caps |

The MTU is not a question worth agonising over: the tunnel measures the path
once it is up and corrects the interface itself.

### Setup Kharej

| Prompt | Notes |
|---|---|
| **Select transport family / transport** | must match the server |
| **Server address** | the Iran IP or a domain. Resolved and checked: a CDN in front of a raw transport, or an AAAA record that would send the tunnel over IPv6, is warned about |
| **Server tunnel port** | the same one the server binds |
| **Tunnel name** | |
| **Security token** | the **same** string as the server |
| **Edge IP** | WebSocket family only — connect to a CDN edge rather than the server address |
| **Simple token auth**, **IP Spoofing**, **Flag pattern** | as above, where the transport calls for them |
| **Configure optional connection settings** `[y/N]` | opens the three below |
| ‣ **Proxy URL** | reach the server through `socks5://…` or `http://…`. Not offered on datagram transports — a TCP proxy cannot relay UDP |
| ‣ **Interface / Source address** | pin the tunnel to one uplink. Only asked on a multi-homed host |
| ‣ **Backup addresses** | comma separated; a bare IP reuses the main port. [Failover](failover-load-balancing.md) |
| ‣ **Automatic failover to the healthiest server** `[y/N]` | offered when backups exist — scores every exit and steers to the best. Overrides load balancing |
| ‣ **Load balancing** `[y/N]` | offered instead — spread connections over all addresses at once |
| **Performance preset** | use the same one as the server |
| **Fine-tune the advanced settings by hand** `[y/N]` | |

---

## 3 — Manage

| Option | What it does |
|---|---|
| **Manage Tunnels** | Per-tunnel actions. [↓](#manage--manage-tunnels) |
| **Status** | A live table of every tunnel: role, transport, state, uptime, traffic. |
| **Health Check** | Tests the server, the panel and every tunnel, and prints a **fix** under each problem it finds. Start here when something is wrong. [More](health-check.md) |
| **Link Test** | Measures the real route (latency, jitter, loss) and recommends a transport with matching timers. On a lossy link it names the exact FEC ratio and offers to apply it. [More](choosing-a-transport.md) |
| **Speed Test** | Measures what a tunnel actually carries, end to end — encapsulation, encryption, carrier and path together. Needs both servers: start **Receive** on one, then **Send and measure** on the other, which is the side that reports. Link Test above measures how the path *behaves*; this measures how much it *moves*. Full IP tunnels only. |
| **Game Latency Test** | Estimates the in-game ping a player would feel through this exit — pings the nearest edge of Dota 2, CS2, Valorant, PUBG, Fortnite and others from the kharej server, adds the tunnel leg, and rates the result. Endpoint list at `/etc/backpack/game-endpoints.list`. |
| **Exit Health** | Scores and ranks every server address of a tunnel by `rtt + 2·jitter + 20·loss%`, and offers to pin the healthiest as the primary. The manual companion to health failover. |
| **IP Spoofing Tester** | Two-node test that finds which forged source IPs actually cross the path. [More](ip-spoofing.md#the-ip-spoofing-tester) |
| **Tunnel Metrics** | Traffic and connections per transport and, on KCP, retransmits, loss and packets repaired by FEC. Totals survive restarts. [More](tunnel-metrics.md) |
| **Restart ALL** | Restarts every tunnel at once and reports how many failed. |
| **Auto Refresh** | Restart every tunnel every N hours. `0` disables it. |
| **Built-in Proxy** | Makes this node its own SOCKS5/HTTP backend, so nothing separate has to be installed behind the tunnel. [↓](#manage--built-in-proxy) |
| **File Locations** | Where every config, service, log and backup lives. [More](server-layout.md) |

### Manage → Manage Tunnels

Pick a tunnel, then:

| Action | What it does |
|---|---|
| **Edit** | Everything about the tunnel's configuration. [↓](#manage--manage-tunnels--edit) |
| **Start / Stop / Restart** | The systemd service for that tunnel. |
| **Live Log** | Streams the journal. Ctrl+C returns. |
| **Delete** | Removes the tunnel permanently, after a confirm. |

### Manage → Manage Tunnels → Edit

The screen opens with a summary of the tunnel's current settings, then offers
only the entries that apply to its role and transport.

**On a server (Iran) tunnel:**

| Entry | What it does |
|---|---|
| **Change tunnel port** | The control port clients dial. Checked against what is already bound *on that protocol*. **Change the client to match**, or it cannot reconnect. |
| **Change forwarded ports** | Enter the **full new list**, not an addition. |
| **Change transport** | Switches the carrier, keeping name, token and ports. **Switch the other side too.** A TLS transport generates a self-signed certificate automatically if needed. |
| **Change performance preset** | Rewrites **every** tuning value — buffers, pool size, mux windows, and on KCP the ARQ and FEC settings. Use the same preset on both ends. |
| **Real client IP** | PROXY protocol v2. **The service must be set to accept it first** or every connection breaks. [More](real-client-ip.md) |
| **Limits** | Max simultaneous connections and Mbit/s for this tunnel as a whole; `0` = unlimited. Warns below 10 connections — one browser opens more. [More](limits.md) |
| **Forward UDP** | Carry UDP on the exposed ports too. Off by default. [More](forwarded-udp.md) |
| **TCP MSS clamp** | Non-datagram transports only. Leave at 0 unless Health Check reports a smaller path MTU — it prints the number. **Same value on both ends.** [More](mss-clamp.md) |
| **TCP packet flags** | `pck` only. What this end's flag field says; each end decides its own, so they need not match. [More](tcp-pck.md) |
| **IP Spoofing** | `spoof` only. The forged source, the profile, and everything else. [More](ip-spoofing.md) |
| **Certificate** | wss/wssmux only. Switch between self-signed and Let's Encrypt. [More](../tutorial/websocket-tls.md) |

**On a client (kharej) tunnel:**

| Entry | What it does |
|---|---|
| **Change server tunnel port** | Must match the server side. |
| **Change server address** | The Iran IP or domain. |
| **Change transport** | As above — switch both ends. |
| **Backup server addresses** | The full new list; empty clears them. The client walks the list until one answers. [More](failover-load-balancing.md) |
| **Change performance preset** | As above. |
| **Load balancing** | Off: backups are spares, used one at a time. On: connections are spread over all of them at once. **Every address must reach the same server.** |
| **TCP MSS clamp**, **TCP packet flags**, **IP Spoofing** | As on the server, where the transport applies. |

Every edit is written and applied in one go, and **reverted automatically if the
tunnel does not come back up**.

### Manage → Built-in Proxy

This node serves its own SOCKS5 or HTTP proxy on a loopback port, so the tunnel
exit *is* the proxy — no xray, no panel, nothing separate to keep running. Then
forward a tunnel port to `127.0.0.1:<that port>`.

| Prompt | Notes |
|---|---|
| **Proxy type** | SOCKS5 (works for most apps, carries UDP too) or HTTP (for clients that only take an HTTP proxy) |
| **Port to listen on** | loopback only; you choose, nothing is assumed |
| **Require a username/password** | optional — safe to skip, since the proxy binds loopback and is only reachable through the token-authenticated tunnel |

---

## 4 — Backup & Restore

A backup bundles **every tunnel, the web-panel password, Telegram settings, TLS
certificates and the auto-refresh schedule** into one portable `.tar.gz` under
`/root/BackPack/backups`.

| Option | Notes |
|---|---|
| **Create a backup file** | Choose the directory; the file is timestamped. **Keep it private — it contains tokens and the panel password.** |
| **Restore from a backup file** | Pick one from the folder or enter a path. **Overwrites** existing tunnels and settings, after a confirm. The panel is restarted so a restored password takes effect. |

[More](backup-restore.md)

---

## 5 — Web Panel

A monitoring-only dashboard, recommended on the **Iran** server. The header shows
the URL, the login code and the state.

| Option | Notes |
|---|---|
| **Change panel port** | default 7777 |
| **Regenerate login code** | a new random 8-digit code |
| **Set a custom password** | replaces the login code with your own |
| **Certificate** | serve the panel over HTTPS |
| **Restart panel** | also starts it when stopped |
| **Stop panel** | disables the web UI. Monitoring, alerts and the watchdog keep running — they live in a separate service |

[More](web-panel.md)

---

## 7 — Telegram Bot

The header shows the report interval, relay status, alert summary and admin
count.

| Option | Notes |
|---|---|
| **Configure / Update bot** | token, admin id, and how the bot reaches Telegram (automatic through a tunnel peer, always a named tunnel, or direct) |
| **Alerts** | warn when CPU, memory or disk crosses a threshold, a tunnel goes down or comes back, or a new release appears — each with a recovery message. [More](alerts.md) |
| **Admins** | who else may use the bot, and who may only look |
| **Diagnose relay** | names the exact hop that is broken when messages do not arrive |
| **Send a test report now** | verifies the whole chain |
| **Disable reports** | stops the scheduled reports |

[More](telegram-bot.md)

---

## 8 — Update

| Option | Notes |
|---|---|
| **Check for updates** | Downloads the release, **verifies it against the published SHA-256**, installs it, and rolls back automatically if a tunnel does not come back up. Anything that cannot be verified is refused rather than installed |
| **Restore points** | Go back to a previous version |
| **Release channel** | stable only, or also test pre-releases |

[More](updates.md)

---

## The advanced settings (Fine Tune)

Asked at the end of both setup wizards behind
**"Fine-tune the advanced settings by hand"** (default: no), and available
per-tunnel in the web panel's **Fine Tune** drawer.

**You do not need any of these.** The preset has already filled in every value,
and each prompt starts from the preset's answer — anything you leave alone keeps
it. Editing them marks the tunnel **Custom**, so a later preset change will not
silently overwrite your answers.

### Asked on both roles

| Setting | What it is |
|---|---|
| **TCP_NODELAY** | Sends small writes immediately instead of waiting to coalesce them. On = lower latency, slightly more packets. |
| **Keepalive period (seconds)** | How often the connection is probed for liveness. |
| **Heartbeat interval (seconds, 0 to disable)** | The tunnel's own liveness message. Too short wastes bandwidth on a slow link; too long delays noticing a dead peer. Link Test derives a value from your real round trip. |
| **Log level** | `info` (default), `debug`, `warn`, `error`. |
| **Write logs as JSON** | For a log collector or a script. The human-readable text format stays the default. |

### Server only

| Setting | What it is |
|---|---|
| **Channel size** | The queue between the accept loop and the forwarders. Bigger absorbs bursts at the cost of memory. |

> **Forward UDP** used to be asked here. It is now asked in the main server flow,
> right after the exposed ports — and changed afterwards from
> **Edit → Forward UDP**.

### Client only

| Setting | What it is |
|---|---|
| **Connection pool size** | How many connections are kept ready to carry a forward, so a new one need not be dialled first. |
| **Aggressive pool** | Refills the pool harder — faster under bursty load, more idle connections. |

### Non-datagram transports

| Setting | What it is |
|---|---|
| **TCP MSS clamp (bytes, 0 = automatic)** | The largest TCP payload the tunnel puts in one packet. **Keep it at 0** unless Health Check reports the path cannot carry full-sized packets; it prints the number, and both ends need the same one. Deliberately not part of any preset. [More](mss-clamp.md) |

### Mux transports (tcpmux, wsmux, wssmux)

| Setting | What it is |
|---|---|
| **Mux connections/sessions** | How many real connections carry the multiplexed streams. |
| **Mux version** | smux protocol version, 1 or 2. |
| **Mux frame size** | The largest chunk written per frame. |
| **Mux receive buffer** | Per-session receive window — the ceiling on in-flight data for the session. |
| **Mux stream buffer** | Per-stream window, which is what stops one heavy stream consuming the session. |

### KCP transports (kcp, xdi, spoof, pck)

| Setting | What it is |
|---|---|
| **KCP MTU (bytes)** | Keep it **below the path MTU**. |
| **KCP interval (ms)** | The tick. Lower reacts faster and costs CPU; 10 ms on the gaming presets. |
| **KCP send window / receive window (packets)** | Packets in flight. With congestion control off the window **is** the queue — bigger is not better for ping. |
| **FEC data shards** | Packets per parity group. `0` disables error correction. |
| **FEC parity shards** | Losses repairable per group. |

The shard counts must match on both ends. [More](../tutorial/udp-kcp-fec.md)

### Plain `tcp` only

| Setting | What it is |
|---|---|
| **Zero-copy forwarding (experimental)** | Lets the kernel move bytes directly between the two sockets. The fastest path here and the least proven — try it on a spare tunnel first. Linux only, plain `tcp` only, and only when the tunnel has no bandwidth limit; anything else quietly keeps the buffered path. Purely local, so the two ends need not agree. |

---

<div dir="rtl">

## خلاصهٔ فارسی

همه‌چیز از یک منو در دسترس است: `sudo backpack`.

**منوی اصلی:** ۱) ساخت سرور ایران ۲) ساخت کلاینت خارج ۳) مدیریت ۴) پشتیبان‌گیری
و بازگردانی ۵) پنل وب ۶) بهینه‌سازی کرنل (BBR، بافرها، محدودیت فایل) ۷) ربات
تلگرام ۸) آپدیت (با بازگشت خودکار) ۹) حذف کامل.

**منوی Manage** علاوه بر مدیریت تونل‌ها این‌ها را دارد: **Status** (جدول زنده)،
**Health Check** (مشکل را پیدا می‌کند و زیر هرکدام راه‌حل می‌نویسد — از اینجا
شروع کن)، **Link Test** (مسیر را می‌سنجد و ترنسپورت و نسبت FEC پیشنهاد می‌دهد)، **Speed Test** (اندازه می‌گیرد تونل واقعاً چقدر حجم رد می‌کند — روی یک سرور Receive و روی دیگری Send)،
**Game Latency Test** (تخمین پینگ واقعی بازی از این خروجی)، **Exit Health**
(امتیازدهی و رتبه‌بندی همهٔ آدرس‌های سرور)، **IP Spoofing Tester**،
**Tunnel Metrics**، **Restart ALL**، **Auto Refresh**، **Built-in Proxy**
(خود این سرور SOCKS5/HTTP شود) و **File Locations**.

**منوی Edit** بسته به نقش و ترنسپورت فرق می‌کند. روی سرور: پورت تونل، پورت‌های
forward شده، ترنسپورت، پریست، آی‌پی واقعی کاربر، محدودیت‌ها، **Forward UDP**،
MSS clamp، فلگ‌های TCP (فقط pck)، IP Spoofing (فقط spoof) و گواهی (فقط wss).
روی کلاینت: آدرس و پورت سرور، ترنسپورت، آدرس‌های پشتیبان، پریست و لود بالانس.
هر تغییر یک‌جا نوشته و اعمال می‌شود و **اگر تونل بالا نیامد خودکار برمی‌گردد
عقب**.

**تنظیمات پیشرفته (Fine Tune)** آخر ویزارد پرسیده می‌شود و پیش‌فرضش «نه» است —
**به هیچ‌کدامشان نیاز نداری**، چون پریست همه را پر کرده. اگر دستی عوضشان کنی
تونل «Custom» علامت می‌خورد تا تغییر پریست بعدی جواب‌هایت را پاک نکند. شامل:
TCP_NODELAY، keepalive، heartbeat، سطح لاگ، لاگ JSON؛ روی سرور channel size؛
روی کلاینت اندازهٔ pool و aggressive pool؛ MSS clamp؛ تنظیمات mux؛ تنظیمات KCP و
FEC؛ و zero-copy (فقط روی tcp ساده).

</div>

---
[← Back to the docs index](README.md)

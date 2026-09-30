# The web panel, screen by screen

[Web panel](web-panel.md) explains what the panel is and how to turn it on.
This page is the map: every screen it has, how you get there, what it shows, and
which CLI menu entry does the same job. It exists because the panel is the part
of Backpack hardest to describe in prose — there is no command to quote and no
file to show.

<p align="center"><img src="../img/panel-map.svg" alt="The panel's navigation: three sections in a dock — Overview, Servers, Tunnels — with dialogs opening over whichever section you were on." width="100%"></p>

## How the panel is shaped

Three things, and it helps to know which is which before reading the list:

**The strip** sits above everything and belongs to no screen. It is this machine
right now: processor, memory, and what is moving this second. It keeps updating
while a dialog is open.

**Five sections**, chosen from the dock: **Overview**, **Connection test**,
**Tunnels**, **Terminal**, **Manage**. Only these are pages (Servers is out of
the dock for now, and its address still works). Each renders into the same slot, and
switching between them is the only navigation that replaces what you are
looking at.

**Dialogs** — everything else. A dialog opens *over* the section you were on
and closing it puts you back exactly there, so opening Health check from the
Overview does not land you on the Tunnels when you close it.

Every screen has its own URL, and the URL is a hash: `#/t/fr-relay/metrics`.
That is deliberate — the panel is served by a Go mux that knows nothing about
client-side paths, and a hash reloads correctly everywhere. Any screen below can
be bookmarked or linked to.

Addresses in this page are relative to the panel's secret base path, which is
random per installation: the real URL of the Overview is
`https://your-server:7777/<base>/#/`.

## The sections

### Overview — `#/`

The whole installation on one screen, and the screen the panel opens on.

It repeats nothing from the strip. The strip is now; this is everything that is
not a live meter — what has been carried since the server was set up, which
tunnels carried it, and whatever is currently wrong. Trouble is shown first on
purpose: a page that opens with healthy numbers and hides the one failure among
them is a page that gets glanced at and trusted.

*CLI: the opening screen, plus Manage → Status.*

### Servers — `#/servers`

Out of the dock for now; the address still opens it. The managed fleet: other machines this panel can build and run tunnels on.

Adding one is a form. The panel logs into the server over SSH — which is
already running and already how that machine is administered — so there is no
command to carry to the other end and nothing to wait for. See
[managed servers](managed-servers.md).

*CLI: nothing. There is no Backpack state on a managed server to configure.*

### Connection test — `#/conntest`

The Iran side of the menu's Connection Test, in two panes of one size. The left
one is what to do now: the form, then a 15-minute dial with the one line for the
kharej — `sudo backpack link apply 'backpack://t.…'` — then the test's own
countdown, then *Best for this path*. The right one fills a row per transport as
its echoes come back and, at the end, keeps only what held or was unstable. The
test tunnels are removed when it ends.

*CLI: 0 Connection Test → Iran.*

### Terminal — `#/terminal`

A root shell on this server. It opens only from its button, only for a signed-in
browser (never an API token), and each one opened is written to the audit record
and the alert feed. The shell stays open while you move between sections.

*CLI: none — it is SSH, in the page.*

### Manage — `#/manage`

Auto Refresh (a 24-hour dial counting down to the next restart on the server's
clock), the Built-in Proxy (on this server, or wired to a reverse tunnel with the
kharej's `backpack proxy enable` line; a test that speaks its handshake) and
File Locations (a search bar that opens), on one page.

*CLI: 3 Manage → Auto Refresh, Built-in Proxy, File Locations.*

### Tunnels — `#/tunnels`

The fleet of tunnels as cards, each with its state, its rate chart, and the
actions for it. This is where start, stop, restart and delete live, and where
every per-tunnel screen below is opened from.

*CLI: Manage → Manage Tunnels.*

## Per-tunnel screens

Each takes the tunnel's name in the URL and opens over whichever section you
were on. `fr-relay` below is an example name.

| Screen | Address | What it is | CLI |
| --- | --- | --- | --- |
| Live log | `#/t/fr-relay/logs` | The tunnel's journald output as it happens, coloured by level. The level is read from the line, because journald hands the panel free text. | Manage → Manage Tunnels → Live Log |
| Metrics | `#/t/fr-relay/metrics` | Everything known about one tunnel: traffic, the peer, limits, the bot relay, the certificate, failover, the connection pool, and on a KCP link what the error correction is repairing. Sections with nothing behind them are removed rather than shown empty. | Manage → Tunnel Metrics |
| History | `#/t/fr-relay/history` | The long view: speed over the last day, per-day totals for the week, both uptime figures, and the configuration changes inside the window. | Manage → Tunnel Metrics |
| Link test | `#/t/fr-relay/link` | Twelve TCP connects to the tunnel port, then the transport the measurement argues for. Same branch logic as the CLI's recommendation, in the same order. | Manage → Link Test |
| Setup link | `#/t/fr-relay/share` | The link that builds the tunnel's other end, with the one line for a kharej (`sudo backpack link apply '…'`) and the install-and-set-up line for one without Backpack. Add tunnel shows the same block when it builds only this end. | Manage → Manage Tunnels → Setup Link |
| Traffic limit | `#/t/fr-relay/quota` | How much the tunnel may carry, in and out together, before it goes offline: used, limit and what is left, presets from 50 GB to 10 TB or any amount, and quick adds. Opened from the pencil in the card's bottom band, on the Iran end only. The count survives restarts and updates; the engine takes the tunnel offline the moment it reaches the limit and brings it back when the limit is raised. | — (panel only) |
| Edit | `#/t/fr-relay/edit` | Every setting the tunnel has. The values come from the same call the CLI's edit screen makes, so a tunnel edited here is byte for byte a tunnel edited in the terminal. | Manage → Manage Tunnels → Edit |
| Undo | `#/t/fr-relay/undo` | The configuration history for this tunnel, and a restore back to any earlier version of it. | Manage → Manage Tunnels → Config history |

## Installation screens

These belong to the machine rather than to one tunnel.

| Screen | Address | What it is | CLI |
| --- | --- | --- | --- |
| Add tunnel | `#/add` | Five steps: this server as the Iran end with reverse or direct under it, the tunnel (forwarded ports with a Random), performance (preset cards and what the chosen one sets), optional, done. Only this server's end is built; the last step hands over its setup link — the kharej's `sudo backpack link apply '…'` line. The families and presets are served rather than written into the page. | 1 Setup Iran, 2 Setup Kharej |
| Settings | `#/settings` | Panel access (port, certificate, password, address path, a new login code, restart), security, the Telegram bot, and the release channel. | 5 Web Panel, 7 Telegram Bot, 8 Update → Release channel |
| Alerts | `#/alerts` | The alert history: what fired, when, and about which tunnel. | The alert history |
| Health check | `#/health` | The machine-level checks — the same list the CLI runs, with the same fixes offered. Reached from the warning bar as well as directly. | Manage → Health Check |
| Maintenance | `#/maintenance` | Update (also from an uploaded archive), restore points with roll back, and backup: download or upload one, the archives kept on this server with Test / Restore / Delete, and the off-site copy command. The fleet key stays in the menu on purpose. | 4 Backup & Restore, 8 Update |
| Support | `#/support` | Static. Addresses copy on click. | — |
| Enjoying Backpack? | `#/star` | Static. | — |

## Keeping this page honest

`internal/webui/panelroutes_test.go` reads the routes out of
`panel/js/main.js` and the addresses out of this file, and fails when they
disagree. A screen added to the panel without a line here, or a line here for a
screen that no longer exists, is a failing test rather than a page that quietly
goes wrong.

Screenshots are deliberately not part of this page. A photograph of a panel goes
stale on the next restyle and nothing detects it, whereas the map above is
checked on every build. [The README](../README.md) carries one picture of the
panel for people deciding whether they want it; this page is for people using
it.

---

<div dir="rtl">

## خلاصهٔ فارسی

[Web panel](web-panel.md) می‌گوید پنل چیست و چطور روشنش کنی؛ این صفحه **نقشه**
است: هر صفحه‌ای که پنل دارد، چطور به آن می‌رسی، چه نشان می‌دهد، و کدام گزینهٔ CLI
همان کار را می‌کند. وجود دارد چون پنل سخت‌ترین بخشِ این محصول برای توصیف با نوشته
است — نه دستوری برای نقل‌کردن دارد و نه فایلی برای نشان‌دادن.

**شکل پنل سه چیز است.** **نوار بالا** به هیچ صفحه‌ای تعلق ندارد: همین حالای این
ماشین — پردازنده، حافظه، و آنچه همین ثانیه در حرکت است — و زیر دیالوگ هم به کارش
ادامه می‌دهد. **پنج بخش** که از dock انتخاب می‌شوند: **Overview**، **Connection test**،
**Tunnels**، **Terminal** و **Manage**؛ فقط همین‌ها صفحه‌اند (Servers فعلاً از dock بیرون است). **دیالوگ‌ها** بقیهٔ چیزهایند: روی بخشی که در
آن بودی باز می‌شوند و بستن‌شان دقیقاً به همان‌جا برمی‌گرداند — پس بازکردن Health
check از Overview، موقع بستن تو را روی Tunnels نمی‌اندازد.

هر صفحه نشانی خودش را دارد و نشانی یک hash است: `#/t/fr-relay/metrics`. این عمدی
است — پنل را یک mux در Go سرو می‌کند که چیزی از مسیرهای سمت مرورگر نمی‌داند، و
hash همه‌جا درست reload می‌شود. هر صفحه‌ای در این فهرست را می‌شود bookmark کرد.
نشانی‌ها نسبت به **مسیر مخفی** پنل‌اند که برای هر نصب تصادفی است.

**اسکرین‌شات عمداً اینجا نیست.** عکسِ یک پنل با اولین تغییر ظاهر کهنه می‌شود و هیچ
چیزی این را تشخیص نمی‌دهد؛ این نقشه نمی‌تواند کهنه شود، چون یک تست مسیرها را از
`panel/js/main.js` و نشانی‌ها را از همین صفحه می‌خواند و وقتی با هم نخوانند شکست
می‌خورد.

</div>

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v1.8.5.*

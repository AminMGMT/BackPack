# The CLI menu

Everything Backpack does is reachable from one interactive menu. Open it as root:

```bash
sudo backpack
```

Every option shows a short gray description beside it, so you rarely need to
guess what something does.

## Main menu

| Option | What it does |
|--------|--------------|
| **Setup Server** | Create the Iran-side tunnel that exposes ports. Always set this up first. |
| **Setup Client** | Create the abroad (kharej) tunnel that dials the Iran server and forwards to the real service. |
| **Manage** | Everything about existing tunnels — see below. |
| **Backup & Restore** | Save or restore the whole configuration as one `.tar.gz`. See [Backup & restore](backup-restore.md). |
| **Web Panel** | The monitoring dashboard — link, login code, port. See [Web panel](web-panel.md). |
| **Optimize** | Kernel and network tuning (BBR, buffers, file limits). |
| **Telegram Bot** | Set up status reports and alerts. See [Telegram bot](telegram-bot.md). |
| **Update** | Update to a newer release, with restore points. See [Updates & rollback](updates.md). |
| **Uninstall** | Remove everything. |

## Manage

Per-tunnel control lives here:

- **Edit** — ports, transport, [performance preset](performance-presets.md),
  [backup server addresses and load balancing](failover-load-balancing.md),
  [real client IP](real-client-ip.md), [limits](limits.md), TLS certificate.
- **Start / stop / restart**, **live log**, **delete**, and a **restart all**.
- **Status** — a live table of every tunnel.
- **[Health Check](health-check.md)** — find problems and get a fix for each.
- **[Link Test](choosing-a-transport.md)** — measure the route and get a transport recommendation.
- **[Tunnel Metrics](tunnel-metrics.md)** — traffic, connections and, on KCP, loss and FEC repairs.
- **Auto Refresh** — restart every tunnel every N hours.
- **File Locations** — where every config, service and backup lives. See [Server layout](server-layout.md).

---
[← Back to the main README](../README.md)

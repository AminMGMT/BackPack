# Server layout (file locations)

Everything lives in a tidy, predictable layout. You can also see this any time
from **Manage → File Locations** in the CLI.

| Path | What |
|------|------|
| `/root/BackPack` | The release bundle and downloaded archives. |
| `/root/BackPack/backups` | [Backup](backup-restore.md) `.tar.gz` files. |
| `/etc/backpack` | Tunnel configs (one `.toml` per tunnel) and runtime state. |
| `/usr/local/bin/backpack` | The binary itself. |
| `backpack-<name>.service` | A systemd unit per tunnel. |
| `backpack-monitor.service` | The [monitor service](monitor-service.md). |

The install directory is recorded in `/etc/backpack/install_path`, which is what
the uninstaller reads to know what to remove.

---
[← Back to the main README](../README.md)

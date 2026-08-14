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

<div dir="rtl">

## خلاصهٔ فارسی

همه‌چیز در یک ساختار مرتب و قابل‌پیش‌بینی است. همین را هر وقت خواستی از
`Manage → File Locations` در CLI هم می‌بینی.

`‎/root/BackPack` بستهٔ ریلیز و آرشیوهای دانلودشده ·
`‎/root/BackPack/backups` فایل‌های [پشتیبان](backup-restore.md) ·
`‎/etc/backpack` کانفیگ تونل‌ها (برای هر تونل یک فایل `.toml`) و وضعیت اجرا ·
`‎/usr/local/bin/backpack` خودِ باینری ·
`backpack-<name>.service` یک یونیت systemd به‌ازای هر تونل ·
`backpack-monitor.service` [سرویس مانیتور](monitor-service.md).

مسیر نصب در `/etc/backpack/install_path` ثبت می‌شود و حذف‌کنندهٔ داخلی از روی
همان می‌فهمد چه چیزی را پاک کند.

</div>

---
[← Back to the docs index](README.md)

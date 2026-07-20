# Backup & restore

Everything in one portable `.tar.gz`: every tunnel and token, the web-panel
password, Telegram settings, TLS certificates, and the auto-refresh schedule.
Backups live in `/root/BackPack/backups`.

## Restore

Restoring **re-registers and starts every tunnel**, and traffic totals carry on
from where the backup left off rather than resetting to zero.

## Where you can do it

- the **CLI** — **Backup & Restore**,
- the [web panel](web-panel.md) — **Settings**, or
- the [Telegram bot](telegram-bot.md) — **Backup** button.

> Keep a backup file private — it contains tokens and the panel password.

---
[← Back to the main README](../README.md)

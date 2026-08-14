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

<div dir="rtl">

## خلاصهٔ فارسی

همه‌چیز در یک فایل `.tar.gz` قابل‌حمل: تمام تونل‌ها و توکن‌ها، رمز پنل وب،
تنظیمات تلگرام، گواهی‌های TLS و زمان‌بندی ری‌فرش خودکار. فایل‌ها در
`/root/BackPack/backups` ذخیره می‌شوند.

**بازگردانی** همهٔ تونل‌ها را دوباره ثبت و استارت می‌کند و آمار ترافیک از همان
جایی که بوده ادامه پیدا می‌کند، نه از صفر.

از سه جا می‌شود انجامش داد: منوی CLI (گزینهٔ Backup & Restore)،
[پنل وب](web-panel.md) در بخش Settings، یا دکمهٔ Backup در
[ربات تلگرام](telegram-bot.md).

> فایل پشتیبان را خصوصی نگه دار — توکن‌ها و رمز پنل داخلش است.

</div>

---
[← Back to the docs index](README.md)

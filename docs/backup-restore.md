# Backup & restore

Everything in one portable `.tar.gz`: every tunnel and token, the web-panel
password, Telegram settings, TLS certificates, and the auto-refresh schedule.
Backups live in `/root/BackPack/backups`.

## Restoring onto a different machine

A backup carries everything except one thing: the key that decrypts the stored
root passwords of your **managed servers**. That is deliberate — a backup is a
file people move, and without the seal every copy of it would carry those
passwords in the clear to wherever it went.

Restoring onto the same machine is unaffected: the key is already there and the
restore leaves it alone.

Restoring onto a **different** machine brings the fleet list back without the
credentials, and the panel asks for each server's password again. That is the
right default. If you would rather not re-enter them — the panel machine died
and this is the recovery — keep the key yourself, separately:

**On the machine that has the fleet, before you need it:**

`sudo backpack` → **Backup & Restore** → **Show the fleet key**

Store it somewhere the backup is not. Keeping them together undoes the only
thing sealing them achieves.

**On the new machine, after restoring the backup:**

`sudo backpack` → **Backup & Restore** → **Restore the fleet key**

It refuses if that machine already has a key of its own, because overwriting one
would make every password currently sealed there unreadable and there is no
undo — move the existing key aside yourself if that is genuinely what you mean.

Both entries only appear when there is actually a managed server with a sealed
password, so a single-machine install never sees them.


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

# Web panel

A **monitoring-only** dashboard on **port 7777**, matching the CLI's look. It
shows live CPU / RAM / disk / traffic, each tunnel's state, real ping, and logs.
Backup, Telegram setup and the panel password live in **Settings**.

Run it on the **Iran** server, where you watch things from. It does not create
or change tunnels — that is the CLI's job.

## Getting in

The link and login code are shown in the CLI under **Web Panel** (whose settings
also cover update, panel port and password). Open the port first:

```bash
sudo ufw allow 7777
```

---

<div dir="rtl">

## خلاصهٔ فارسی

یک داشبورد **فقط-پایشی** روی **پورت ۷۷۷۷** با ظاهری هماهنگ با CLI: پردازنده،
حافظه، دیسک و ترافیک زنده، وضعیت هر تونل، پینگ واقعی و لاگ‌ها. پشتیبان‌گیری،
تنظیمات تلگرام و رمز پنل در بخش **Settings** است.

روی سرور **ایران** اجرایش کن، همان‌جا که از آن نظارت می‌کنی. تونل نمی‌سازد و
تغییر نمی‌دهد — آن کارِ CLI است.

**ورود:** لینک و کد ورود در CLI زیر گزینهٔ **Web Panel** نشان داده می‌شود (پورت،
رمز و گواهی پنل هم همان‌جا تنظیم می‌شود). اول پورت را باز کن:
`sudo ufw allow 7777`.

</div>

---
[← Back to the docs index](README.md)

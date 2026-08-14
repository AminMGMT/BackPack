# Monitor service

The watchdog, the [Telegram bot](telegram-bot.md) and the [alerts](alerts.md)
run as their own systemd unit, **`backpack-monitor.service`**, separately from
the [web panel](web-panel.md).

## Why it is separate

Monitoring used to run inside the panel process, which made the panel a
dependency of monitoring — backwards. Stopping the panel, or the panel crashing,
silently stopped dropped tunnels from being restarted and stopped every alert.

Now monitoring depends on nothing but the machine being up, restarts itself if
it dies, and keeps working when the panel is stopped.

## Nothing to do by hand

It is installed automatically — the CLI installs it on launch and the updater
installs it as part of an update. [Health Check](health-check.md) reports if it
is not running.

---

<div dir="rtl">

## خلاصهٔ فارسی

واچ‌داگ، [ربات تلگرام](telegram-bot.md) و [هشدارها](alerts.md) در یک سرویس
جداگانهٔ systemd به نام **`backpack-monitor.service`** اجرا می‌شوند، مستقل از
[پنل وب](web-panel.md).

**چرا جداست؟** قبلاً پایش داخل پروسهٔ پنل بود، یعنی پایش به پنل وابسته بود —
که برعکس منطق است. با خاموش شدن یا کرش کردن پنل، ری‌استارت تونل‌های افتاده و
همهٔ هشدارها بی‌صدا متوقف می‌شد. حالا پایش به هیچ چیز جز روشن بودن سرور وابسته
نیست و اگر بمیرد خودش را دوباره بالا می‌آورد.

**کاری لازم نیست بکنی:** خودکار نصب می‌شود — هم موقع اجرای CLI و هم به‌عنوان
بخشی از آپدیت. [Health Check](health-check.md) می‌گوید اگر اجرا نشده باشد.

</div>

---
[← Back to the docs index](README.md)

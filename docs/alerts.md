# Alerts

Under **Telegram Bot → Alerts**, the bot messages you when something needs
attention — it does not only answer when asked.

## What it watches

- **CPU, memory and disk** crossing a threshold (defaults: **85% / 85% / 90%**).
- A **tunnel going down or coming back**.

Each event also gets a **recovery message** when things return to normal.

## It will not spam you

- A reading must fall **clearly below** its threshold before the alert clears
  (hysteresis), so a value hovering right at the line does not flap.
- A standing alert repeats **at most every 30 minutes**.
- Set any threshold to **0** to stop watching it.

The watching is done by the [monitor service](monitor-service.md), which runs on
its own — so alerts keep working even when the [web panel](web-panel.md) is
stopped.

---

<div dir="rtl">

## خلاصهٔ فارسی

از مسیر **Telegram Bot → Alerts** ربات خودش خبر می‌دهد، نه فقط وقتی بپرسی.

**چه چیزهایی را می‌پاید:** عبور پردازنده، حافظه و دیسک از آستانه (پیش‌فرض
۸۵٪ / ۸۵٪ / ۹۰٪) و پایین رفتن یا برگشتن هر تونل. برای هر رویداد یک **پیام
بازگشت به حالت عادی** هم می‌فرستد.

**اسپم نمی‌کند:** یک مقدار باید به‌وضوح زیر آستانه برگردد تا هشدار پاک شود، و
هشدار فعال حداکثر هر ۳۰ دقیقه تکرار می‌شود. هر آستانه را روی **۰** بگذاری، دیگر
پایش نمی‌شود.

پایش را [سرویس مانیتور](monitor-service.md) انجام می‌دهد که مستقل اجرا می‌شود،
پس هشدارها حتی با پنل وب خاموش هم کار می‌کنند.

</div>

---
[← Back to the docs index](README.md)

# Tunnel Metrics

**Manage → Tunnel Metrics** shows traffic and connection counts per tunnel, and
for **KCP** the numbers that actually explain a slow link:

- retransmits,
- lost and duplicated segments, and
- how many packets **forward error correction (FEC)** repaired.

That last one is the direct answer to "is KCP earning its overhead on my route?"

Traffic totals are counted on **every** transport and are kept across restarts,
so the numbers do not reset when a tunnel bounces (and they carry on after a
[backup restore](backup-restore.md)).

---

<div dir="rtl">

## خلاصهٔ فارسی

**Manage → Tunnel Metrics** ترافیک و تعداد اتصال هر تونل را نشان می‌دهد، و برای
**KCP** همان عددهایی که واقعاً توضیح می‌دهند چرا لینک کند است: ارسال‌های مجدد،
سگمنت‌های گم‌شده و تکراری، و اینکه **تصحیح خطا (FEC) چند پکت را ترمیم کرده**.

عدد آخری جواب مستقیم این سؤال است: «آیا KCP روی مسیر من ارزش سربارش را دارد؟»
اگر ترمیم‌های FEC تقریباً صفر باشد، بی‌دلیل داری پهنای باند parity می‌دهی.

آمار ترافیک روی **همهٔ** ترنسپورت‌ها شمرده می‌شود و بین ری‌استارت‌ها حفظ می‌شود،
پس با بالا و پایین شدن تونل صفر نمی‌شود (و بعد از
[بازگردانی پشتیبان](backup-restore.md) هم ادامه پیدا می‌کند).

</div>

---
[← Back to the docs index](README.md)

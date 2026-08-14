# Real client IP (PROXY protocol v2)

The service behind the tunnel normally sees every connection as coming from the
tunnel itself — so a VPN panel counts all users as one device, and per-user
device limits stop working.

Turn on **Edit → Real client IP (PROXY protocol)** and Backpack prefixes each
forwarded connection with a PROXY protocol v2 header carrying the user's real IP
and port, so the backend sees each user's own address.

## Availability

Works on **TCP, TCP Mux, KCP, WS Mux and WSS Mux**. The plain WebSocket and raw
UDP transports have nowhere to put the header.

## Important

**The backend must be configured to accept PROXY Protocol v2 first.** If it is
not, it reads the header as ordinary traffic and every connection breaks. It is
**off by default** for exactly this reason — enable it on both sides together.

---

<div dir="rtl">

## خلاصهٔ فارسی

سرویسِ پشت تونل به‌طور عادی همهٔ اتصال‌ها را از طرف خودِ تونل می‌بیند — پس پنل
VPN همهٔ کاربران را یک دستگاه می‌شمارد و محدودیت تعداد کاربر از کار می‌افتد.

با روشن کردن `Edit → Real client IP` هر اتصال forward شده با یک هدر
**PROXY protocol v2** حاوی آی‌پی و پورت واقعی کاربر شروع می‌شود.

**در دسترس روی:** TCP، TCP Mux، KCP، WS Mux و WSS Mux. وب‌سوکت ساده و UDP خام
جایی برای این هدر ندارند.

**مهم:** **اول** باید سرویس مقصد طوری تنظیم شود که PROXY Protocol v2 را قبول
کند (در X-UI/Marzban گزینهٔ inbound به نام «Accept Proxy Protocol»)، **بعد**
این را روشن کنی. اگر برعکس عمل کنی، سرویس هدر را داده می‌خواند و **همهٔ
اتصال‌ها خراب می‌شوند**. دقیقاً به همین دلیل پیش‌فرض خاموش است.

</div>

---
[← Back to the docs index](README.md)

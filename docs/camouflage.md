# Decoy site (WSS camouflage)

A WSS tunnel is meant to be indistinguishable from an ordinary HTTPS website —
same port 443, a real domain, a valid certificate. But looking like a website
only to the tunnel's own client is not enough: **anything else that reaches the
server has to see a website too.** A browser that opens the domain, a scanner
sweeping the IP, or an active probe testing the port must all get a plausible
page — not a `401`, not a blank close, not anything that says "this is a tunnel."

So the WSS/WSS Mux server answers as a normal web server for every request that
is **not** a genuine tunnel connection.

## What counts as a genuine tunnel connection

All three must hold, or the request gets the decoy:

1. it is a **WebSocket upgrade**,
2. on a **tunnel path** (`/channel` or `/tunnel…`), and
3. it carries a **valid credential** (the token, or the session-bound proof over
   TLS — see [Transports → WSS](transports.md)).

A browser GET, a request on any other path, or a WebSocket upgrade with the
wrong token fails one of these and is served the decoy instead.

## What a probe sees

A stock **nginx** server. Opening `/` gives the plain "Welcome to nginx!"
placeholder page with `200 OK` — one of the most ordinary things on the web,
the kind a freshly set-up server serves. **Every other path gives nginx's own
`404 Not Found`**, including the tunnel's own `/channel`, because that is what a
server with one `index.html` in its root does. Nothing about any of it hints at
a tunnel. The real tunnel only ever answers a WebSocket upgrade, on its own
path, with the right credential.

The response carries the full header set a real file gets — `Last-Modified`,
`ETag`, `Accept-Ranges`, `Content-Length` — and honours conditional and range
requests, so a probe that sends the `ETag` back is answered `304` the way a file
on disk would be. Serving a bare `200` without those headers is itself a tell:
it says a program answered, not a file.

## Every server is a different server

Looking ordinary is only half of it. If every Backpack install answered with the
same bytes, a single internet-wide scan for that exact response would find every
Backpack server there is — no token and no probing needed. Camouflage everyone
wears identically is a uniform.

So each install derives its own web-server identity from **its tunnel token**:

| | varies per install |
|---|---|
| **`Server`** | a real distro nginx version (`nginx/1.18.0 (Ubuntu)`, `nginx/1.24.0 (Ubuntu)`, …), or bare `nginx` where the build hides its version |
| **`Last-Modified`** | when this server's `index.html` was "written" — somewhere in the previous couple of years |
| **`ETag`** | computed from that date and the page size, in nginx's own format, so it can never contradict them |
| **the page itself** | nginx changed its default page and its error pages in the 1.23 series; each version serves the pages that version really ships |

The token is secret and different everywhere, so the values cannot be predicted
from outside and no two servers share them. It is a hash of the token rather
than a random draw, so a server keeps the same identity across restarts — a real
file does not change its date when the machine reboots.

There is nothing to configure and nothing to keep in sync: the client never
looks at the decoy, so the two ends do not have to agree on it.

## Why this matters against filtering

This is the difference between "the tunnel is encrypted" and "the tunnel is
invisible." Combined with the [Chrome TLS fingerprint](transports.md) on the
client and a Let's Encrypt certificate, the server is, to anyone probing it, a
normal HTTPS website — which is exactly what survives filtering that blocks the
unfamiliar. It is built in and always on for `wss` / `wssmux`; there is nothing
to configure.

> This does not replace picking a good transport. Where the connection itself is
> being filtered rather than fingerprinted, [TCP + Stealth](transports.md) — which
> looks like nothing at all — is the other tool.

---

<div dir="rtl">

## خلاصهٔ فارسی

تونل WSS باید نه‌فقط برای کلاینت خودش، بلکه برای **هر چیز دیگری** که به سرور
می‌رسد شبیه یک سایت HTTPS معمولی باشد. برای همین سرور به هر درخواستی که یک
اتصال واقعی تونل نیست، مثل یک وب‌سرور عادی جواب می‌دهد.

**اتصال واقعی تونل** یعنی هر سه شرط با هم: یک WebSocket upgrade، روی مسیر تونل
(`/channel` یا `/tunnel…`)، و با اعتبارنامهٔ درست. هر چیز دیگری — باز کردن دامنه
در مرورگر، اسکنر، یا upgrade با توکن غلط — صفحهٔ تقلبی می‌گیرد.

**چیزی که یک کاوشگر می‌بیند:** یک nginx معمولی. مسیر `/` صفحهٔ بسیار رایج
«Welcome to nginx!» را با کد ۲۰۰ می‌دهد، و **هر مسیر دیگری — از جمله مسیر خود
تونل یعنی `/channel` — همان `404 Not Found` خود nginx را می‌گیرد**، چون سروری که
فقط یک `index.html` دارد دقیقاً همین کار را می‌کند. پاسخ، مجموعهٔ کامل هدرهای یک
فایل واقعی را دارد (`Last-Modified`، `ETag`، `Accept-Ranges`، `Content-Length`) و
به درخواست‌های شرطی هم مثل یک فایل روی دیسک جواب `304` می‌دهد.

**هر سرور، یک سرور متفاوت:** اگر همهٔ نصب‌های بک‌پک بایت‌به‌بایت یکسان جواب
می‌دادند، یک اسکن سراسری روی همان پاسخ، تمام سرورهای بک‌پک دنیا را پیدا می‌کرد —
بدون توکن و بدون هیچ کاوشی. استتاری که همه یکسان می‌پوشند، «لباس فرم» است.

برای همین هر نصب هویت وب‌سرور خودش را از **توکن تونل خودش** می‌سازد: نسخهٔ nginx
در هدر `Server`، تاریخ `Last-Modified`، مقدار `ETag` (که از همان تاریخ و اندازهٔ
صفحه و با فرمت خود nginx محاسبه می‌شود تا هرگز با آن‌ها در تناقض نباشد)، و حتی
اینکه کدام نسخهٔ صفحهٔ پیش‌فرض سرو شود. توکن مخفی و در هر نصب متفاوت است، پس این
مقادیر از بیرون قابل حدس نیستند؛ و چون هش توکن است نه عدد تصادفی، هویت سرور بعد
از ری‌استارت هم عوض نمی‌شود — یک فایل واقعی با ری‌بوت شدن ماشین تاریخش تغییر
نمی‌کند. هیچ تنظیمی ندارد و دو طرف تونل هم لازم نیست روی آن توافق کنند.

این تفاوت بین «تونل رمزنگاری‌شده» و «تونل نامرئی» است. همیشه روشن است و هیچ
تنظیمی ندارد. اگر مشکل، فیلترِ خودِ اتصال باشد نه اثر انگشتش،
[TCP + Stealth](transports.md) ابزار دیگری است که اصلاً شبیه هیچ چیز نیست.

</div>

---
[← Back to the docs index](README.md)

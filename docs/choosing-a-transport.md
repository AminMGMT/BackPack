# Choosing a transport (Link Test)

Not sure which transport suits your route? Let Backpack measure it.

## Run the test

On the **abroad (kharej)** server, go to **Manage → Link Test**. It measures
latency, jitter and packet loss **over TCP** — never ICMP, because many networks
on this route drop ping while carrying tunnel traffic perfectly well. It then:

- recommends a transport and explains **why**, and
- offers to derive the liveness timers from your real round trip instead of the
  fixed defaults.

## The short version

| Situation | Use |
|-----------|-----|
| The route loses packets | **UDP + KCP + FEC** — repairs loss with forward error correction |
| The link is clean | **TCP Mux** |
| You need to look like a browser loading HTTPS | **WSS** — sends a real Chrome TLS fingerprint |
| The connection itself is being filtered | **TCP + Stealth** — encrypted with no fingerprint at all |

**KCP runs over UDP** — if your provider filters UDP, it will not help; pick a
TCP-based transport instead.

## The families

- **TCP** — reliable stream. `TCP Mux` runs many streams over one connection;
  `TCP + Stealth` wraps it in a Noise layer with no fingerprint.
- **UDP** — datagrams. `UDP + KCP + FEC` is a low-latency gaming tunnel: reliable delivery with always-on FEC.
- **WebSocket** — looks like web traffic. `WSS` / `WSS Mux` add TLS with a
  browser fingerprint and bind the credential to the TLS session.

Change a tunnel's transport any time from **Edit → Change transport**.

---

<div dir="rtl">

## خلاصهٔ فارسی

نمی‌دانی کدام ترنسپورت به مسیرت می‌خورد؟ بگذار بک‌پک اندازه بگیرد.

روی سرور **خارج** برو به **Manage → Link Test**. تأخیر، جیتر و افت پکت را
**روی TCP** می‌سنجد (نه ICMP، چون خیلی از شبکه‌های این مسیر پینگ را می‌اندازند
ولی ترافیک تونل را عبور می‌دهند). بعد یک ترنسپورت پیشنهاد می‌دهد و **دلیلش** را
می‌گوید، و پیشنهاد می‌کند تایمرهای زنده‌بودن را از روی رفت‌وبرگشت واقعی مسیرت
تنظیم کند.

**خلاصهٔ کوتاه:** مسیر پرافت → **UDP + KCP + FEC**؛ لینک تمیز → **TCP Mux**؛
می‌خواهی شبیه مرورگر روی HTTPS باشی → **WSS**؛ خودِ اتصال فیلتر می‌شود →
**TCP + Stealth**.

**KCP روی UDP اجرا می‌شود** — اگر سرویس‌دهنده‌ات UDP را فیلتر کند فایده ندارد و
باید سراغ ترنسپورت‌های TCP بروی. ترنسپورت هر تونل را هر وقت خواستی از
`Edit → Change transport` عوض کن (روی هر دو طرف).

</div>

---
[← Back to the docs index](README.md)

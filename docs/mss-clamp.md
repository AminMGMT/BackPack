# TCP MSS clamp

Most tunnels never need this. Set it only when something has measured the path
and told you to.

## The fault it fixes

Some paths cannot carry a full-sized packet, and drop the oversized ones without
sending back the ICMP message that would say so. Nothing learns: the handshake
and the heartbeats are small enough to arrive, so the tunnel connects, stays
connected, and reports healthy from every angle — while every real transfer
stalls on the first full segment.

**Manage → Health Check** measures this on purpose, because nothing else does:

```
✗ Path MTU   path carries 1260 bytes but the tunnel sends segments of 1448
             → set the MSS clamp to 1208 on both ends
```

## Setting it

- **CLI** — Manage → Manage Tunnels → Edit → **TCP MSS clamp**
- **Web panel** — Edit → **Fine Tune** → *TCP MSS clamp*
- **At setup** — answer *yes* to "Fine-tune the advanced settings by hand"

`0` means automatic, which is the default and what almost every tunnel should
keep.

**Set the same value on both ends.** Each end clamps only what *it* sends, so a
tunnel clamped on one side is still sending full-sized packets from the other —
and those are the ones being dropped.

## Notes

- It applies to the transports that carry TCP segments: `tcp`, `tcpmux`,
  `stealth`, `ws`, `wss`, `wsmux`, `wssmux`. A datagram transport (`udp`, `kcp`,
  `xdi`, `quic`, `spoof`) sizes its packets with the **KCP MTU** instead, and is
  not offered a clamp.
- It is not part of a [performance preset](performance-presets.md) and a preset
  change leaves it alone. It describes the path the tunnel crosses, not how hard
  the tunnel is being pushed.
- Accepted range is 524–1460 bytes. Below that, the path is smaller than IPv4
  guarantees every route can carry, and no clamp will rescue it — look for a
  broken tunnel or VPN in front of yours.

---

<div dir="rtl">

## خلاصهٔ فارسی

بیشتر تونل‌ها هیچ‌وقت به این نیاز ندارند. فقط وقتی تنظیمش کن که چیزی مسیر را
اندازه گرفته و همین را گفته باشد.

**مشکلی که حل می‌کند:** بعضی مسیرها نمی‌توانند پکت با اندازهٔ کامل را عبور دهند و
پکت‌های بزرگ را بدون فرستادن پیام ICMP دور می‌ریزند. چون handshake و heartbeat
کوچک‌اند و می‌رسند، تونل وصل می‌شود، وصل می‌ماند و از هر زاویه سالم به‌نظر
می‌رسد — ولی هر انتقال واقعی روی اولین سگمنت کامل گیر می‌کند.

**Manage → Health Check** دقیقاً همین را می‌سنجد و عدد دقیق را چاپ می‌کند.

**تنظیم:** `Manage → Manage Tunnels → Edit → TCP MSS clamp`. مقدار `0` یعنی
خودکار، که پیش‌فرض است و برای تقریباً همهٔ تونل‌ها درست است.
**همان مقدار را روی هر دو طرف بگذار** — هر طرف فقط چیزی را که خودش می‌فرستد
clamp می‌کند.

**نکات:** فقط روی ترنسپورت‌هایی که سگمنت TCP حمل می‌کنند معنی دارد
(`tcp`, `tcpmux`, `stealth`, `ws`, `wss`, `wsmux`, `wssmux`)؛ ترنسپورت‌های
دیتاگرامی به‌جایش **KCP MTU** دارند. جزو پریست‌ها نیست و تغییر پریست به آن دست
نمی‌زند. بازهٔ مجاز ۵۲۴ تا ۱۴۶۰ بایت است.

</div>

---
[← Back to the docs index](README.md)

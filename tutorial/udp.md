# Setting up a raw UDP tunnel

Raw datagrams, carried as-is. No reliability layer, no ordering, no error
correction — a lost packet stays lost, which is correct for protocols that expect
that and wrong for everything else.

**This is almost never the transport you want.**

> Two things are easy to confuse:
>
> - **the `udp` transport** (this page) — the tunnel itself travels in UDP;
> - **UDP forwarding** ([that page](udp-forwarding.md)) — a forwarded port
>   carries UDP traffic, on *any* transport.
>
> If you are here because "UDP does not work through my tunnel", you want the
> second one. You do not need to change transport.

---

## When it is the right answer

Rarely, and only when all of these hold:

- the service you forward is itself UDP and handles its own loss;
- the route is clean enough that a raw datagram tunnel is not a loss multiplier;
- you specifically do not want a reliability layer in the middle.

On a throttled or lossy Iran route it will do far worse than
**[UDP + KCP + FEC](udp-kcp-fec.md)**, which runs over the same UDP but repairs
loss instead of passing it on. And since v1.7.1 you do not need this transport to
forward a UDP service at all — any transport can, with
[UDP forwarding](udp-forwarding.md) turned on.

---

## The setup

The [TCP walkthrough](tcp.md), with **`UDP` → `UDP`** on both ends.

Two differences:

**The firewall rule is `udp`, not `tcp`:**

```bash
ufw allow 8443/udp      # the tunnel port — this transport binds UDP
ufw allow 443/tcp       # forwarded ports, as usual
ufw allow 443/udp       # …plus this, if you turned UDP forwarding on
```

**No PROXY protocol.** The real-client-IP header needs a stream to prefix, so the
question is not asked on this transport. If your panel needs per-user device
limits, use a TCP-family transport or KCP.

**No MSS clamp** either — there is no TCP segment to clamp.

---

## Check the path first

If your provider filters or throttles UDP, this transport cannot help — and
neither can KCP or QUIC. Test before committing:

```
sudo backpack  →  3. Manage  →  Link Test
```

It measures latency, jitter and loss on the real route and recommends a
transport, with the timers to match.

---

<div dir="rtl">

## خلاصهٔ فارسی

ترنسپورت **UDP** دیتاگرام خام را بدون هیچ لایهٔ قابلیت‌اطمینانی حمل می‌کند —
پکت گم‌شده گم می‌ماند. تقریباً هیچ‌وقت انتخاب درستی نیست.

**دو چیز را قاطی نکن:** «ترنسپورت udp» یعنی خودِ تونل روی UDP می‌رود؛ اما
«UDP forwarding» یعنی یک پورت forward شده ترافیک UDP را هم عبور می‌دهد و روی
**هر** ترنسپورتی کار می‌کند. اگر مشکلت این است که «UDP از تونلم رد نمی‌شود»،
سراغ [این صفحه](udp-forwarding.md) برو و لازم نیست ترنسپورت را عوض کنی.

روی مسیرهای پرافت ایران، **[UDP + KCP + FEC](udp-kcp-fec.md)** به‌مراتب بهتر
است چون افت پکت را ترمیم می‌کند.

راه‌اندازی مثل [TCP](tcp.md) است، فقط: پورت تونل را در فایروال روی **`udp`** باز
کن، و بدان که روی این ترنسپورت PROXY protocol و MSS clamp وجود ندارد.

</div>

---
[← Back to the tutorials](README.md)

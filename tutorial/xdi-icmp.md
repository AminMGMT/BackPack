# Setting up an xDi (ICMP) tunnel

The tunnel rides inside **ping packets**. It is the [KCP](udp-kcp-fec.md)
transport with its packets in ICMP echo requests and replies instead of UDP
datagrams — everything above the packet layer (reliability, error correction,
encryption) is identical.

**It is for one situation:** a network where UDP *and* TCP are filtered but ICMP
is not, because ping is how that network proves itself reachable.

**It is a last resort, not a default.** Slower than the other transports and
heavy on ICMP rate limits.

**Requirements:** Linux, root (or `cap_net_raw`) on **both** ends. Startup is
refused with a plain message if the raw socket is not available.

> Read [TCP](tcp.md) for the parts of the wizard not covered here.

---

## The setup

The TCP walkthrough, with **`Experimental` → `xDi (ICMP)`** on both ends.

The differences are all consequences of ICMP having **no ports**:

- **There is no tunnel port to open.** You still enter one — it names the tunnel
  and keys its session — but nothing binds it and no firewall rule is needed
  for it.
- **The firewall must allow ICMP echo**, in both directions, on the Iran server
  and anywhere in between. Many VPS images and cloud security groups drop it by
  default:

  ```bash
  # ufw: make sure ICMP is not blocked
  ping -c3 IRAN_IP           # from the kharej server — this must work first
  ```

  If plain `ping` between the two machines does not work, this transport cannot
  work either. Test that before anything else.

- **Forwarded ports are opened as usual** on the Iran server (`tcp`, plus `udp`
  if you turned UDP forwarding on).

---

## How several tunnels share one host

A raw ICMP socket receives **every** ping the host sees, including stray ones and
the kernel's own automatic replies. Each xDi tunnel derives a **session tag** from
its token: a packet without this tunnel's tag is not this tunnel's packet and is
dropped without a second look. So several xDi tunnels on one machine stay out of
each other's way, and out of the way of ordinary ping traffic, with nothing to
configure.

---

## What to expect

- **Throughput** is lower than every other transport, and ICMP rate limiting on
  intermediate hops is usually what caps it. The `aggressive` preset drives it to
  the same numbers as KCP where the path permits — which is often not.
- **Latency** behaves like KCP, since it *is* KCP.
- The FEC and window settings are KCP's, and both ends must match on them —
  see [UDP + KCP + FEC](udp-kcp-fec.md).
- The **Throughput preset is not offered** here: xDi builds its packets by hand
  and pays a syscall per datagram, so bandwidth is not what it is for.

If TCP or UDP works at all on your route, use it instead.

---

<div dir="rtl">

## خلاصهٔ فارسی

**xDi** تونل را داخل بسته‌های **پینگ (ICMP)** می‌برد. در واقع همان KCP است که
پکت‌هایش به‌جای دیتاگرام UDP در echo request/reply جا می‌شوند — قابلیت اطمینان،
تصحیح خطا و رمزنگاری‌اش عیناً همان است.

فقط برای یک حالت است: شبکه‌ای که هم TCP و هم UDP را فیلتر می‌کند ولی ICMP را
نه. **آخرین راه‌حل است، نه گزینهٔ پیش‌فرض** — کندتر است و به محدودیت نرخ ICMP
می‌خورد.

**پیش‌نیاز:** لینوکس و دسترسی root روی **هر دو** طرف.

راه‌اندازی مثل [TCP](tcp.md) با انتخاب `Experimental` → `xDi (ICMP)` در دو طرف.
چون ICMP پورت ندارد: پورت تونل فقط اسم و شناسهٔ session است و **نیازی به باز
کردنش در فایروال نیست**، اما **ICMP باید در دو جهت باز باشد**. قبل از هر کاری از
سرور خارج `ping -c3 IRAN_IP` بگیر؛ اگر پینگ ساده کار نکند، این ترنسپورت هم کار
نمی‌کند.

هر تونل xDi از روی توکنش یک برچسب session می‌سازد، پس چند تونل روی یک سرور و
پینگ‌های عادی مزاحم هم نمی‌شوند.

</div>

---
[← Back to the tutorials](README.md)

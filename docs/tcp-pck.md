# TCP + PCK

A TCP transport that does not use the kernel's TCP stack.

```
Setup → TCP → TCP + PCK
```

Linux only. Needs root (or `CAP_NET_RAW`). **Both ends must be on it.**

> **This page is the reverse `TCP + PCK` transport** (`[server]` / `[client]`).
> The **direct** `pck` carrier — `Setup → Direct → PCK`, which runs on
> [paqet](https://github.com/hanselime/paqet) since v2.0.0 — is a different
> engine with its own page: [Direct PCK](direct-pck.md).

## What it is

Every other transport hands its bytes to a socket and lets the kernel produce
the packets. `pck` builds them itself and reads the replies straight off the
network device.

- **Receive** is a packet socket, which taps the driver — *upstream* of
  connection tracking, of every netfilter chain, and of reverse-path filtering.
  A rule that drops the tunnel's packets does not stop them arriving.
- **Send** builds the frame and hands it to the same driver, addressed to the
  next hop. Where that is not possible (a `tun`, a PPP link) it falls back to a
  raw IP socket and lets the kernel route, which costs nothing but the output
  chain.

Above the packet layer it is [KCP](performance-presets.md) — the same reliable,
encrypted, error-correcting transport `kcp` and `xdi` use. That is what supplies
the retransmission and ordering the absent TCP stack would have provided.

## What it is not

**It does not forge anything.** The source address is this machine's real one,
the ports are real, and the replies are routed normally. It is not
[IP Spoofing](transports.md) and needs none of that transport's proving.

What does not exist is the *connection*: no handshake, no socket, no kernel
state on either host. What crosses the wire is segments that look exactly like
an established connection's — timestamps on every one, sequence and
acknowledgement numbers that track the bytes actually exchanged, a normal window
and DSCP marking.

## When to use it

When a normal TCP tunnel connects and then dies, stalls, or is throttled, and
the cause is something acting on the *connection* rather than on the packets:
resets from nowhere, a flow that works for thirty seconds and stops, throughput
that collapses once the transfer is recognised.

If plain `tcp` or `tcpmux` works fine, use those. This costs a raw socket, root,
and a Linux-only dependency.

## Setup

Nothing to look up. The interface, the local address and the next hop are read
from this machine's own routing and neighbour tables.

The wizard asks one thing — the **flag pattern**, i.e. what the TCP flag field
of the tunnel's packets says:

| Pattern | For |
|---|---|
| `PA` | push + ack, what bulk data carries — the default, and right unless you know otherwise |
| `A` | ack only — reads as a stream being drained |
| `PA,A` | alternates, as a real transfer does |
| `PA,A,PA,PAU` | varied — hardest to match on the pattern alone |

Each end decides only its own packets, so the two need not match. Change it
later with **Edit → TCP packet flags**, or in the panel's Fine Tune drawer.

### Overrides

Only for a host where the automatic lookup guesses wrong (some virtualised
networks answer ARP with an address the hypervisor then rewrites):

```toml
pck_interface    = "eth0"
pck_gateway_mac  = "aa:bb:cc:dd:ee:ff"
pck_flags        = ["PA", "A"]
```

## Firewall rules

The kernel is not listening on the tunnel's port, so it answers every arriving
segment with a RST — and to any stateful device in between, that RST is the
connection ending. The tunnel then dies for a reason that appears nowhere.

Backpack installs two narrow rules on start and removes them on stop:

```
filter OUTPUT  -p tcp --sport <port> --tcp-flags RST RST -j DROP
raw PREROUTING -p tcp --dport <port> -j NOTRACK
raw OUTPUT     -p tcp --sport <port> -j NOTRACK
```

They are tagged `backpack-pck-<port>`, so one left behind by a crash is easy to
find:

```bash
iptables-save | grep backpack-pck
```

**Without `iptables` installed the tunnel runs and is unreliable** — it works,
then drops under load or after a pause. The log says so at startup.

## On a busy or lossy link

Since v2.0.0 the reverse `pck` transport controls how much it sends, as the
[direct PCK engine](direct-pck.md#on-a-busy-or-lossy-link) does. Nothing to
configure, and **nothing changes on the wire**: a v2.0.0 end works with a v1.9.0
end, and each end's own sending improves on its own. Next to a v1.9.0 end the
gain is smaller (the old end still fills its own direction's queue): at 1% loss a
v2.0.0 kharej with a v1.9.0 Iran carried 22 Mbit/s down and 24 up, two v1.9.0
ends 29 and 19; update both. `kcp` and `xdi`, which share
the session code, are unchanged.

The presets give every pooled session a window of 512 to 2048 packets and turn
KCP's own congestion control off, so a Turbo pool of eight could have 8192
packets in flight against a path that holds about 1500. The window was the only
brake, and bulk transfers filled the bottleneck's queue and kept it full. On a
test path of 100 Mbit/s each way, 30 ms ± 10 ms and a 1000-packet queue, with no
random loss at all, Turbo carried 28 Mbit/s down and 18 up, a third of its
segments were sent by the retransmission timer, a latency probe took 1.3 s (130
ms without the tunnel), and every short connection waited behind the bulk.

What it does now, per tunnel:

- **The window follows the queue.** One controller moves the windows of the pool
  together between a small floor and the preset's window (now the ceiling, not
  the operating point), from what it reads of **its own direction**: the
  round-trip time against the lowest it has seen lately, less the queue of the
  way back, which the receiver reads from the timestamps KCP segments already
  carry, and segments the local transmit queue refused. It does not read loss, so
  random loss does not shrink the window.
- **The window is a budget for the pool**, divided among the sessions that have
  something to send, so two sessions carrying a transfer out of eight get what
  the path holds and not an eighth of it.
- **Output is paced** over the round trip instead of leaving as a burst, the
  retransmission timer waits for a segment that is only queued, and a lost fast
  retransmission is sent again after a round trip instead of after the timer.
- **Acknowledgements are batched** where the round trip is longer than the KCP
  tick, whatever `kcp_acknodelay` says: immediate acknowledgements were half the
  packets the bottleneck queue counted. On a LAN-short path they stay immediate.

Measured on that path, Turbo, 300 s, a mix of bulk transfers both ways, 200
short connections a second, idle connections, UDP flows and a probe. Single runs
swing by tens of per cent, so these are the runs, not a mean:

| | Mbit/s down / up, before | after |
|---|---|---|
| no loss | 28.4 / 17.9, 28.8 / 19.1 | 61.0 / 15.4, 59.6 / 20.0 |
| 1% loss | 28.7 / 18.5, 30.4 / 19.2 | 53.7 / 24.1, 55.2 / 24.4 |
| 3% loss | 29.1 / 19.0 | 60.4 / 20.4 |
| 5% loss | 29.5 / 18.3 | 45.1 / 28.6 |
| 10% loss | 28.7 / 17.8 | 16.9 to 28.2 / 23.5 to 26.0 (5 runs) |
| reordering, duplication | 27.9 / 17.9, 28.2 / 17.9 | 63.7 / 16.7, 61.9 / 16.7 |

The probe's median fell from 0.9-1.5 s to 110-215 ms at every loss rate, and
the share of segments sent by the timer from 31% to under 1%. Download roughly
doubles; upload is about what it was, to a little better at 1-10% loss, and
stays near 15-28 Mbit/s there. At 10% loss the two directions trade: what one
gains the other gives back, and their sum is what it was.

**Forwarded UDP and new connections under load.** Forwarded UDP rides one of the
tunnel's reliable streams, and in the first v2.0.0 build 53-89% of the test's 4
UDP flows (50 packets a second each) came back, a median of 0.6-3 s later, some
of them 30 s later (74-96% before the controller, with the wait then in the
network's queue). Two things made it, both in how the stream is fed and neither
on the wire:

- **One datagram per stream write.** A write returns only when the frame has
  been through the session's write queue, tens of milliseconds on a loaded
  session, so a flow could not move more datagrams a second than it had writes,
  the queue in front of it stood full at 256 (about 5 s of a 50 pps flow) and
  dropped what arrived. Datagrams already queued now go in one write, in the
  Iran-side flow and in the kharej-side backend reader, which also gets a queue
  of its own, and a full queue drops its oldest datagram, not the newest. Nothing
  is held back to fill a batch.
- **Datagrams waited for a turn.** Streams take turns frame by frame, so a
  400-byte datagram waited behind one frame of every stream on its session. The
  streams of forwarded UDP now go first (at most eight frames in a row while
  other data waits), on both ends, for `pck` only. Another end's send order is
  its own; nothing is negotiated.

Measured on the same path, 300 s, one run each: **99.9-100% of the UDP came back
at every loss rate from 0 to 10%, a median of 150-210 ms and a 99th percentile of
340-740 ms** (median 0.6-2.9 s and a tail of 8-27 s before).
Download and upload did not change. Under a UDP load of about 19 Mbit/s each way
the priority is the difference between 76-81% and 91-97% delivered, at the price
of upload (8-12 against 16-17 Mbit/s), which the UDP then uses.

**Stalls.** A download could wait 3-6 s for its first byte, which the test
reported as a stream that stalled. It was not the path (the sessions were
healthy and the other streams ran at full speed) but the pairing of new
connections: each session sent the backend's name for one connection, waiting
for it to leave the write queue, before it took the next, so eight sessions
paired fewer connections a second than the 200 the test opens, the queue behind
them reached the three seconds a waiting connection is allowed, and a new
download's first byte came just inside that limit. The name is now sent from the
connection's own goroutine and the session's mux slots are the only limit. In
the 11 final 300 s runs seven had no first byte over 2.5 s and the other four
had 17 between them, the longest 3.2 s; every 150 s run before had 5 to 15. The
longest stream stall is 1-3 s there (3-6 s before).
The path controller's pacing is also
bounded now: one session reading a round trip of seconds (a late timer) had
slowed the pace of all of them, which delayed the packets and lengthened the
round trip further, once stopping a 10%-loss run for 27 s.

Next to a v1.9.0 end the sending of each side improves on its own, and the wire
is the same: at 1% loss a v2.0.0 Iran with a v1.9.0 kharej carried 55.8 Mbit/s
down, 7.8 up and 99.7% of the UDP, a v1.9.0 Iran with a v2.0.0 kharej 12.8 down,
25.9 up (two v1.9.0 ends 28.7 / 18.5), and neither lost a session or corrupted
a byte; update both. The test's short connections half-close and wait for a
reply, which the reverse relay has never carried, so they read 0% before and
after. A tunnel whose path goes dark with the link down (a NIC that loses its
link) does not come back by itself on either version, and a path that goes dark
with the link up does.

## Notes

- The port must be free of TCP listeners. Nothing binds it, but a real listener
  there would also receive the tunnel's segments and answer them.
- Cloud provider security groups still apply to the *inbound* direction, since
  they sit ahead of the machine. Open the tunnel's TCP port there as usual.
- The client's source port is derived from the tunnel token, so it is stable
  across reconnects — one firewall rule per tunnel rather than one per
  reconnect.
- Tuned by the [performance presets](performance-presets.md) like any other KCP
  transport; the window they set is the ceiling the controller works under (see
  *On a busy or lossy link*). The [MSS clamp](mss-clamp.md) does not apply: there is no kernel
  socket to clamp, and KCP is already sized under the framing.

---

<div dir="rtl">

## خلاصهٔ فارسی

**TCP + PCK** یک ترنسپورت TCP است که از استک TCP کرنل استفاده نمی‌کند. لینوکس،
root، و **هر دو طرف** باید روی همین باشند.

**دریافت** با packet socket انجام می‌شود که مستقیم به درایور وصل است — *بالاتر
از* connection tracking، همهٔ زنجیره‌های netfilter و فیلتر مسیر برعکس؛ یعنی
قانونی که پکت‌های تونل را drop کند جلوی رسیدنشان را نمی‌گیرد. **ارسال** هم فریم
را خودش می‌سازد و به همان درایور می‌دهد. بالای لایهٔ پکت همان KCP است که قابلیت
اطمینان و ترتیب را تأمین می‌کند.

**هیچ چیزی جعل نمی‌شود** — آدرس و پورت‌ها واقعی‌اند و جواب‌ها عادی مسیریابی
می‌شوند. آنچه وجود ندارد خودِ *اتصال* است: نه handshake، نه سوکت، نه state در
کرنل.

**کی استفاده کن:** وقتی تونل TCP معمولی وصل می‌شود و بعد می‌میرد، گیر می‌کند یا
throttle می‌شود و علتش چیزی است که روی *اتصال* عمل می‌کند — ریست بی‌دلیل، جریانی
که سی ثانیه کار می‌کند و می‌ایستد، یا سرعتی که به‌محض شناخته شدن انتقال فرو
می‌ریزد. اگر `tcp` یا `tcpmux` ساده کار می‌کند، همان‌ها را استفاده کن.

**راه‌اندازی:** چیزی برای پیدا کردن نیست — رابط شبکه، آدرس محلی و next hop از
جدول مسیریابی خود سرور خوانده می‌شوند. فقط یک سؤال دارد: **الگوی فلگ‌ها** که
پیش‌فرضش `PA` درست است و هر طرف فقط پکت‌های خودش را تعیین می‌کند.

**روی لینک شلوغ یا پرتلفات:** از v2.0.0 پنجرهٔ ارسال از روی صف مسیر کنترل می‌شود
(مثل PCK مستقیم)، پنجره بین نشست‌های فعال تقسیم می‌شود، خروجی pace می‌شود و
ACKها روی مسیرهای WAN دسته‌ای می‌شوند. **هیچ‌چیز روی سیم عوض نمی‌شود** و
v2.0.0 با v1.9.0 کار می‌کند. روی مسیر آزمایشی ۱۰۰ مگابیت، دانلود بدون تلفات از
حدود ۲۸ به ۶۰ مگابیت رسید، پینگ از ۱٫۳ ثانیه به حدود ۱۸۰ میلی‌ثانیه؛ آپلود
تقریباً همان است. UDP فوروارد‌شده روی یک stream قابل‌اعتماد می‌رود؛ حالا دیتاگرام‌های
صف‌شده با یک نوشتن می‌روند و stream‌های UDP جلوتر از دادهٔ حجیم ارسال می‌شوند (فقط
سمت ارسال، بدون تغییر روی سیم): روی همان مسیر ۹۹٫۹ تا ۱۰۰٪ UDP از ۰ تا ۱۰٪ تلفات برمی‌گردد
با میانهٔ ۱۵۰ تا ۲۱۰ میلی‌ثانیه. توقف ۳ تا ۶ ثانیه‌ای اولین بایت دانلودها هم که از صف
جفت‌سازی اتصال‌های جدید بود برطرف شد؛ حالا حداکثر ۱ تا ۳ ثانیه است.

**قوانین فایروال:** چون کرنل روی پورت تونل گوش نمی‌دهد، به هر سگمنت با RST جواب
می‌دهد و آن RST برای هر دستگاه stateful وسط راه یعنی «اتصال تمام شد». بک‌پک موقع
استارت دو قانون باریک با تگ `backpack-pck-<port>` اضافه و موقع استاپ حذف می‌کند.
**بدون نصب بودن iptables تونل بالا می‌آید ولی ناپایدار است** و لاگ همین را
می‌گوید. پورت TCP تونل را در security group ابری هم باز کن.

</div>

---
[← Back to the docs index](README.md) · [Step-by-step tutorial →](../tutorial/tcp-pck.md)

---

*Last verified against Backpack v2.0.0.*

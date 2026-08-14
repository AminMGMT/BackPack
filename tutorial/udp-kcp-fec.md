# Setting up a UDP + KCP + FEC tunnel

The **low-latency gaming tunnel**: a reliable, ordered protocol on top of UDP,
with **always-on forward error correction**. For every batch of data packets it
sends parity packets, so the receiver repairs a loss **instantly** instead of
waiting a full round trip for a retransmit.

Every preset runs the same latency-first ARQ — NoDelay, a 10 ms tick, Resend=2,
KCP's own congestion window **off**, immediate ACKs. That is the trade: it spends
bandwidth to hold the ping steady.

**Reach for it when:** gaming, voice, or any route that loses packets where TCP
keeps backing off.
**Do not use it when:** your provider filters or throttles UDP — KCP rides on UDP
and cannot help there. Test first.

> Read [TCP](tcp.md) for the parts of the wizard not covered here.

---

## Check the route before you commit

```
sudo backpack  →  3. Manage  →  Link Test
```

It measures latency, jitter and loss on the real path, recommends a transport,
and — on a lossy link — names the exact **FEC ratio** to run, sized so the parity
always clears the measured loss with burst headroom. It offers to apply it for
you.

| Measured loss | Ratio it recommends |
|---|---|
| a few percent | 10:5 |
| in the teens | 8:8 |
| past 20% | 4:8 |

**Both ends must run the same ratio.**

---

## The setup

The TCP walkthrough, with **`UDP` → `UDP + KCP + FEC`** on both ends.

Differences:

- **The tunnel port is UDP.** `ufw allow 8443/udp` on the Iran server. Forwarded
  ports stay `tcp` (plus `udp` if you turned UDP forwarding on).
- **The token is never sent.** Datagrams are encrypted with a key derived from
  it, so a wrong token means silence, not an error — check it first when nothing
  connects.
- **PROXY protocol is available** here, unlike raw UDP.
- **No MSS clamp** — there is no TCP segment to clamp. KCP has its own MTU
  setting instead.

---

## The presets — this is where the choice matters

| Preset | Tick | Window | For |
|---|---|---|---|
| **Balance** | 10 ms | modest | small or shared VPS |
| **Turbo** | 10 ms | tuned | **most people — start here** |
| **Aggressive** | 10 ms | 2048 pkt, 32 MB buffers | maximum gaming headroom, noticeably more CPU |
| **Throughput** | 20 ms | 4096 pkt, 32 MB per stream, 10:1 parity | **downloads, not play** |

**Throughput is offered only on this transport** (plain `kcp`), and it is *not* a
gaming preset. With congestion control off, a window that large is a queue that
deep — exactly the bufferbloat the gaming presets exist to prevent. Measured on
loopback it carries roughly twice what Aggressive does, at the cost of the steady
ping.

Use **Turbo** or **Aggressive** for play, **Throughput** for transfers. Choosing
Throughput on any other transport is refused with a message saying why, rather
than written to the config and quietly ignored.

Apply the **same preset on both ends**.

---

## The KCP settings, if you fine-tune

Under **Fine-tune the advanced settings by hand**, or the panel's Fine Tune
drawer:

| Setting | What it is |
|---|---|
| **KCP MTU** | bytes per KCP packet. Keep it **below the path MTU** — too large and every packet fragments or is dropped. |
| **KCP interval** | the tick, in ms. Lower reacts faster and costs CPU. 10 ms on the gaming presets. |
| **KCP send window** / **receive window** | packets in flight. With congestion control off, the window *is* the queue — bigger is not better for ping. |
| **FEC data shards** | packets per parity group (0 disables error correction entirely). |
| **FEC parity shards** | losses repairable per group. |

The ratio is `data:parity` — 10:4 means four of any fourteen packets can be lost
and still recovered. **Both ends must match** on the shard counts.

---

## Watching whether it earns its overhead

```
sudo backpack  →  3. Manage  →  Tunnel Metrics
```

On KCP this shows retransmits, lost and duplicated segments, and **how many
packets FEC repaired**. If FEC repairs are near zero, you are paying parity
bandwidth for nothing — lower the ratio. If retransmits are high despite FEC, the
ratio is too thin for the loss on the route.

For a multi-exit gaming setup — several kharej servers, traffic steered to the
healthiest one — see
[failover & load balancing](../docs/failover-load-balancing.md) and
**Manage → Game Latency Test**, which estimates in-game ping to real game
publishers through this exit.

---

<div dir="rtl">

## خلاصهٔ فارسی

**UDP + KCP + FEC** تونل کم‌تأخیر مخصوص بازی است: روی UDP یک پروتکل قابل‌اطمینان
با **تصحیح خطای همیشه‌روشن**. به‌ازای هر دسته پکت داده، چند پکت parity می‌فرستد
تا گیرنده افت را **فوری** ترمیم کند و منتظر ارسال مجدد نماند. برای بازی، ویس و
مسیرهای پرافت. اگر سرویس‌دهنده‌ات UDP را فیلتر یا throttle می‌کند، این هم جواب
نمی‌دهد.

**اول `Manage → Link Test` را بزن.** مسیر را می‌سنجد و نسبت دقیق FEC را پیشنهاد
می‌دهد (افت کم → ۱۰:۵، حدود ۱۵٪ → ۸:۸، بالای ۲۰٪ → ۴:۸). **نسبت باید در دو طرف
یکی باشد.**

راه‌اندازی مثل [TCP](tcp.md)، فقط با انتخاب `UDP` → `UDP + KCP + FEC` در دو طرف،
و پورت تونل را در فایروال روی **`udp`** باز کن.

**پریست‌ها:** Turbo برای اکثر آدم‌ها، Aggressive برای سرور قوی و بازی جدی، و
**Throughput فقط برای دانلود — نه بازی** (پنجرهٔ خیلی بزرگ یعنی صف عمیق و پینگ
ناپایدار). پریست دو طرف را یکی بگذار.

با `Manage → Tunnel Metrics` ببین FEC واقعاً چند پکت را ترمیم می‌کند؛ اگر تقریباً
صفر است داری بی‌دلیل پهنای باند parity می‌دهی.

</div>

---
[← Back to the tutorials](README.md)

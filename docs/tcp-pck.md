# TCP + PCK

A TCP transport that does not use the kernel's TCP stack.

```
Setup → TCP → TCP + PCK
```

Linux only. Needs root (or `CAP_NET_RAW`). **Both ends must be on it.**

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

## Notes

- The port must be free of TCP listeners. Nothing binds it, but a real listener
  there would also receive the tunnel's segments and answer them.
- Cloud provider security groups still apply to the *inbound* direction, since
  they sit ahead of the machine. Open the tunnel's TCP port there as usual.
- The client's source port is derived from the tunnel token, so it is stable
  across reconnects — one firewall rule per tunnel rather than one per
  reconnect.
- Tuned by the [performance presets](performance-presets.md) like any other KCP
  transport. The [MSS clamp](mss-clamp.md) does not apply: there is no kernel
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

**قوانین فایروال:** چون کرنل روی پورت تونل گوش نمی‌دهد، به هر سگمنت با RST جواب
می‌دهد و آن RST برای هر دستگاه stateful وسط راه یعنی «اتصال تمام شد». بک‌پک موقع
استارت دو قانون باریک با تگ `backpack-pck-<port>` اضافه و موقع استاپ حذف می‌کند.
**بدون نصب بودن iptables تونل بالا می‌آید ولی ناپایدار است** و لاگ همین را
می‌گوید. پورت TCP تونل را در security group ابری هم باز کن.

</div>

---
[← Back to the docs index](README.md) · [Step-by-step tutorial →](../tutorial/tcp-pck.md)

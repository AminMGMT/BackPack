# Setting up a TCP + PCK tunnel

A TCP transport that **does not use the kernel's TCP stack**. Backpack builds the
segments itself and reads the replies straight off the network device — upstream
of connection tracking and every netfilter chain — so the machinery that would
normally reset, throttle or drop a long-lived TCP flow has nothing to act on.

**Reach for it when** a plain TCP tunnel connects and then dies, stalls or is
throttled for a reason the logs cannot explain: resets from nowhere, a flow that
works for thirty seconds and stops, throughput that collapses once the transfer
is recognised.

**If plain [TCP](tcp.md) or [TCP Mux](tcp-mux.md) works fine, use those instead.**
This costs a raw socket, root, and a Linux-only dependency.

**Requirements:** Linux, root (or `CAP_NET_RAW`), `iptables` installed, and
**both ends on this transport**.

> Reference page: [docs/tcp-pck.md](../docs/tcp-pck.md). Read [TCP](tcp.md) for
> the parts of the wizard not covered here.

---

## Nothing is forged

Worth saying before you start, because the name suggests otherwise: the source
address is this machine's **real** one, the ports are real, and replies route
normally. This is not [IP Spoofing](ip-spoofing.md) and needs none of that
transport's proving. What does not exist is the *connection* — no handshake, no
socket, no kernel state — while the segments themselves carry the timestamps,
sequence numbers and window a real connection's would.

---

## Before you begin

```bash
uname -s                       # must say Linux
id -u                          # must be 0
iptables --version             # must exist — see "the firewall rules" below
ss -tlnp | grep :8443          # the tunnel port must have NO listener
```

The last one matters: nothing binds the port, but a real listener there would
also receive the tunnel's segments and answer them.

---

## The setup

The TCP walkthrough, with **`TCP` → `TCP + PCK`** on both ends, plus one extra
screen on each side.

### `Flag pattern:`

What the TCP flag field of this end's packets says.

| Pattern | For |
|---|---|
| **`PA`** | push + ack, what bulk data carries — **the default, and right unless you know otherwise** |
| `A` | ack only — reads as a stream being drained |
| `PA,A` | alternates, as a real transfer does |
| `PA,A,PA,PAU` | varied — hardest to match on the pattern alone |

Each end decides only **its own** packets, so the two sides need not match and
changing it here cannot strand the peer. Change it later with
**Manage → Edit → TCP packet flags**.

### `Override the automatic interface / gateway detection [y/N]`

`N`. The interface, local address and next hop are read from this machine's
routing and neighbour tables — there is nothing to look up. Say yes only on a
host where that lookup guesses wrong (some virtualised networks answer ARP with
an address the hypervisor then rewrites), and then fill in only the field you
need; empty keeps the automatic answer.

---

## The firewall rules it installs

The kernel is not listening on the tunnel's port, so it would answer every
arriving segment with a RST — and to any stateful device in between, that RST is
the connection ending. Backpack installs two narrow rules on start and removes
them on stop:

```
filter OUTPUT  -p tcp --sport <port> --tcp-flags RST RST -j DROP
raw PREROUTING -p tcp --dport <port> -j NOTRACK
raw OUTPUT     -p tcp --sport <port> -j NOTRACK
```

Tagged `backpack-pck-<port>`, so one left behind by a crash is easy to find:

```bash
iptables-save | grep backpack-pck
```

**Without `iptables` the tunnel runs and is unreliable** — it works, then drops
under load or after a pause. The log says so at startup, so read it:

```bash
journalctl -u backpack-<name> -n 50
```

Your **cloud provider's security group** still applies to the inbound direction,
because it sits ahead of the machine. Open the tunnel's TCP port there as usual,
and in `ufw`.

---

## Notes

- The client's source port is derived from the tunnel token, so it is stable
  across reconnects — one firewall rule per tunnel, not one per reconnect.
- It is [KCP](udp-kcp-fec.md) above the packet layer, so the
  [performance presets](../docs/performance-presets.md) tune it like any other
  KCP transport.
- The [MSS clamp](../docs/mss-clamp.md) does **not** apply — there is no kernel
  socket to clamp, and KCP is already sized under the framing. The Edit menu
  hides it for this transport.

---

<div dir="rtl">

## خلاصهٔ فارسی

**TCP + PCK** یک ترنسپورت TCP است که از استک TCP کرنل استفاده نمی‌کند: خودش
پکت‌ها را می‌سازد و جواب‌ها را مستقیم از کارت شبکه می‌خواند — یعنی بالاتر از
connection tracking و همهٔ زنجیره‌های netfilter. برای وقتی است که تونل TCP
معمولی وصل می‌شود و بعد بی‌دلیل می‌میرد، ریست می‌خورد یا throttle می‌شود.

**هیچ چیزی جعل نمی‌شود** — آدرس و پورت واقعی‌اند. این با
[IP Spoofing](ip-spoofing.md) فرق دارد.

**پیش‌نیازها:** لینوکس، دسترسی root، نصب بودن `iptables`، و اینکه **هر دو طرف**
روی همین ترنسپورت باشند. پورت تونل هم نباید listener دیگری داشته باشد.

در ویزارد فقط یک سؤال اضافه دارد: **الگوی فلگ‌ها**. جواب پیش‌فرض `PA` درست است و
هر طرف فقط دربارهٔ پکت‌های خودش تصمیم می‌گیرد، پس لازم نیست دو طرف یکی باشند.
سؤال override رابط شبکه را `N` بگذار — همه‌چیز از جدول مسیریابی خوانده می‌شود.

هنگام استارت دو قانون iptables با تگ `backpack-pck-<port>` اضافه می‌کند و موقع
استاپ برمی‌دارد. **اگر iptables نصب نباشد تونل بالا می‌آید ولی ناپایدار است.**
پورت TCP تونل را در security group ابری هم باز کن.

</div>

---
[← Back to the tutorials](README.md)

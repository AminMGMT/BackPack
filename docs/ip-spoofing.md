# IP Spoofing

> **IP Spoofing is a direct-tunnel carrier now, not a reverse transport.**
> `transport = "spoof"` is refused at startup, and the wizard no longer offers
> it under Setup → Experimental. Build it as a **direct tunnel** instead —
> `sudo backpack` → Setup Iran / Setup Kharej → **Direct**, then choose **Spoof**
> as the carrier — and the same forged packets carry the same forwarded ports.
>
> The reason is not tidiness. A reverse tunnel is a control channel plus a pool
> of connections, each one its own session, and a forged-source packet carries
> nothing a receiver can tell those sessions apart by: every one of them arrives
> at the same address. The session layer keys on that address, so they collapsed
> onto one and each new session closed the one before it — the tunnel reported
> itself connected and carried nothing. A direct tunnel has one session, which
> is the shape this carrier can serve.

The reference page for the `spoof` transport: what it is, and what every single
setting does. For a step-by-step first setup, use
**[the IP Spoofing tutorial](../tutorial/ip-spoofing.md)** instead.

```
Setup → Experimental → IP Spoofing
```

Linux only. Needs root (`CAP_NET_RAW`). **Both ends must be on it.**

---

## What it is

The carrier writes its own IP packets and replaces the source address in the
on-wire header with one you choose. Routing still uses the **real** peer — the
server's bind address, the client's remote address — so the packet actually
arrives; only the header says something else.

Above the packet layer it is the [direct tunnel](l3-direct-tunnel.md): a TUN
device on each host, whole IP packets between them, sealed in a Noise session
with replay protection. The forged packets are what carries that.

It is for a path that **blocks, throttles or counts by source address**.

## What it is not

- Not [TCP + PCK](tcp-pck.md), which forges nothing and uses real addresses.
- Not anonymity. The packets still have to reach the peer, and the peer knows
  exactly who you are.
- Not something you can assume works. **Most of the difficulty with this
  transport is proving that a forged source survives your path at all** — see
  [the tester](#the-ip-spoofing-tester).

---

## The paired settings

Everything in a spoof setup is paired, and the pairing is what goes wrong. This
table is the whole compatibility contract:

| Setting | Must match the peer? |
|---|---|
| Packet profile / per-direction profiles | **yes** — mismatched profiles carry nothing |
| ICMP reply mode | **yes** (icmp profile only) |
| Padding, fake TLS | **yes** — they change the wire |
| Forged source ↔ the peer's "expected forged source" | **yes**, if you pin it |
| The other end's real IPv4 | required on the **server** |
| TTL jitter, DSCP, port shuffle, interface, XDP interface, socket buffer, MTU | no — local only |
| Error correction (`fec_data` / `fec_parity`) | **yes** — a receiver expecting a different scheme rebuilds nothing |

The last row is a **carrier** setting, not a spoof one: it lives on the tunnel
and works over every carrier. IP spoofing does not add a way to spread over
several sockets (`paths`) — that carrier already varies its source per packet,
so spreading it would add nothing. Both are documented in
[the direct tunnel reference](l3-direct-tunnel.md#error-correction).

---

## Every setting

Reached from **Manage → Edit → IP Spoofing** (or the panel's spoof drawer). The
config key is given for each, for `/etc/backpack/<name>.toml`.

### Packet profile — `spoof_profile`

What the forged packets are dressed as.

| Value | Looks like | Notes |
|---|---|---|
| **`udp`** | plain datagrams | **the default and the recommendation** — passes nearly everywhere |
| `icmp` | ping traffic | for a path that filters UDP specifically |
| `tcp` | a TCP flow | the receiving side auto-manages an iptables rule to drop the kernel's RSTs |
| `icmpv6` | an ICMPv6 echo inside an **IPv4** packet (protocol 58) | not real IPv6 — the outer packet is IPv4 with a forged IPv4 source. Many filters clamp down on ICMP and UDP and leave protocol 58 alone |
| `proto58` | the same protocol number, **bare** | `icmpv6` with the echo header taken off. Take this where the path passes protocol 58 whatever is inside it; take `icmpv6` where it expects to see an ICMPv6 message |
| `ipip` | IP-in-IP (protocol 4) | the router-to-router encapsulation, which some filters wave through |
| `gre` | GRE (protocol 47), four bytes of header | the same idea, a different number |

The last four carry **no port**, so a receiver on one of them cannot filter the
flow in the kernel and leans on the encryption above. Set
[`spoof_peer_src_ip`](#describing-the-other-end) on one of these, or every packet
of that protocol reaching the host is handed to the cipher.

**Both ends must be set the same**, or the tunnel stops carrying.

### Per-direction profiles — `spoof_uplink`, `spoof_downlink`

A different profile each way, for a path whose filtering is not symmetric — ICMP
survives kharej → Iran while UDP survives Iran → kharej, say. **Uplink is
client → server (kharej → Iran); downlink is server → client.** Empty falls back
to `spoof_profile`, which is the symmetric case.

Almost no path needs this, and both ends must set the same pair.

### Forged source IP(s) — `spoof_src_ip`, `spoof_src_pool`

The address stamped on the packets this machine sends.

- **Empty forges nothing.** The tunnel runs on the machine's real address and
  still works. This is the correct first-run answer.
- **One address** goes in `spoof_src_ip`.
- **Several, comma separated**, become `spoof_src_pool`: the carrier picks one
  each time it (re)connects, so the tunnel is not pinned to a single address a
  firewall might rate-limit or block. `spoof_src_ip` is always a member.

**Only an address the [tester](#the-ip-spoofing-tester) says arrives is worth
putting here.** One that does not leaves a tunnel that connects and carries
nothing, which reads as every other fault there is.

### The other end's real IPv4 — `spoof_peer_ip`

**Server side: required.** The client forges its source, so its packets do not
say where they came from and the server cannot learn where to send replies. It
must be told the client's **real public IPv4** — the address you SSH into it
with, never a forged one.

Until it is set, nothing on the spoof screen can be saved, and the Edit screen
says so up front.

On the client it is optional and defaults to the host part of the server address
it already dials.

### Egress interface — `spoof_interface`

Pins the raw socket to a named device (`eth0`, …) on a multi-homed host, where the
forged source would otherwise leave by the wrong link. Empty lets the kernel
route by the real destination, which is right on a normal single-uplink VPS. Only
asked at setup when the machine actually has more than one interface.

### XDP receive fast path — `spoof_xdp_interface`

Set to a NIC name (`eth0`, …) to receive this tunnel's forged packets through an
**XDP/eBPF program in the kernel**, before the ordinary socket stack — higher
throughput and lower CPU under heavy load. Empty (the default) uses the normal
raw/UDP receive.

- **Pure Go.** The eBPF program is assembled in-process with `cilium/ebpf`; there
  is no clang, libbpf, or CGo, and the binary stays statically linked.
- **Opt-in and best-effort.** If the kernel is too old (the ring-buffer and
  `bpf_xdp_load_bytes` helpers need ~5.18+), the attach fails, or the verifier
  rejects the program, the carrier logs it and **silently falls back** to the
  ordinary receive — turning it on can never cost a working tunnel.
- Needs `CAP_BPF`/`CAP_NET_ADMIN` in addition to the carrier's `CAP_NET_RAW`
  (root has both).
- The `ipip`/`gre` profiles have no port to demux on, so the fast path needs
  `spoof_peer_src_ip` set to know which packets are the tunnel's; without it, it
  falls back for those profiles.
- Local only — it changes nothing on the wire, so the two ends need not agree,
  and each can use it or not.
- **Whether it attached is now in the log.** On start the tunnel says either
  `XDP receive fast path attached to eth0` or `XDP receive unavailable … <reason>`
  — so `spoof_xdp_interface` is no longer a switch with no feedback. Check the
  log to know which of the two you got before relying on it.

> XDP runs before IP reassembly, so it sees fragments rather than reassembled
> datagrams. The carrier sizes its packets under the tunnel MTU, so this only
> matters for a rare oversize packet, which is dropped and re-sent by whatever
> inside the tunnel owns it. Lower `spoof_mtu` if you see it.

### Relay mode — withdrawn

`spoof_mode` and `spoof_forward` were a shape of the reverse spoof transport: a
bare datagram relay to a local UDP socket, so that something bringing its own
reliability — WireGuard, usually — could ride over the forged-source channel
without KCP underneath.

They went with it, and nothing was lost. A direct tunnel is a private network
between the two machines: WireGuard, or anything else, is **routed over it**
rather than piped through it, which is the same traffic with one fewer moving
part and none of the "both ends must be in the same mode" that the relay needed.

## Fingerprint & evasion

**The short answer is Stealth.** The wizard asks one question — *Turn Stealth
on* — and the panel has one switch, and both set the whole group below at once:
padding, TTL and DSCP variation, a moving source port, and the fake TLS record
header where the profile carries one.

That is one answer rather than seven on purpose. Two of these settings change
what goes on the wire and so **must match at the other end**, and five do not.
Set individually, sooner or later a wire-changing one is set on a single end,
and the result is a tunnel that connects and carries nothing — the failure this
carrier is worst at explaining. As a group, "the same answer on both ends" is
one answer.

The individual keys are below, for a setup being tuned against a particular
path. **All are off by default and none is needed for a working tunnel.** Each
costs something — bandwidth, CPU, or a shape a different filter notices instead.
**Change one at a time and test.**

### Describing the other end

| Setting | Key | What it does |
|---|---|---|
| Expected forged source from the other end | `spoof_peer_src_ip` | Pins the source the peer stamps, so anything arriving with a different one is dropped **before the encryption looks at it** — a tighter, cheaper receive path. Empty accepts any source and leaves demux to the port and the encryption: safe, but noisier. Set it to the peer's `spoof_src_ip`. |
| Forged destination address | `spoof_dst_ip` | A forged destination written **only into the cosmetic L4 shim** of the profiles that carry one. The packet is still routed to the real peer. Empty mirrors `spoof_src_ip`. **Ignored by the udp profile.** |

### Sizing

| Setting | Key | What it does |
|---|---|---|
| Fragment sends above this many bytes | `spoof_mtu` | The largest IP packet the carrier emits before it fragments in userspace; the peer's kernel reassembles. 0 uses 1500. **Lower it on a path with a smaller MTU** so oversize packets fragment cleanly rather than being dropped. |
| Socket buffer in bytes | `spoof_sockbuf` | Sizes `SO_SNDBUF`/`SO_RCVBUF` on the carrier's sockets. **This is what lets a forged-source flow reach real bandwidth** — under a burst the kernel parks packets here instead of dropping them before the read loop drains them. 0 uses the 4 MiB carrier default; raise it on a fat, high-latency path. |

### Shape

| Setting | Key | Match peer? | What it does |
|---|---|:--:|---|
| ICMP profile: this pair asks and answers | `spoof_icmp_reply` | **yes** | Makes an ICMP tunnel read as a real ping exchange — client sends Echo Requests, server answers with Echo Replies — instead of both ends sending Requests. Purely cosmetic. Ignored by udp/tcp. |
| Vary the TTL from packet to packet | `spoof_ttl_jitter` | no | Varies the IP TTL across realistic OS defaults {64, 128, 255} instead of a fixed 64, blurring TTL fingerprints. |
| Vary the DSCP field | `spoof_random_dscp` | no | Varies the DSCP/ToS byte across plausible values instead of leaving it 0. |
| Prepend a fake TLS record header | `spoof_fake_tls` | **yes** | Prepends a fake TLS 1.2 record header to each segment so a middlebox reads it as TLS. **TCP profile only.** |
| Randomise the source port of every packet | `spoof_shuffle_port` + `spoof_port_min` / `spoof_port_max` | no | Randomises the L4 **source** port per packet within the range, so the flow does not sit on one port. The destination port stays fixed, so demux is unaffected. Leave both at 0 for the whole ephemeral range. |
| Append random padding to each frame | `spoof_padding` + `spoof_padding_max` | **yes** | Appends 1..max random bytes to every payload (self-describing, so the receiver strips them), defeating size fingerprints. Costs exactly that much bandwidth. |

---

## The IP Spoofing Tester

```
Manage → IP Spoofing Tester
```

Finds which forged source IPs actually cross the network between two nodes. Linux
only; the sender needs root.

It is a **two-node test, and the receiver must be started first**:

**Receiver** — listens and tallies which forged sources arrive.

| Prompt | Default | Notes |
|---|---|---|
| Shared token | `backpack` | must match the sender; unrelated to the tunnel token |
| Listen UDP port | `45000` | open it in this machine's firewall |
| Probes the sender emits per IP | `5` | must match the sender |
| Capture window (seconds) | `30` | long enough for the sender to finish |
| Report IPs with loss% at or below | `20` | the pass threshold |
| Write passing IPs to file | — | optional; one address per line |

**Sender** — emits probes from a list, range or CIDR of forged sources.

| Prompt | Notes |
|---|---|
| Shared token | the same string |
| Receiver node's REAL IPv4 | not a forged one |
| Receiver's listen UDP port | `45000` |
| Probes per candidate IP | `5` |
| Candidate IPs | `1.0.0.0/24`, `8.8.4.4`, `9.9.9.0-9.9.9.255`, comma-separated, or `@file` with one spec per line |
| Send interface | optional |

The receiver prints an `SPOOF_IP / ARRIVED / LOSS%` table and the count that
passed. Feed the passing addresses to the **sender's** end of the tunnel as its
forged source(s).

**Swap the roles and run it again** to map the other direction — the two are
independent.

**Everything `0/5` means your provider drops forged packets.** No setting changes
that; the transport is not available from that machine.

---

## Troubleshooting

| Symptom | Cause |
|---|---|
| Tunnel connects, carries nothing | the forged source is being dropped — run the tester |
| Nothing at all, even unforged | profile mismatch, or `spoof_peer_ip` unset on the server |
| The Edit screen refuses to save anything | `spoof_peer_ip` is empty on a server — set it first |
| Throughput far below the link | raise `spoof_sockbuf`; check `spoof_padding` is not on for no reason |
| Large transfers fail, small ones fine | lower `spoof_mtu` to the real path MTU |
| Worked, then stopped | if a pool is in use, one of its addresses may now be dropped — retest |

---

<div dir="rtl">

## خلاصهٔ فارسی

ترنسپورت **spoof** پکت‌های IP را خودش می‌سازد و آدرس مبدأ روی هدر را با آدرسی که
تو انتخاب می‌کنی عوض می‌کند. مسیریابی همچنان با آدرس **واقعی** طرف مقابل انجام
می‌شود، پس پکت واقعاً می‌رسد؛ فقط هدر چیز دیگری می‌گوید. زیر لایهٔ پکت همان KCP
است. لینوکس + root روی هر دو طرف.

**آنچه حتماً باید در دو طرف یکی باشد:** پروفایل پکت (و پروفایل هر جهت)، حالت
ICMP reply، padding و fake TLS. **آنچه محلی است و لازم نیست یکی باشد:** TTL
jitter، DSCP، shuffle پورت، رابط شبکه، بافر سوکت و MTU.

**روی سرور ایران وارد کردن «آی‌پی واقعی سرور خارج» (`spoof_peer_ip`) اجباری
است** — چون کلاینت مبدأش را جعل می‌کند و سرور از روی خود پکت نمی‌فهمد جواب را
کجا بفرستد. تا وقتی خالی باشد هیچ تنظیمی ذخیره نمی‌شود.

**آدرس مبدأ جعلی:** خالی یعنی هیچ جعلی انجام نمی‌شود و تونل با آدرس واقعی کار
می‌کند (جواب درست برای اولین اجرا). چند آدرس با کاما یعنی هر بار اتصال یکی
انتخاب می‌شود — همان چیزی که از محدودیت‌های مبتنی بر آدرس عبور می‌کند. فقط
آدرسی را بگذار که **تستر** گفته می‌رسد.

**Stealth:** ویزارد یک سؤال می‌پرسد — *Turn Stealth on* — و همان یک جواب کل
گروهِ ضدِ DPI را روشن می‌کند: padding، تغییر TTL و DSCP، جابه‌جایی پورت مبدأ، و
هدر جعلی TLS آنجا که پروفایل TCP باشد. یک سؤال است نه هفت‌تا، چون دوتای این‌ها
سیم را عوض می‌کنند و **باید در دو طرف یکی باشند**؛ اگر تک‌تک تنظیم شوند، دیر یا
زود یکی‌شان فقط روی یک سمت روشن می‌ماند و تونل وصل می‌شود ولی چیزی رد نمی‌کند.

**حالت relay حذف شد.** آن یک شکل از ترنسپورت spoofِ reverse بود و با خودش رفت.
چیزی از دست نرفت: تونل direct یک شبکه‌ی خصوصی بین دو ماشین است، پس وایرگارد
**روی آن route می‌شود** نه اینکه از داخلش pipe شود.

**بخش Fingerprint & evasion** همه‌اش پیش‌فرض خاموش است و هیچ‌کدام برای کارکردن
تونل لازم نیست. مهم‌ترین‌هایشان: `spoof_sockbuf` (اگر سرعت خیلی کمتر از لینک
است بالا ببر) و `spoof_mtu` (اگر انتقال‌های بزرگ خراب می‌شود پایین بیاور).

**تستر (`Manage → IP Spoofing Tester`)** دو طرفه است: اول Receiver را روی یک
سرور بالا بیاور، بعد Sender را روی سرور دیگر. جدول خروجی می‌گوید کدام آدرس‌ها
رسیده‌اند. اگر همه `0/5` بود، سرویس‌دهنده‌ات پکت جعلی را عبور نمی‌دهد و کاری
نمی‌شود کرد. بعد جای نقش‌ها را عوض کن تا جهت دیگر هم سنجیده شود.

</div>

---
[← Back to the docs index](README.md) · [Step-by-step tutorial →](../tutorial/ip-spoofing.md)

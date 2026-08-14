# IP Spoofing

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

Above the packet layer it is [KCP](performance-presets.md), the same reliable,
encrypted, error-correcting transport `kcp`, `xdi` and `pck` use. (The exception
is [pipe mode](#wireguard-pipe-mode), which has no KCP under it.)

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
| TTL jitter, DSCP, port shuffle, interface, socket buffer, MTU | no — local only |

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

### WireGuard pipe mode — `spoof_pipe`, `spoof_pipe_addr`

Switches the transport from a KCP tunnel to a **raw UDP pipe for WireGuard**:
instead of forwarding ports, it relays datagrams between a local WireGuard socket
and the forged-source channel, so a whole-device VPN rides over it.

- WireGuard supplies its own encryption and loss handling, so **no KCP sits
  underneath**.
- **The forwarded ports and the mux settings are ignored** in this mode.
- **Both ends must be in the same mode.**

`spoof_pipe_addr` (default `127.0.0.1:51820`) means different things per role:

| Role | Meaning |
|---|---|
| **Client** (kharej) | where the tunnel *listens* for WireGuard — point WireGuard's `endpoint` at exactly this address |
| **Server** (Iran) | where the real WireGuard listens on this machine — datagrams out of the tunnel are handed to it there |

---

## Fingerprint & evasion

One screen of questions rather than a menu, because these are set together while
working out what a path lets past, and none means much on its own.

**All are off by default and none is needed for a working tunnel.** Each costs
something — bandwidth, CPU, or a shape a different filter notices instead.
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

**حالت WireGuard pipe:** به‌جای پورت‌های forward، یک وایرگارد کامل را حمل
می‌کند؛ زیرش KCP نیست و پورت‌های forward نادیده گرفته می‌شوند. دو طرف باید در
یک حالت باشند.

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

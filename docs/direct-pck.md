# Direct PCK

```
Setup Iran → Direct → PCK
Setup Kharej → Direct → PCK
```

A **port-forwarding** tunnel between your two servers that looks, on the wire,
like an ordinary TCP flow — but is not one. Linux only. Needs root (or
`CAP_NET_RAW`) and `iptables`. **Both servers must run v2.0.0 or later.**

> This page is the **direct** `pck` carrier (`[l3]` with `carrier = "pck"`). The
> reverse **TCP + PCK** transport — `Setup → TCP → TCP + PCK`, `[server]` /
> `[client]` — is a different, unchanged engine with its own page:
> [TCP + PCK](tcp-pck.md).

## What it is

Since v2.0.0, direct PCK runs on **[paqet](https://github.com/hanselime/paqet)**
— the packet-level tunnel by *hanselime*, MIT licence. Backpack ported it as the
engine and kept its design:

```
user TCP / UDP ─▶ forwarder ─▶ smux stream ─▶ KCP session ─▶ cipher
               ─▶ crafted TCP segment ─▶ libpcap ─▶ the wire
```

- The segments are **built and captured with libpcap**. There is no kernel
  socket on the tunnel's port and no connection for a firewall to track, reset
  or throttle; what a box in the path sees is a long TCP flow.
- **KCP** supplies the reliability the missing TCP stack would have: it
  retransmits what is lost, which is why this carrier holds up on a lossy path
  where the old one stalled.
- **smux** carries many forwarded connections over one KCP session, and the
  **cipher** — keyed from the token — keeps the payload unreadable.

paqet's licence and copyright notice are in the repository's `NOTICE` file.
What Backpack changed — a hello that names a version mismatch, a status byte so
a refused target is reported instead of reset, length-framed UDP, half-close that
survives, and everything under *Nothing to configure* below — is listed at the
top of `internal/tunnel/paqet/doc.go`.

## What it is not

- **Not a routed tunnel.** There is **no TUN interface and no tunnel
  addresses**. It forwards the ports you list, TCP and optionally UDP, and
  nothing else. For ICMP, routing or "two servers on one private network" use
  [xDi, SNI Spoofing, IP Spoofing or GRE](l3-direct-tunnel.md) — those still
  run a TUN.
- **Not forged.** The source address is this machine's real one.
- **Not IPv6.** The segments are IPv4. Give the kharej's IPv4 address or a
  domain with an A record.

## Who dials, and what a port means

| | **Iran** (`mode = "dial"`) | **Kharej** (`mode = "listen"`) |
|---|---|---|
| Role | paqet's *client* | paqet's *server* |
| Lists forwarded ports | Yes — it listens on them (TCP, and UDP with `accept_udp`) | No — each connection names its own target |
| Dials | the kharej's tunnel port | the target of every forwarded port, on **its own** `127.0.0.1` |

`443=8443` therefore means: users reach **this server's** 443, and the kharej
dials **its** `127.0.0.1:8443`. A bare `443` goes to the kharej's 443. A service
bound to loopback on the kharej works; so does one bound to `0.0.0.0`.

**The kharej only connects to itself.** A target must be one of the kharej
server's own addresses — `127.0.0.1`, `::1`, or an address of one of its
interfaces. A target on any other machine (`443=10.0.0.5:443`, a cloud metadata
address, the internet) is refused, and the Iran log says so by name: the
token-holder of a PCK tunnel gets the kharej's services, which is what the
previous PCK's TUN gave it, and not a proxy to everywhere the kharej can reach.
To allow other machines, put this in the **kharej** server's file and restart it:

```toml
pck_remote_targets = true
```

The kharej also refuses more than 65 536 forwarded connections open at once
(answered "busy", not dialled).

## Setting one up

Set up **Iran first**. It makes the key and hands it to the kharej in one line.

1. **Iran:** `sudo backpack` → **Setup Iran** → **Direct** → **PCK**.

   | Question | |
   |---|---|
   | **Kharej IP Or Domain** | IPv4 or a domain |
   | **Tunnel Port** | default 9000. Nothing may listen on it on the kharej |
   | **Forwarded Ports** | `443`, `2053=8443`, `10000-10009` |
   | **Carry UDP As Well As TCP** | default no |
   | **Tunnel Name** | Enter takes the suggestion |
   | **How Should The Tunnel Be Tuned?** | Turbo / Balance / Aggressive, each shown with what it sets |
   | **Fine-Tune The Advanced Settings** | default no — see below |
   | **Restart This Tunnel On Both Servers Every N Hours** | default 0 (off) |

   There is **no token question**: the token is generated here and **is the
   encryption key**. There is no interface, address, MTU or error-correction
   question either — none of them exists for this carrier.

2. The summary shows the **Setup Link**. Confirm with **Create This Tunnel**.
3. **Kharej:** `sudo backpack` → **Setup Kharej** → **Direct** → **PCK** →
   **Setup Link**, and paste it. It asks only for a name. (Or **Manual**: tunnel
   port, name, the Iran server's token, preset.)

The web panel's **Add tunnel → Direct → PCK** asks the same, with the preset
cards showing what each one sets, and a **PCK fine-tune** drawer.

## Presets

The same three names every carrier has, mapped to the engine's parameters:

| | Balance | Turbo (default) | Aggressive |
|---|---|---|---|
| KCP sessions | 1 | 2 | 4 |
| Speed mode | `fast` | `fast2` | `fast3` |
| Window ceiling (packets) | 256 / 512 | 1024 / 1024 | 4096 / 4096 |
| Buffers / capture ring | 2 MiB / 4 MiB | 4 MiB / 8 MiB | 16 MiB / 32 MiB |
| Suits | a small VPS, a quiet tunnel | most links | a long, lossy route, RAM to spare |

The window is a **ceiling**. The engine moves each session's window below it by
what it reads of the path (see *On a busy or lossy link*), so a window larger
than the path needs costs only the memory the receive side may hold. A window
too small is still the speed limit: throughput is at most *window × packet size
÷ round-trip time*. 1024 packets of 1350 bytes at 150 ms is about 74 Mbit/s per
session; Aggressive's 4096 is about 295. Memory is the same product, which is
what the presets trade. The modes differ in the KCP tick (30, 20 and 10 ms),
which is how long an acknowledgement or a small write may wait to be batched,
and in how soon a lost packet is sent again.

Balance's 256 packets are about 37 Mbit/s at a 75 ms round trip, for
everything the tunnel carries: beside a few hundred short connections a second
it leaves bulk transfers a few Mbit/s, and at 10% loss it carries about 1.1
Mbit/s each way, less than GRE's 1.5. It is for a small VPS and a quiet
tunnel. Aggressive is for a long, lossy route and is **slower than Turbo on a
clean path** (the window control holds it to the same few hundred packets a
session, so four sessions add overhead). On the test path below it carried
40 / 41 Mbit/s with no loss and 12 / 19 at 10% loss (22 / 21 and 10 / 10 before
the changes in that section): four sessions lose less to a lost packet than
two.

### Fine-Tune

Optional, and every value empty means "from the preset". They are the `pck_*`
keys:

| Key | |
|---|---|
| `pck_conns` | KCP sessions, 1–16. Each is its own TCP flow (its own source port). 4 on Turbo is worth it on a lossy route, not on a clean one (see *On a busy or lossy link*) |
| `pck_kcp_mode` | `normal`, `fast`, `fast2`, `fast3` — least CPU to lowest latency |
| `pck_kcp_sndwnd`, `pck_kcp_rcvwnd` | the windows, in packets |
| `pck_kcp_mtu` | packet size, 576–1450 (default 1350). Not fragmented, so a size the path cannot carry is *lost*, not slow. **Keep it the same on both ends** |
| `pck_kcp_block` | the cipher: `aes` (default), `aes-128-gcm`, `salsa20`, … or `none`. **Must be the same on both ends** |

Changing the preset in **Edit** sets sessions, mode and windows back to the new
preset's. The packet size and the cipher are not preset values and stay.

## On a busy or lossy link

paqet runs KCP with no congestion control and a fixed window, and the
presets inherited that. On a path with a bottleneck queue, which is every real
one, a window larger than the path's bandwidth-delay product plus the queue
sends more than the queue holds. It was measured on a test path of 100 Mbit/s
each way, 30 ms ± 10 ms of delay and a 1000-packet queue, carrying a mix of 8
bulk transfers, about 200 short connections a second, idle connections, UDP
flows and a latency probe, with **no random loss at all**: Turbo carried 14
Mbit/s of the 100, the queue tail-dropped a quarter of what it was sent, KCP
retransmitted 30% of its segments, the probe took 661 ms (130 ms without the
tunnel), a transfer could stall for 15 seconds, and forwarded UDP got 7.8% of
its datagrams through, a median of 11.7 seconds late. The
engine answers that with:

- **The window is controlled.** One controller per tunnel moves the sessions'
  windows together, between a small floor and the preset's window, from two
  readings, both of **its own direction**: the queue the data it sends meets
  (the round-trip time, smoothed, against the lowest it has seen in 20 seconds,
  less the queue of the way back, which the receiver reads from the timestamp
  KCP segments already carry) and the segments the local transmit queue
  refused, counted only once they are 1% of the segments sent. It is not loss
  based, so random loss does not shrink the window — on a lossy path the window
  has to be several times the path's bandwidth-delay product to keep the pipe
  full, and the controller gets there because the queue does not form until it
  has. Every 15 seconds it halves the window for 0.3 s to empty the queue and
  read the path's real delay. Reading the round trip instead made the less
  loaded direction cut its window for the other's queue: bulk ran at about 45
  Mbit/s one way and 6 the other for a minute at a time, then swapped.
- **The output is paced, and the retransmit timer has a margin.** The sessions'
  output is held to 1.5 × window × packet size ÷ round trip, so a window is not
  sent as one burst into a bottleneck queue that tail-drops it. KCP's
  retransmit timer waits 2 × slack (the larger of 25 ms and a third of the
  base round trip) plus the reverse queue beyond its own figure: before, with
  no loss at all, 8% of segments were sent by the timer and 7.5% arrived
  twice; now it is 0.1% or less. A fast retransmit that is itself lost is
  fast-retransmitted again, and does not wait for the timer. These need the
  copy of kcp-go in `internal/thirdparty/kcp-go` (upstream v5.6.72, MIT, noted
  in `NOTICE`); the legacy `kcp` transport links it too and is not affected.
  The wire is unchanged.
- **KCP batches acknowledgements and small writes in every mode.** paqet's
  fast2 and fast3 answer every packet with one, and the packet is what a
  queue counts: with batching the same load sent half the segments, and bulk
  transfers beside the short connections carried 20 and 34 Mbit/s where they
  had carried 7 and 9.
- **A forwarded UDP flow's datagrams travel beside KCP.** A datagram that fits
  one segment (up to 1318 bytes) is sealed with a key of its own and sent as a
  segment of the same flow, not as a frame in the reliable stream: it is not
  retransmitted, not queued behind other streams, and lost if the path drops
  it, as UDP is. The stream stays as the flow's control channel, and carries
  larger datagrams and the first ones, sent before the other end has answered.
  At 5% loss, 90% of datagrams arrive in about 90 ms.
- **Window updates no longer block the reader.** smux sent a stream's flow
  control update from inside the `Read` that triggered it, which returned only
  once the frame had been through the session's write queue — hundreds of
  frames long on a busy session, every time and for every stream. The reader
  that waited was not reading, the data kept arriving into the session's
  receive buffer, and when that was empty the session stopped reading
  everything, window updates included. Every upload stopped, for up to a
  minute. smux is a modified copy in `internal/thirdparty/smux`: updates go out
  from a goroutine of their own and control frames go ahead of data in the
  write queue. The wire is unchanged.

Turbo on that path, before and after the first round of changes (bulk Mbit/s down/up; probe latency
median and 99th percentile; longest stall of a bulk transfer; UDP delivered
and its round trip). GRE, the kernel's, and a plain veth are for comparison:

| Path | | Bulk | Latency | Stall | UDP |
|---|---|---|---|---|---|
| no loss | plain veth | 44 / 43 | 130 / 181 ms | 1 s | 98%, 130 ms |
| | GRE | 44 / 41 | 147 / 475 ms | 1 s | 98%, 147 ms |
| | PCK before | 14 / 15 | 661 / 1419 ms | 15 s | 7.8%, 11.7 s |
| | **PCK now** | 40-44 / 36 | 163 / 310 ms | 1 s | 99.6%, 104 ms |
| 1% | GRE | 9.0 / 9.3 | 80 / 365 ms | 1 s | 98%, 80 ms |
| | PCK before | 12 / 9 | 717 / 1335 ms | 21 s | 12.3%, 8.0 s |
| | PCK, first controller | 10 / 12 | 528 / 1007 ms | 2 s | 97.8%, 101 ms |
| 3% | GRE | 4.5 / 4.4 | 79 / 380 ms | 1 s | 94.0%, 79 ms |
| | PCK before | 12 / 10 | 828 / 1514 ms | 16 s | 5.6%, 14.1 s |
| | PCK, first controller | 8.4 / 6.6 | 671 / 1275 ms | 2 s | 93.9%, 95 ms |
| 5% | GRE | 3.0 / 3.0 | 79 / 403 ms | 1 s | 90.2%, 78 ms |
| | PCK before | 11 / 9 | 992 / 1645 ms | 19 s | 6.7%, 13.3 s |
| | PCK, first controller | 7.7 / 5.8 | 890 / 1482 ms | 2 s | 90.1%, 94 ms |
| 10% | GRE | 1.5 / 1.6 | 79 / 777 ms | 9 s | 80.8%, 78 ms |
| | PCK before | 7.7 / 8.5 | 1058 / 1721 ms | 51 s | 6.5%, 16.1 s |
| | PCK, first controller | 7.2 / 8.0 | 1041 / 1695 ms | 3 s | 81.1%, 92 ms |
| bursty 10% | GRE | 1.7 / 1.9 | 79 / 800 ms | 21 s | 81.3%, 78 ms |
| | PCK before | 8.9 / 8.9 | 884 / 1479 ms | 29 s | 6.1%, 16.1 s |
| | PCK, first controller | 4.7 / 7.4 | 1037 / 1671 ms | 2 s | 80.8%, 92 ms |

The loss rows are from the first controller, which read the round trip; the
current one reads its own direction only, is paced, and sets the retransmit
margin (above). Single loss runs vary by about ±50%, and down and up trade
places inside a run, so the bulk figure to read is the sum of both directions,
in Mbit/s, over 300 s:

| Loss | Original | Now |
|---|---|---|
| 1% | about 19 | about 30 |
| 3% | 15 | 26 |
| 5% | 13 | 23 |
| 10% | 15 | 18 |

At 1-10% loss Turbo's bulk is 1.2-2.4 times the original, more at 5% and up,
and above GRE at every loss rate. Stalls are 3 s or less, and recovery from a
path that flaps is no worse. With no loss the latency is 163 ms median and
310 ms at the 99th percentile (243 and 598 before). On the 25 ms, 0.5% loss
test path below it again matches the release before the controller (see
*Speed*).

On a clean veth pair with no rate limit and no delay, where there is no queue
to read, the numbers did not move: Turbo carries 658 / 896 / 902 Mbit/s down and
643 / 1053 / 1311 up with 1 / 4 / 16 streams (642 / 862 / 854 and 649 / 1049 /
1308 before). On a path whose round trip is shorter than the KCP tick the
engine turns acknowledgement batching off: batching held the round trip to the
tick, and one stream's speed is its window over the round trip (one Turbo
stream fell from 640 to 390 Mbit/s with it on).

What is still true:

- **Beside a few hundred short connections a second, bulk transfers get what is
  left.** The mix above offers 39 Mbit/s of short connections in each direction,
  and they all complete (100% in every run), so the bulk figures are what the
  rest of the link gives: with the bulk transfers alone Turbo carried 47 down and
  70 up on the same path, and GRE 44 / 41 under the mix. Turbo does not fill
  the 100 Mbit/s: it keeps a standing queue of about a quarter of the round trip
  at most, and a link whose queue is counted in packets (as this one's is)
  drops what its senders burst into it before that.
- **On a lossy path a transfer is slower than it could be and a small request
  is slow.** KCP delivers in order inside a session, so a lost packet holds up
  everything behind it for a round trip or more, and with 1-10% loss that is
  always some packet. The probe's 0.5-1 s at 1-10% loss is that, and the
  reason GRE's is 80 ms: its flows do not wait for each other. PCK still moves
  more bulk than GRE at every loss rate measured, and keeps all of its
  connections. More sessions help here, and are an opt-in (`pck_conns = 4`
  on Turbo), measured against Turbo's 2:

  | | `pck_conns` 2 | `pck_conns` 4 |
  |---|---|---|
  | 10% loss, Mbit/s down / up | about 9 / 8 | about 13 / 15 |
  | 10% loss, probe median | about 900 ms | about 550 ms |
  | 1% loss, Mbit/s down / up | about 18 / 13 | about 24 / 15 |

  With no loss it gains nothing and sends a little less up (34.5 against 36
  Mbit/s), and on a path that flaps recovery is occasionally slower. So use it
  on a lossy route, not on a clean one.
- **A bottleneck that drops without queueing** — a policer — shows in neither
  signal the controller reads (the delay of its own direction, the local queue's
  refusals), and there the window ceiling is the only limit, as before. A
  signal from the share of segments lost was tried and removed: it fired at 10%
  random loss and cost ten times the goodput.
- **A datagram larger than 1318 bytes goes by the stream**: reliable, in order,
  and as late as the stream is. QUIC and WireGuard keep theirs under that;
  a flow that does not should be given a smaller packet size.

## Nothing to configure

paqet makes its operator find these by hand. Backpack finds them at start:

- **The interface and this server's address** — from the route to the peer.
- **The gateway's MAC** — from the neighbour table.
- **The firewall guard** — three `iptables` rules that drop the RSTs the kernel
  would answer the tunnel's segments with (it has no socket on that port), and
  keep the flow out of connection tracking. They are put in when the tunnel
  starts and taken out when it stops.

`pck_interface`, `pck_gateway_mac` and `pck_flags` still exist, for the host
where the guess is wrong.

**Manage → Health** checks what a PCK tunnel actually needs — libpcap, the
firewall guard, the gateway — and not an interface it does not have.

## Both servers must update

The wire is different from the previous PCK, and the two do not talk to each
other. Update **both** servers. The end that is new logs, as an error, that the
other server "is still running the previous PCK"; the end that cannot see the
peer at all logs "no answer from" it, with a reminder to check the other
server's version.

## Migrating from the previous PCK

A tunnel from v1.9.0 or earlier **keeps running as it is** after the update. Its
file is read, never rewritten:

- `mode`, `addr`, `token`, `ports`, `accept_udp`, `max_connections`,
  `bandwidth_mbps`, `preset`, `sockbuf` and the `pck_interface`,
  `pck_gateway_mac`, `pck_flags` overrides are read.
- `iface`, `local_ip`, `peer_ip`, `mtu`, `txqueuelen`, `qdisc`, `auto_mtu`,
  `mss_clamp`, `fec_*`, `encap` and `gre_key` are **ignored**. The log says so
  once, and the panel shows one line.
- The preset's engine parameters come from its name (`balance`, `turbo`,
  `aggressive`); `pck_*` keys override them.
- **Saving the tunnel** (Edit, in the CLI or the panel) writes a new-format
  file and drops the dead keys.

What changes with the engine:

- **A tunnel with no ports** carried routed traffic over the TUN, which no longer
  exists for PCK. It is told so in the log. Use xDi, SNI Spoofing, IP Spoofing
  or GRE for that.
- **A target** `10.10.0.2:80` — the peer's address on the old TUN — is now dialled
  on the kharej's `127.0.0.1`, which reaches the same service. A target naming
  any other host used to need forwarding and NAT set up on the kharej; now it
  needs `pck_remote_targets = true` in the kharej's file, and is then dialled by
  the kharej directly.
- **Several kharej servers sharing a port** (`443=a:443|b:443`) is a TUN feature.
  A PCK tunnel dials one kharej; give each its own Iran port.
- **A PCK tunnel cannot be edited into xDi, SNI Spoofing, IP Spoofing or GRE, or
  they into PCK.** The TUN carriers agree on a pair of tunnel addresses between
  the two servers, which an edit cannot make the other server agree on, and PCK
  has no TUN to carry routed traffic at all. Add the new tunnel on both servers,
  then delete the old one — as for GRE.

## Speed

On a path with loss and delay PCK is much faster than it was: on a test link
with 25 ms delay and 0.5% loss each way and no rate limit, Turbo downloads at
89 / 177 / 172 Mbit/s with 1 / 4 / 16 streams, where the previous PCK managed
4 / 16 / 56. (The release before the window control gave 99 / 176 / 170 there,
so the control no longer costs speed on a path with nothing to fill. An
earlier version of it did, 5-30%: it cut the window on three refused segments
in 25 ms and held it for 1.5 s, and a burst that overflows the local queue for
an instant refuses that many. Refusals now count once they are 1% of the
segments sent.) On a
clean, short link (a veth pair, no delay) Turbo carries about 650-1300 Mbit/s
up and 650-900 down at 1 / 4 / 16 streams, which is the previous PCK's upload
and more than its download; Balance 520-540 each way, Aggressive 570-1910.
Each KCP session sends through a libpcap handle of its own (up to four),
because a single handle — one system call per packet, one at a time — was where
the engine spent its time. If your route is clean and you only need speed, a
TCP transport is still the better choice; PCK is for the route where TCP is the
thing being interfered with. What it does when the path is full or lossy is
under *On a busy or lossy link*.

## Requirements and limits

- Linux, root or `CAP_NET_RAW`, `iptables`.
- Release builds link **libpcap statically** (they are built with cgo); a binary
  without it refuses to start a PCK tunnel and says so. Use the release.
- IPv4 only.
- The tunnel port must be **free** on the kharej: the segments are addressed to
  it and no socket answers.
- It is a long-lived flow to one port. A path that cuts any flow after a few
  packets cuts this too.

---

[← Back to the docs index](README.md)

---

<div dir="rtl">

## خلاصهٔ فارسی

**PCK مستقیم** حالا روی **[paqet](https://github.com/hanselime/paqet)** (از
*hanselime*، لایسنس MIT) اجرا می‌شود: یک تونل **فوروارد پورت** که روی سیم شبیه یک
جریان TCP معمولی است. بسته‌ها با libpcap ساخته و گرفته می‌شوند، KCP قابلیت اطمینان
را می‌دهد و smux چند اتصال را روی یک نشست می‌برد.

- **TUN ندارد.** آدرس تونل، رابط، MTU و FEC در کار نیست. فقط پورت‌هایی را که
  می‌نویسی (TCP و در صورت نیاز UDP) رد می‌کند. برای مسیریابی از xDi، SNI Spoofing،
  IP Spoofing یا GRE استفاده کن.
- **ایران** (`dial`) روی پورت‌ها گوش می‌دهد و **خارج** (`listen`) به
  `127.0.0.1:پورت` خودش وصل می‌شود. `443=8443` یعنی: کاربر به پورت ۴۴۳ همین سرور
  می‌آید و خارج به `127.0.0.1:8443` خودش وصل می‌شود.
- **خارج فقط به خودش وصل می‌شود:** مقصد باید یکی از آدرس‌های خودِ سرور خارج باشد
  (`127.0.0.1`، `::1` یا آدرس یکی از اینترفیس‌هایش). مقصد روی ماشین دیگر رد
  می‌شود و لاگ ایران دلیلش را می‌گوید. برای اجازه دادن، در فایل **سرور خارج**
  `pck_remote_targets = true` بگذار و ری‌استارت کن.
- **راه‌اندازی:** اول ایران: `Setup Iran → Direct → PCK`. سؤال‌ها: آدرس خارج،
  پورت تونل، پورت‌های فوروارد، UDP، نام، پریست. **توکن را نمی‌پرسد** — خودش
  ساخته می‌شود و همان کلید رمز است. در خارج: `Setup Kharej → Direct → PCK → Setup
  Link` و لینک را بچسبان.
- **پریست‌ها** همان سه تا: Balance (۱ نشست، `fast`، پنجره ۲۵۶/۵۱۲)، Turbo
  (پیش‌فرض: ۲ نشست، `fast2`، پنجره ۱۰۲۴)، Aggressive (۴ نشست، `fast3`، پنجره
  ۴۰۹۶؛ برای مسیر طولانی و پرتلفات، و روی مسیر تمیز از Turbo کندتر). Fine-Tune اختیاری است: کلیدهای `pck_*`. `pck_conns = 4` روی Turbo در مسیر پرتلفات
  می‌ارزد (در ۱۰٪ تلفات حدود ۱۳/۱۵ به‌جای ۹/۸ مگابیت بر ثانیه و تأخیر میانه حدود ۵۵۰
  به‌جای ۹۰۰ میلی‌ثانیه)، در مسیر تمیز نه. پنجره حالا **سقف** است، نه
  نقطهٔ کار.
- **روی لینک شلوغ یا پرتلفات:** یک کنترلر پنجرهٔ نشست‌ها را با خواندن تأخیر
  و سرریز صفِ ارسال محلی (نه تلفات) بین یک کف و سقفِ پریست جابه‌جا می‌کند، پس تلفات
  تصادفی پنجره را کوچک نمی‌کند. فقط صفِ **جهت خودش** را می‌خواند (تأخیر رفت‌وبرگشت
  منهای صفِ مسیر برگشت که گیرنده از زمان‌نگار داخل بسته‌های KCP می‌خواند) و رد
  شدن از صفِ محلی را فقط وقتی حساب می‌کند که به ۱٪ بسته‌های فرستاده‌شده برسد. خروجی
  با سرعت ۱٫۵ × پنجره × اندازهٔ بسته ÷ RTT پیش می‌رود، تایمر ارسال مجدد حاشیه دارد و
  ارسال مجدد سریعِ گم‌شده دوباره سریع ارسال می‌شود (کپی fork شدهٔ kcp-go در
  `internal/thirdparty/kcp-go`؛ پروتکل روی سیم تغییری نکرده). KCP در همهٔ حالت‌ها ack و نوشتن‌های کوچک را دسته‌ای
  می‌فرستد. دیتاگرام‌های UDP که در یک بسته جا شوند (تا ۱۳۱۸ بایت) از کنار KCP
  می‌روند: بدون ارسال مجدد و بدون صف، و اگر مسیر بیندازدشان گم می‌شوند. smux یک
  کپی اصلاح‌شده است تا خواننده منتظر به‌روزرسانی پنجره نماند. جزئیات و اعداد: بخش
  «On a busy or lossy link».
- **همه‌چیز خودکار پیدا می‌شود:** رابط، آدرس سرور، MAC گیت‌وی و سه قانون iptables
  که جلوی RST کرنل را می‌گیرد.
- **هر دو سرور باید به‌روز شوند.** پروتکل با PCK قبلی سازگار نیست.
- **مهاجرت:** تونل قدیمی همان‌طور که هست کار می‌کند و بازنویسی نمی‌شود؛ کلیدهای
  `iface`، `local_ip`، `peer_ip`، `mtu`، `fec_*` نادیده گرفته می‌شوند. با اولین
  ذخیره (Edit) فایل به قالب جدید درمی‌آید. تونلی که پورت نداشت (ترافیک مسیریابی‌شده)
  دیگر با PCK ممکن نیست.
- **تبدیل حامل:** PCK با ویرایش به xDi/SNI/Spoof/GRE (و برعکس) تبدیل نمی‌شود؛
  تونل جدید را روی هر دو سرور بساز و قدیمی را پاک کن.

</div>

---

*Last verified against Backpack v2.0.0.*

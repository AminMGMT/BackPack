# Direct layer-3 tunnel

Every other transport in Backpack forwards **ports**: a listener on the Iran
server, a backend dial on the kharej server, and a stream in between. This one
is different. It creates a network **interface** on each host and carries whole
IP packets between them, so the two servers get an ordinary point-to-point
link — `10.10.0.1` talking to `10.10.0.2` — over which anything at all can be
routed.

It is the GRE/IPIP idea, built into Backpack's own core rather than borrowed
from the kernel, and carried inside Backpack's own transports.

### Always GRE + Noise

Every direct tunnel is wrapped the same way: **GRE + Noise**. There is no
choice to make and the wizard does not ask — but the name is worth eight
characters of explanation, because "GRE" on its own means something else.

A kernel GRE tunnel — `ip tunnel add gre1 mode gre`, what most guides mean by
"GRE" — puts its packets on the wire as bare **IP protocol 47**. It is
unencrypted, it is visible for exactly what it is, and it is removed by a single
firewall rule. Kernel IPIP is protocol 4 and no better off.

Backpack writes the same GRE header — RFC 2784, with the RFC 2890 key — but the
header is not what travels. It is sealed inside an encrypted session and handed
to a carrier, so what a capture sees is the carrier: an ordinary TCP flow, a UDP
stream, ICMP echo, or forged packets. There is no protocol 47 to block.

Two consequences follow, and the second is the cost:

- Nothing about the tunnel is visible or unencrypted, and no single rule stops
  it.
- **It does not interoperate.** A Cisco, a MikroTik or a plain Linux GRE
  endpoint cannot talk to it. Backpack talks to Backpack.

There is one encapsulation and it is **GRE**. A config that still says
`encap = "ipip"` is read as GRE, so a tunnel built before the choice was removed
keeps loading — but **both ends must be on the same version**, and the handshake
refuses a mismatch by name if they are not.

Two encapsulations were a way for the two ends to disagree, and the
disagreement cost far more than the choice was worth: ipip saved four bytes,
and a pair that disagreed came up, reported a peer, logged nothing above debug,
and carried nothing at all.

The config value stays `encap = "gre"` — both ends compare it, so it cannot
change. Only what the screens call it changed.

> **Linux only.** It needs `/dev/net/tun` and `CAP_NET_ADMIN` (in practice,
> root). Every other platform reports that plainly and refuses to start.

---

## When you want it

Use the layer-3 tunnel when a port forwarder is the wrong shape:

- You need to carry **protocols that have no ports** — ICMP, OSPF, ESP.
- You want the two servers on **one private network**, reachable by address
  rather than by a mapping written in advance.
- You want to run **routing** across the link.
- You are carrying something that already brings its own reliability and
  encryption, and you only need packets moved.

Use a reverse or direct **port** tunnel for the ordinary case: exposing a few
services on the Iran server. It is simpler, it needs no privileges beyond the
ports themselves, and it is the path with years of production behind it.

## Direction is free

Once the tunnel is up it is symmetric. The only asymmetry is who reaches out
first, and you choose that per deployment:

| | `mode = "dial"` | `mode = "listen"` |
|---|---|---|
| What it does | Reaches out to the peer | Waits to be dialled |
| Needs an open inbound port | No | Yes |

Put `listen` on whichever host can accept an inbound connection, and `dial` on
the other. For the Iran ⇄ kharej case that is normally `dial` on Iran and
`listen` on kharej, which is the **direct** direction — Iran needs no inbound
port of its own.

---

## Setting one up

**From the menu — the easy way.** Run `sudo backpack`, choose **Setup Iran** or
**Setup Kharej**, then **Direct**, then **Full IP tunnel**. It asks which machine you are on and how the packets
should travel, suggests private addresses for both ends, and writes the config
itself.

The rest of this page is what it writes.

Two files, one on each host. They must agree on the token, the encapsulation
and the carrier.

**Iran** (`/etc/backpack/l3.toml`):

```toml
[l3]
mode     = "dial"
addr     = "KHAREJ_IP:9000"
token    = "USE_A_LONG_RANDOM_TOKEN"
local_ip = "10.10.0.1/30"
peer_ip  = "10.10.0.2"
```

**Kharej** (`/etc/backpack/l3.toml`):

```toml
[l3]
mode     = "listen"
addr     = "0.0.0.0:9000"
token    = "USE_A_LONG_RANDOM_TOKEN"
local_ip = "10.10.0.2/30"
peer_ip  = "10.10.0.1"
```

Start both. Each host brings up a `bp0` interface, and from Iran:

```
ping 10.10.0.2
```

From here the two servers are on a private network. Route what you like across
it, expose a service on the tunnel address, or forward ports over it — the link
is an ordinary interface and behaves like one.

---

## Forwarding ports over the tunnel

You can keep the familiar `ports = [...]` interface on top of the layer-3
tunnel. Add it to the Iran side:

```toml
[l3]
mode     = "dial"
addr     = "KHAREJ_IP:9000"
token    = "USE_A_LONG_RANDOM_TOKEN"
local_ip = "10.10.0.1/30"
peer_ip  = "10.10.0.2"

ports      = ["443", "2053-2060", "8080=80"]
accept_udp = true
```

Port 443 on Iran now reaches port 443 on kharej, across the tunnel. The syntax
is the reverse tunnel's, so a config moves across unchanged:

| Mapping | Effect |
|---|---|
| `443` | `:443` → `peer:443` |
| `443=8443` | `:443` → `peer:8443` |
| `443=10.0.0.5:8443` | `:443` → an explicit host |
| `127.0.0.1:443=8443` | bind to one local address only |
| `10000-10009` | a range, each to the same port |
| `10000-10009=20000-20009` | a range, preserving the offset |
| `443=10.0.0.1:80\|10.0.0.2:80` | two backends, load-balanced |

A target with no host of its own means `peer_ip`, which is what almost every
mapping wants. `accept_udp` adds UDP alongside TCP; it is off by default, for
the same reason it is on the reverse tunnel — a web tunnel should not silently
start carrying every QUIC flow on port 443.

**Ports are optional.** Leave them out and the tunnel simply carries whatever
the kernel routes into the interface, which is the plain layer-3 case.

Two things worth knowing:

- **The forwarder outlives tunnel restarts.** Listeners are opened once. If the
  engine rebuilds its session, connections already open are undisturbed, and
  while the tunnel is genuinely down new connections are refused rather than
  left hanging.
- **This is ordinary userspace forwarding.** For the highest possible
  throughput you can skip it and use kernel `iptables` DNAT over `bp0` instead
  — the interface is a normal one and nothing here prevents it.

---

## Options

| Key | Default | What it does |
|---|---|---|
| `mode` | *required* | `dial` or `listen`. Its absence is what tells Backpack there is no layer-3 tunnel here at all |
| `addr` | *required* | Peer `host:port` when dialling; bind address when listening |
| `token` | *required* | The shared secret. The only credential |
| `local_ip` | *required* | This end's tunnel address, normally with a prefix: `10.10.0.1/30` |
| `peer_ip` | | The other end's tunnel address. Required if `local_ip` has no prefix |
| `encap` | `gre` | `gre` — a file that says `ipip` is read as GRE |
| `gre_key` | `0` | RFC 2890 key, letting several logical tunnels share a carrier. `gre` only |
| `carrier` | `udp` | `udp`, `quic`, `pck`, `sni`, `xdi` or `spoof` — see below |
| `iface` | `bp0` | Interface name to create |
| `mtu` | `1400` | Interface MTU |
| `sockbuf` | 4 MiB | Carrier socket buffers |
| `fec_data` | `0` | Error correction: data packets per group. Both keys or neither — see [Error correction](#error-correction) |
| `fec_parity` | `0` | Error correction: spare packets per group |
| `paths` | `1` | Spread the `udp` carrier over this many sockets — see [Several sockets](#several-sockets) |
| `sni_domain` | built-in | The domain the `sni` carrier announces. `sni` only |
| `ports` | none | Forwarded port mappings — see above |
| `accept_udp` | `false` | Forward UDP as well as TCP on those ports |

### Carriers

Plain UDP is right on a path that does not interfere. On one that does, it is
the first thing to go — a long-lived UDP flow to a foreign address is among the
easiest patterns to rate-limit. The other three carry the same encrypted
packets somewhere less obvious:

| `carrier` | What is on the wire | Overhead | Needs |
|---|---|---|---|
| `udp` | UDP datagrams | 28 | nothing |
| `quic` | a real QUIC session, carrying the tunnel in RFC 9221 datagrams | 60 | nothing |
| `pck` | TCP segments built without a socket — no handshake, no connection state | 52 | root / `CAP_NET_RAW` |
| `sni` | `pck`, plus a TLS hello naming an allowed domain at the start of the flow | 52 | root / `CAP_NET_RAW` |
| `xdi` | ICMP echo, for a path that filters UDP and TCP but not ping | 33 | root / `CAP_NET_RAW` |
| `spoof` | raw IP with a forged source address | 28+ | root / `CAP_NET_RAW` |

The obfuscated ones are **Linux only**. `pck`, `xdi` and `spoof` are the same
carriers those transports already use — the layer-3 tunnel simply hands them its
own packets instead of KCP's, so a fix to a carrier reaches both at once.

`quic` is not imitating anything: it opens a real QUIC connection, with a real
TLS 1.3 handshake and `h3` as the ALPN, and puts the tunnel in QUIC's unreliable
DATAGRAM frames. To a path it is the HTTP/3 that dominates a modern network. It
is the only obfuscated choice that needs no root. The certificate it presents is
for the shape and not for the secrecy — the tunnel's own payload is sealed by
Noise before it reaches any carrier, and the peer is authenticated by the token.

`sni` is `pck` with one extra segment at the start of the flow: a TLS
ClientHello naming a domain the path is known to allow. A box that classifies by
server name reads it, decides the flow is permitted, and stops looking; the
tunnel's segments follow on the same five-tuple. Set `sni_domain` to a name your
own route already reaches — which names those are is a property of the route,
so it is yours to choose and to test. The far end drops the hello before the
tunnel sees it. The technique is [patterniha's][sni-orig], by way of
[therealaleph/sni-spoofing-rust][sni-rs].

[sni-orig]: https://github.com/patterniha/SNI-Spoofing
[sni-rs]: https://github.com/therealaleph/sni-spoofing-rust

### Error correction

A layer-3 tunnel may not ride on anything that retransmits — see [why](#limits)
— but it can carry **redundancy**. For every `fec_data` packets the tunnel
sends, it sends `fec_parity` spare ones, and any `fec_parity` of the group may
be lost without losing anything: the far end rebuilds them, with nothing waiting
for a timer.

It is for a path that drops packets **steadily** — a congested international
route, a lossy last mile. Measured against a link dropping 20%, an application
saw **3.5% loss with it on and 39% with it off**, for about a third more
traffic. On a clean route that third is pure waste, so it is off by default.

```toml
[l3]
carrier    = "spoof"
fec_data   = 10
fec_parity = 3
```

- **Both ends must set the same pair.** It is not negotiated; a receiver
  expecting a different scheme rebuilds nothing.
- It works over **every** carrier — `udp`, `quic`, `spoof`, `pck`, `sni`, `xdi` — because loss
  is a property of the path, not of the disguise.
- Half a scheme is refused at startup: set both keys, or neither.
- The recommended pair (10/3) is what the wizard's *Turn on error correction*
  and the panel's checkbox write. The exact numbers are a manual tuning; the
  Link Test can also size them to a measured loss.

### Several sockets

A tunnel on one UDP socket is one flow, and some providers give **each flow its
own speed limit** — so the tunnel sits at one flow's allowance however fast the
link really is. The usual sign is a tunnel that will not go faster no matter
what you tune. `paths` spreads the same traffic over several sockets, which is
several flows, which is several allowances.

```toml
[l3]
carrier = "udp"
paths   = 4
```

- Measured against a link capped at 8 Mbit/s per flow: one socket carried
  **5.8 Mbit/s, four carried 23.6**.
- The sockets use **consecutive ports** counting up from the tunnel port — `paths = 4`
  on port 9000 uses 9000–9003, and those must be open on the listening side.
- **Both ends must set the same number.**
- It is for the **`udp` carrier only**. The obfuscated carriers already vary
  their source per packet, so a shaper counting flows sees many either way;
  Backpack refuses `paths` on them rather than writing a setting that does
  nothing.
- Nothing is added to the wire — the MTU is unchanged.

Tuning uses the keys you already know. They sit at the top level of `[l3]`,
exactly as they do in `[server]` and `[client]`:

```toml
[l3]
mode          = "dial"
addr          = "KHAREJ_IP:9000"
token         = "USE_A_LONG_RANDOM_TOKEN"
carrier       = "pck"
local_ip      = "10.10.0.1/30"
peer_ip       = "10.10.0.2"
mtu           = 1380          # pck costs 52, so leave a little more room
pck_interface = "eth0"        # optional
```

> **`spoof` listeners need `spoof_peer_ip`.** The dialling peer forges the
> source of every packet it sends, so the listening side cannot learn where to
> reply from the packets themselves and must be told. Backpack refuses the
> config up front rather than coming up and replying nowhere.

### Encapsulation

**GRE**, always. Four bytes, or eight when a key is set — the key is what lets
several logical tunnels share one carrier between the same pair of addresses.

It carries IPv4 and IPv6 over the same tunnel; the first nibble of a packet
says which it is. There is no separate `sit`/`6in4` mode because none is
needed.

### MTU

The default of **1400** is deliberately low. A layer-3 tunnel whose packets are
slightly too large does not fail loudly — it passes small flows and stalls
large ones, which presents as "ping works but downloads hang" and costs an
afternoon. Many real Iranian routes have an effective MTU below 1500.

The budget is:

```
mtu = path − outer IP − carrier − session (29) − encap
```

On a clean 1500-byte path with `udp` that comes to **1439** (GRE's four bytes
included). The log
prints what it computes at startup and warns if your configured MTU exceeds it.

---

## Security

The handshake is **Noise NNpsk0** with the pre-shared key derived from your
token, giving an encrypted, mutually authenticated, forward-secret channel. On
top of that:

- Every packet carries an **explicit counter** and is checked against a
  2048-bit **sliding replay window**, so a recorded packet cannot be sent
  again.
- The header is **authenticated** as additional data, so nothing in it can be
  altered in flight.
- Sessions **rekey** every two minutes, with the old keys kept briefly so
  in-flight packets are not lost.
- A peer without the token gets **no reply at all** — a scanner finds a socket
  that never answers.
- The peer's address is only ever learned from a packet that has already
  authenticated, so nobody can redirect the tunnel by forging one datagram.

GRE and IPIP have no encryption of their own; all of the above is Backpack's,
and it is why the kernel's own tunnels are not used here.

> **Use a long, random token.** It is the only thing standing between your
> tunnel and anyone who can reach the port.

---

## Limits

- **Linux only**, and needs root or `CAP_NET_ADMIN`. The obfuscated carriers
  need `CAP_NET_RAW` as well.
- **QUIC datagrams are not wired up yet.** The four carriers above are what
  there is.
- **No reliable carrier, ever.** `tcp`, `ws` and `kcp` are refused by design:
  an IP packet already belongs to something that handles its own loss, and
  stacking two retransmit timers makes throughput collapse under loss rather
  than degrade. This is the classic TCP-over-TCP meltdown.
- **Two peers**, not many.

## It cannot disturb a reverse tunnel

The layer-3 tunnel lives in its own `[l3]` table and its own package. A
configuration file that does not mention `[l3]` cannot reach any of this code,
and one that does never reaches the reverse engine. They share no keys, no
sockets and no state.

If something here misbehaves, delete the `[l3]` file. Nothing else changes.

## TCP segment cap (MSS clamp)

This is on by default and you should almost always leave it alone. It is
described here because it is the fix for the one failure this tunnel produces
that looks like nothing at all.

### The fault

The tunnel's MTU is smaller than the 1500 bytes the interfaces at either end
carry, because the carrier, the session and the encapsulation each take their
cut. A TCP connection crossing the tunnel does not know that: its two endpoints
agree a segment size from *their* interfaces and then send full-sized segments
that cannot fit.

The kernel is supposed to learn this from an ICMP "fragmentation needed"
message. A great many networks drop those, and the routes this tunnel exists to
cross drop them with particular enthusiasm. So nothing learns, and what you see
is:

- `ping` works
- SSH works
- a small web page loads
- every download stalls at a few kilobytes and never recovers

Every liveness check passes, because every liveness check sends small packets.

### The fix

Backpack rewrites the MSS option in the SYN of each TCP connection leaving the
tunnel interface, so both ends agree on something that fits before any data is
sent. Nothing has to be discovered, so nothing depends on an ICMP message
arriving.

| `mss_clamp` | Meaning |
|---|---|
| `0` | automatic — the MTU minus 40 (IPv4) or 60 (IPv6). The default. |
| a number | that value exactly, for when a path measurement gave you one |
| `-1` | off, for a host whose firewall is managed elsewhere |

Set it in the wizard under *Fine-tune the advanced settings by hand*, or later
with **Manage → Manage Tunnels → Edit → TCP segment cap**.

### Checking it

```bash
iptables -t mangle -S | grep backpack-l3-mss
```

Two rules per address family: `FORWARD` for traffic routed *through* this host,
`OUTPUT` for connections that start on it. Both are needed — the forwarded
ports are the second kind.

The rules are removed when the tunnel stops, and any left behind by a process
that was killed are swept before new ones are added, so they cannot accumulate.

## Automatic MTU

On by default. The tunnel measures what the path really carries and sets the
interface to match, instead of trusting the number in the file.

### Why it exists

The MTU is the only setting here that cannot be worked out from the
configuration, and the one that fails worst when it is wrong. Set it too high
and the tunnel comes up, passes every health check, carries `ping` and SSH — and
stalls every download and every TLS handshake, because the packets that matter
are the large ones and they are dropped out on the path with nothing coming back
to say so.

The log used to print a guess: *"a 1500-byte path fits 1415"*. That assumes the
whole route carries 1500 bytes, which a real route frequently does not — a PPPoE
hop, an encapsulating provider, a tunnel somewhere upstream. On one pair of
servers the true figure was **1371** against a configured **1400**, and those 29
bytes cost an afternoon: everything looked healthy and nothing worked.

### How it works

Once a session is up, each end sends probe packets padded to exactly the size a
full data packet would be, and binary-searches for the largest that comes back
acknowledged. The result goes straight onto the interface, and the MSS clamp is
recalculated with it.

- **Each end measures its own sending direction.** A path can carry more one way
  than the other, and what an interface MTU governs is what *this* host sends.
  Nothing has to be negotiated.
- **Probes are encrypted and authenticated** under the tunnel session, so only a
  peer holding the token can answer one. Nobody else can move your MTU.
- **Re-measured every 30 minutes**, because paths change.
- **A peer too old to answer probes leaves your configured MTU alone.** Turning
  this on cannot break an existing pair.

In the log:

```
l3: the path only carries 1371 bytes, not 1400 — interface lowered to 1371.
    Large transfers would otherwise stall while ping kept working.
```

### Turning it off

```toml
[l3]
auto_mtu = false
```

or answer *no* to "Let the tunnel measure and correct the MTU automatically"
under **Fine-tune the advanced settings by hand**. The `mtu` you set is then
used exactly as written.

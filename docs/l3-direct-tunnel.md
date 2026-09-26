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

**From the menu — the easy way.** Set up the **Iran server first**, then paste
one line on the kharej server.

1. **Iran:** `sudo backpack` → **Setup Iran** → **Direct** → the carrier —
   **xDi**, **PCK**, **UDP**, **Quic**, **IP Spoofing** or **SNI Spoofing**. Then, in order: **Kharej IP Or Domain**,
   **Tunnel Port**, **Forwarded Ports** (blank for a plain IP tunnel — TUN),
   **Tunnel Name**, **Security Token** (generated here — press Enter), UDP,
   **Error Correction (FEC)**, the preset, and whether to fine-tune. The tunnel
   addresses are chosen for you — a free `10.10.N.0/30` — and can be changed
   under fine-tune.
2. The summary before **Create This Tunnel** shows the **Setup Link**: one line
   starting with `backpack://`. It carries everything both ends must agree on
   — token, port, carrier, addresses, GRE key, error correction, MTU and
   segment cap — so nothing is retyped. It is shown again at any time under
   **Manage tunnels → the tunnel → Setup Link**.
3. **Kharej:** `sudo backpack` → **Setup Kharej** → **Direct** → the **same
   carrier** → **Setup Link**, paste the line, confirm. Done.

The kharej side can still be filled in by hand instead (**Manual**): the
tunnel port, the tunnel addresses, and the token — which must be pasted from
the Iran server; the kharej side never makes one up.

> The link contains the tunnel's token. Treat it as the secret it is.

**IP Spoofing and SNI Spoofing are the exception.** They keep the classic
wizard: no setup link, both servers set up by hand, the token suggested on the
kharej server and pasted on Iran, and the tunnel addresses asked on both.

The rest of this page is what the two sides write.

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
| `85.11.12.13:10000-10009` | a range bound to one local address |
| `443=10.0.0.1:80\|10.0.0.2:80` | two backends, load-balanced |

A target with no host of its own means `peer_ip`, which is what almost every
mapping wants. Full reference: [Port mappings](port-mappings.md). `accept_udp` adds UDP alongside TCP; it is off by default, for
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

## Several kharej servers behind one Iran server

The Iran side of a direct tunnel dials, so one Iran server can reach as many
kharej servers as you like: **one tunnel per kharej**, each with its own
interface (`bp0`, `bp1`, …), its own `10.10.N.0/30` and its own carrier socket.
Every carrier works this way, `pck` included — each tunnel builds and reads its
own packets on its own port.

A forwarded port can then be served by all of them. The kernel routes each
peer address over its own interface, so one mapping on one of the tunnels can
list every kharej:

```toml
ports = ["443=10.10.0.2:443|10.10.1.2:443|10.10.2.2:443"]
```

- **Each new connection goes to the kharej with the fewest connections open**,
  so bandwidth adds up: measured with `pck`, two kharej each limited to
  300 Mbit/s gave 267 Mbit/s apiece alone and **535 Mbit/s together** on the
  shared port.
- **A kharej that stops answering is set aside for 20 seconds** and tried again
  after that, or sooner if nothing else answers. While another is left to try,
  a connection gives up on a dead one after 3 seconds rather than 10: with one
  of two kharej killed, ten new connections took 3.1 s in all (50 s before).
- One connection always travels over one kharej, so a single download is
  limited to that kharej's link; it is many connections that add up.

**You do not have to write that mapping by hand.** Set up the first kharej as
usual. When you set up the second one on the Iran server and give it a port the
first already forwards, the wizard offers to share it: the port stays on the
first tunnel and the new kharej is added to it as another backend. The panel
does the same without asking, since binding the port twice could only fail.
A port *range* cannot be shared (which backend would port 10005 belong to?);
the wizard leaves it out and the panel refuses it with the reason.

**Each kharej takes its own link.** Run **Setup Iran → Direct** once per kharej;
each run prints a link for that kharej alone, with its own token and its own
`10.10.N.0/30`, and the kharej pastes it. Measured through the menus on one Iran
with three kharej (Germany, USA, Finland — ports 1245, 3294 and 3190), 1 GiB
crossed each tunnel at the same time at about 1 Gbit/s apiece, on `pck` and on
`xdi` alike, with one session per tunnel and no drop after the tunnels sat idle.

If you fill a kharej in by hand, give it exactly the tunnel addresses the Iran
server printed. A block picked on the kharej's own machine is not the one the
Iran server chose for a second kharej, and a tunnel set up that way comes up,
reports a peer, and carries nothing.

### When it does not come up

The log names the cause rather than leaving a silent tunnel:

- **`a handshake from … did not authenticate`** — the tokens on the two servers
  differ. Paste the Iran server's setup link again, or compare the token under
  **Edit → Show the token** on both.
- **`connections to :PORT are being refused because the tunnel is not up yet`**
  — the forwarded port works; no handshake has completed. Look at the lines
  above it.
- **`local_ip` and `peer_ip` are the same address** — refused when the config
  is loaded. Each end has its own address in the `/30`; the kharej's
  `peer_ip` is the Iran end, not its own.
- A forwarded port that something on the server already listens on — the web
  panel's `7777` is the usual one — is refused by the wizard and the panel
  instead of failing to bind in the log.

A tunnel that carries only one-way traffic, or none, is not torn down for it.
When the peer has been quiet for 15 seconds the tunnel asks it directly, under
the current session, before starting a new handshake, so an idle or one-way
tunnel no longer drops and reconnects every few seconds.

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

`xdi` installs one iptables rule on the listening side while it runs. The
client's data travels in Echo Requests, and without the rule the kernel answers
each one with an Echo Reply carrying the same payload — every uploaded byte
would leave the server a second time. The rule drops only those automatic
replies (it matches the tunnel's tag together with the client's direction
byte), so an ordinary ping to the server, or across the tunnel, still answers.
It is removed when the tunnel stops, and it needs the `u32` iptables module;
without it the tunnel works and simply spends the extra uplink.

`xdi` also opens its own way in. A server that drops ICMP in its firewall (an
`INPUT` policy of `DROP`, a `ufw` default) would otherwise swallow every echo
before the tunnel reads it, and the tunnel would sit waiting with no error. On
both ends it inserts one `ACCEPT` rule for its own echoes — same tag, same
direction byte, nothing else — and removes it on exit. Where it cannot, the log
says which firewall rule is in the way.

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
- The dialler's handshake carries a **timestamp** inside its encrypted,
  authenticated payload, and the listener refuses one that is not newer than
  the last it accepted — WireGuard's rule — so a handshake recorded off the
  wire cannot be replayed to keep the tunnel from coming up. Against a listener
  from before v1.8.2 the dialler falls back to the older handshake, on the
  listener's own authenticated answer and on nothing weaker, and tries again
  every half hour; the old listener logs one "wrap packets differently" line
  each time until it is upgraded.
- If the listener restarts, the dialler notices within about twenty seconds —
  nothing comes back for fifteen while it is sending — and handshakes again,
  instead of sending into a session the listener no longer has.
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
- **`quic` needs room for QUIC's own framing.** A DATAGRAM frame can carry
  only what the connection's current packet size allows, which starts at 1232
  and grows as QUIC discovers the path. Leave `auto_mtu` on (the default) and
  the tunnel measures what actually fits; a fixed `mtu` on a narrow path can
  be too big for it, and the packets that do not fit are dropped.
- **No reliable carrier, ever.** `tcp`, `ws` and `kcp` are refused by design:
  an IP packet already belongs to something that handles its own loss, and
  stacking two retransmit timers makes throughput collapse under loss rather
  than degrade. This is the classic TCP-over-TCP meltdown.
- **Two peers per tunnel.** A tunnel is one point-to-point link; several kharej
  servers are several tunnels (see above).

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

Set it in the wizard under *Fine-Tune The Advanced Settings*, or later
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
under **Fine-Tune The Advanced Settings**. The `mtu` you set is then
used exactly as written.

---

<div dir="rtl">

## خلاصهٔ فارسی

**چند سرور خارج پشت یک سرور ایران:** در تونل direct سمت ایران dial می‌کند، پس
برای هر خارج یک تونل جدا می‌سازی (اینترفیس و ‎`10.10.N.0/30`‎ جدا، با هر حاملی،
از جمله `pck`). یک پورت می‌تواند بین همه‌شان پخش شود:
‎`443=10.10.0.2:443|10.10.1.2:443`‎. هر اتصال جدید به خارجی می‌رود که کمترین اتصال
باز را دارد، پس پهنای باند جمع می‌شود (دو خارجِ ۳۰۰ مگابیتی: ۵۳۵ مگابیت با هم)،
و خارجی که جواب ندهد ۲۰ ثانیه کنار گذاشته می‌شود. ویزارد وقتی پورت تکراری بدهی
خودش پیشنهاد اشتراک می‌دهد و پنل بدون پرسیدن این کار را می‌کند.

**راه‌اندازی با لینک:** اول سرور ایران را بساز: **Setup Iran ← Direct ← حامل**،
بعد آی‌پی یا دامنهٔ خارج، پورت تونل، پورت‌های forward (خالی = TUN)، نام، توکن
(ایران خودش می‌سازد — Enter بزن)، UDP، تصحیح خطا (FEC)، preset و تنظیم دستی. در
خلاصهٔ قبل از **Create This Tunnel** یک **لینک `backpack://`** نشان داده می‌شود. روی سرور خارج: **Setup Kharej ← Direct ← همان
حامل ← Setup Link** و لینک را پیست کن — توکن،
آدرس‌ها و بقیهٔ تنظیمات خودکار پر می‌شوند. برای هر خارج یک بار از ایران تونل بساز؛
هر کدام لینک خودش را دارد. لینک شامل توکن است، پس مثل رمز نگهش دار. اگر خارج را
دستی پر کنی، توکن و آدرس‌ها را دقیقاً همان‌طور که ایران چاپ کرد وارد کن.

**وقتی بالا نمی‌آید:** لاگ علت را می‌گوید — توکن متفاوت (`did not authenticate`)،
آدرس یکسان برای `local_ip` و `peer_ip`، یا پورتی که چیز دیگری (مثل پنل روی
`7777`) گرفته. تونلی که ترافیک یک‌طرفه یا هیچ ترافیکی ندارد دیگر قطع و وصل نمی‌شود،
و `xdi` روی سروری که ICMP را در فایروال می‌بندد خودش راه ورودش را باز می‌کند.

هر ترنسپورت دیگری در Backpack **پورت** forward می‌کند: یک listener روی ایران، یک
dial به backend روی خارج، و یک stream وسطشان. این یکی فرق دارد: روی هر هاست یک
**اینترفیس شبکه** می‌سازد و پکت کامل IP را بین‌شان حمل می‌کند، پس دو سرور یک لینک
نقطه‌به‌نقطهٔ معمولی می‌گیرند — `10.10.0.1` با `10.10.0.2` حرف می‌زند — که هر چیزی
می‌تواند رویش route شود. همان ایدهٔ GRE/IPIP است، ولی داخل هستهٔ خود Backpack و
داخل ترنسپورت‌های خودش.

**همیشه GRE + Noise.** انتخابی در کار نیست و ویزارد نمی‌پرسد. تونل GRE کرنلی —
همان چیزی که بیشتر راهنماها «GRE» می‌گویند — پکتش را به‌صورت **IP protocol 47**
لخت روی سیم می‌گذارد: بدون رمز، کاملاً قابل تشخیص، و با یک قانون فایروال حذف‌شدنی.
Backpack همان هدر GRE را می‌نویسد، ولی آن هدر چیزی نیست که سفر می‌کند: داخل یک
سشن رمزشده مهر می‌شود و به یک حامل تحویل داده می‌شود. چیزی که یک capture می‌بیند
حامل است — یک جریان عادی TCP، یک جریان UDP، ICMP echo، یا پکت‌های جعلی. هیچ
protocol 47ای برای بلاک‌کردن وجود ندارد. هزینه‌اش: **با هیچ‌کس دیگر کار نمی‌کند** —
سیسکو، MikroTik یا GRE لینوکسی نمی‌توانند با آن حرف بزنند. Backpack با Backpack.

**کِی می‌خواهیش:** وقتی که forwarder پورت شکل درستی نیست — پروتکل‌هایی که پورت
ندارند (ICMP، OSPF، ESP)، خواستن دو سرور روی **یک شبکهٔ خصوصی** که با آدرس در
دسترس باشند نه با mappingِ از پیش نوشته، اجرای **routing** روی لینک، یا حمل چیزی
که خودش قابلیت اطمینان و رمزنگاری دارد و فقط جابه‌جایی پکت می‌خواهد. برای حالت
عادی — عرضهٔ چند سرویس روی ایران — تونل پورت ساده‌تر است و سال‌ها production پشتش
دارد.

**جهت آزاد است.** وقتی بالا آمد متقارن است؛ تنها عدم‌تقارن این است که چه کسی اول
دست دراز می‌کند. `listen` را روی هاستی بگذار که می‌تواند اتصال ورودی بپذیرد و
`dial` را روی دیگری. برای ایران ⇄ خارج معمولاً `dial` روی ایران و `listen` روی
خارج — یعنی جهت **مستقیم**، که ایران به هیچ پورت ورودی نیاز ندارد.

**دو فایل، یکی روی هر هاست**، و باید روی توکن، encapsulation و حامل با هم بخوانند.

**امنیت:** handshake همان **Noise NNpsk0** است با کلید مشترکِ برگرفته از توکن —
کانالی رمزشده، احراز هویت دوطرفه و forward-secret. روی آن: هر پکت یک **شمارندهٔ
صریح** دارد و در برابر یک **پنجرهٔ replay ۲۰۴۸ بیتی** بررسی می‌شود؛ هدر به‌عنوان
داده‌ی اضافی **احراز** می‌شود پس چیزی در آن قابل دست‌کاری نیست؛ سشن‌ها هر دو دقیقه
**rekey** می‌شوند؛ طرفی که توکن ندارد **هیچ جوابی** نمی‌گیرد؛ و آدرس peer فقط از
پکتی یاد گرفته می‌شود که قبلاً احراز شده، پس کسی با جعل یک دیتاگرام نمی‌تواند
تونل را منحرف کند. **توکن طولانی و تصادفی بگذار** — تنها چیزی است که بین تونل تو
و هر کسی که به پورت می‌رسد ایستاده.

**MTU خودکار — پیش‌فرض روشن.** MTU تنها تنظیمی است که از روی کانفیگ درنمی‌آید و
بدترین خرابی را وقتی غلط باشد می‌دهد: تونل بالا می‌آید، هر health check را رد
می‌کند، `ping` و SSH را حمل می‌کند — و هر دانلود و هر TLS handshake گیر می‌کند،
چون پکت‌های مهم بزرگ‌اند و جایی در مسیر بی‌صدا دور ریخته می‌شوند. قبلاً یک حدس چاپ
می‌شد؛ روی یک جفت سرور عدد واقعی **۱۳۷۱** بود در برابر **۱۴۰۰** تنظیم‌شده، و همان
۲۹ بایت یک بعدازظهر را گرفت. حالا خود تونل با پکت‌های probe اندازه می‌گیرد.

**محدودیت‌ها:** فقط **لینوکس**، و نیازمند root یا `CAP_NET_ADMIN` (حامل‌های
obfuscated به `CAP_NET_RAW` هم نیاز دارند). **هیچ‌وقت حامل قابل‌اطمینان**: `tcp`،
`ws` و `kcp` عمداً رد می‌شوند — پکت IP خودش به چیزی تعلق دارد که loss خودش را
مدیریت می‌کند، و روی‌هم‌گذاشتن دو تایمر retransmit یعنی throughput زیر loss به‌جای
افت، فرو می‌ریزد (همان TCP-over-TCP meltdown کلاسیک). **دو peer**، نه بیشتر.

**نمی‌تواند به تونل معکوس آسیب بزند:** در جدول `[l3]` و پکیج خودش زندگی می‌کند.
اگر اینجا چیزی بد رفتار کرد، فایل `[l3]` را پاک کن؛ هیچ چیز دیگری عوض نمی‌شود.

</div>

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v1.8.4.*

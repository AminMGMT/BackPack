# Transports

The optional managed Naive HTTP/2 wrapper runs with the existing TCP Reverse
engine, configured using `[server.naive]` and `[client.naive]`. Install its pinned
official helpers with `BP_HELPERS=naive bash install.sh` on Linux glibc amd64 or
arm64; the installer prints their versioned absolute paths. Certificate/key
renewal remains external, and valid file replacements reload the tunnel without
a TOML edit. Invalid or incomplete replacements keep the current generation.
Helper settings are configured manually and remain outside automatic transport
selection. Each side owns its helper under the tunnel context; TCP forwarding
and one endpoint are currently supported.

`BP_HELPERS=xray` installs the pinned official Xray helper in a versioned
directory; `BP_HELPERS=naive,xray` installs both sets. Existing helper directories
are retained. No helper is started by the installer. Xray transport settings
require a Backpack build that implements the managed Xray wrapper.

## Managed HTTPS carriers

Use a build containing these changes on both machines. From its source checkout,
`BP_BUILD_FROM_SOURCE=1 BP_HELPERS=naive,xray bash install.sh` builds that checkout
and installs the pinned helpers. Without the source flag the installer prefers
the latest published BackPack release, which may predate these carriers.

These optional Linux helpers carry the existing **TCP reverse engine**. Keep
`transport = "tcp"` and use the same literal loopback `server.bind_addr` and
`client.remote_addr` on both machines. The Iran helper's public listener must
use a different port. The outside client initiates the encrypted connection
toward Iran. Only TCP forwarding with one endpoint is supported; UDP forwarding,
transport/address fallbacks, other outbound proxies, Direct and L3 are refused.
Choose one helper per tunnel. Helper configuration is manual and ordinary edits
preserve it; setup links cannot export these credentials.

The engine owns each helper, stores generated configuration with owner-only
permissions, restarts a crashed process, and removes its process group and
temporary directory when the generation ends. Local listener readiness is
distinct from tunnel health: verify traffic through a forwarded entry port.
Runtime startup failures also retry with a cancellable one-second backoff;
invalid generated configuration is rejected before launching the supervisor.
Existing flows can fail during a crash or reload; new flows recover after the
reverse control connection and pool reconnect. Start with a modest pool and
measure memory/CPU and verified throughput before increasing it: each reverse
pool connection adds work in the helper as well as the TCP engine.

Both engines must run the same managed-carrier build. Managed connections now
carry bounded data records and an explicit EOF marker inside the encrypted
helper stream, because a physical TCP half-close can make a proxy helper close
the response direction as well. The adapter preserves delayed backend replies
and closes the physical stream once both directions finish or the generation
is cancelled. Ordinary TCP connections keep their existing wire format. The
record header adds four bytes per at-most-64KiB chunk; vectored TCP writes avoid
a separate header syscall. There is no automatic fallback to older unframed
managed peers. Deploy the matching build to both sides together.

### Naive HTTP/2

Install with `BP_HELPERS=naive bash install.sh`. The pinned versions are
NaiveProxy `v154.0.8037.49-4` and sing-box `v1.14.2`. On amd64, for example:

```toml
# Iran; the ordinary [server] table still owns token, ports and pool tuning.
[server]
bind_addr = "127.0.0.1:3080"
transport = "tcp"
token = "REPLACE-WITH-A-RANDOM-SHARED-TOKEN"
ports = ["8080=127.0.0.1:8081"]
accept_udp = false
[server.naive]
binary = "/usr/local/lib/backpack/helpers/sing-box/v1.14.2-amd64/sing-box"
listen = "0.0.0.0:443"
username = "REPLACE-WITH-A-PRIVATE-USERNAME"
password = "REPLACE-WITH-A-RANDOM-PASSWORD"
certificate = "/etc/letsencrypt/live/tunnel.example.com/fullchain.pem"
key = "/etc/letsencrypt/live/tunnel.example.com/privkey.pem"
```

```toml
# Outside; 8081 is the local backend named by the Iran forwarding rule above.
[client]
remote_addr = "127.0.0.1:3080"
transport = "tcp"
token = "REPLACE-WITH-A-RANDOM-SHARED-TOKEN"
[client.naive]
binary = "/usr/local/lib/backpack/helpers/naive/v154.0.8037.49-4-amd64/naive"
server = "tunnel.example.com:443"
username = "REPLACE-WITH-A-PRIVATE-USERNAME"
password = "REPLACE-WITH-A-RANDOM-PASSWORD"
# ca_file = "/absolute/path/to/private-ca.pem"  # only for your own CA
```

Use a valid certificate for the outer hostname. This sing-box inbound does not
include a complete website frontend or an active-probe camouflage guarantee.

### XHTTP over TLS

Install with `BP_HELPERS=xray bash install.sh`; the tested contract uses official
Xray `v26.3.27`. Keep the ordinary reverse tables from the example above and
replace the Naive tables with these, using a fresh UUID (`xray uuid`) and a
random private path shared by both helpers:

```toml
[server.xray]
binary = "/usr/local/lib/backpack/helpers/xray/v26.3.27-amd64/xray"
listen = "0.0.0.0:443"
mode = "xhttp"
uuid = "REPLACE-WITH-A-CANONICAL-UUID"
server_name = "tunnel.example.com"
path = "/REPLACE-WITH-A-RANDOM-PRIVATE-PATH"
certificate = "/etc/letsencrypt/live/tunnel.example.com/fullchain.pem"
key = "/etc/letsencrypt/live/tunnel.example.com/privkey.pem"
```

```toml
[client.xray]
binary = "/usr/local/lib/backpack/helpers/xray/v26.3.27-amd64/xray"
server = "tunnel.example.com:443"
mode = "xhttp"
uuid = "REPLACE-WITH-A-CANONICAL-UUID"
server_name = "tunnel.example.com"
path = "/REPLACE-WITH-A-RANDOM-PRIVATE-PATH"
# ca_file = "/absolute/path/to/private-ca.pem"
```

TLS verification remains enabled and HTTP/2 is selected. Optional `host` sets
the HTTP host and must match on both sides; empty uses `server_name`. A CDN must
support the selected XHTTP requests, response streaming, timeouts and origin
TLS configuration. CDN deployment is route/provider dependent and has not been
validated by the local integration tests. This first wrapper does not expose
H3, split download settings, raw JSON overrides, or a website frontend.

### RAW / REALITY / Vision

Use `mode = "reality"` on both sides, the same UUID and a fresh 16-character
hexadecimal `short_id`. Generate an X25519 pair with the installed `xray x25519`;
put its private key only on Iran and its corresponding public value on the
outside client (the pinned CLI names that value `Password`). Both are unpadded
base64url strings encoding 32 bytes.

In `[server.xray]`, replace `path`, `certificate`, and `key` with `private_key`,
`short_id`, and an explicit `target = "cover.example.com:443"`. In `[client.xray]`,
replace `path` with `public_key` and the same `short_id`. Set `server_name` on
both sides to the DNS name accepted by the cover endpoint's certificate. Choose
a reachable TLS 1.3 endpoint with HTTP/2 that you control; it must differ from
the helper and reverse listeners. There is no `ca_file` or HTTP `host` in this
mode. The generated VLESS account uses Vision and the client uses the Chrome
fingerprint. REALITY uses a direct outer connection and is not a CDN transport.

The Xray SOCKS listener permits only the reverse target, and authenticated Iran
traffic can reach only that target. The REALITY unauthenticated cover behavior
uses the explicitly configured cover endpoint. None of these carriers promises
availability on every filtered network; validate your own Iran/outside route.

For XHTTP and Naive, certificate renewal is external. Valid changes to the
certificate/key files or a configured CA file reload the tunnel without a TOML
edit. Identical file rewrites do not restart it; missing files, invalid pairs,
and expired certificates keep the running generation.
Naive server and Xray configurations embed validated TLS material so a helper
cannot reread those files after validation or install a rejected renewal.
Protect both TOML files and the helper binaries from untrusted writes. The
installer verifies pinned
archive hashes and never replaces an existing version directory.

Backpack carries every tunnel over one transport, chosen when you create the
tunnel and changeable later from **Edit → Change transport**. They all move the
same traffic between the two engines — they differ only in what they put on the
wire, and therefore in how fast, how reliable, and how hard to detect they are.

Not sure which to pick? Run **Manage → Link Test** on the kharej server; it
measures your route and recommends one. See
[Choosing a transport](choosing-a-transport.md).

**Twelve of them.** IP Spoofing used to be a thirteenth and is not any more —
it is a carrier of the [direct tunnel](l3-direct-tunnel.md) now, for the reason
given at the bottom of this page.

| Transport | Family | Encrypted handshake | PROXY protocol | Needs | Setup guide |
|-----------|--------|:--:|:--:|-------|---|
| TCP | TCP | — | ✅ | — | [→](../tutorial/tcp.md) |
| TCP Mux | TCP | — | ✅ | — | [→](../tutorial/tcp-mux.md) |
| **TCP + Stealth** | TCP | ✅ (Noise) | ✅ | — | [→](../tutorial/tcp-stealth.md) |
| **TCP + PCK** | TCP | ✅ (token key) | ✅ | Linux, root | [→](../tutorial/tcp-pck.md) |
| UDP | UDP | — | — | UDP open | [→](../tutorial/udp.md) |
| **UDP + KCP + FEC** | UDP | ✅ (token key) | ✅ | UDP open | [→](../tutorial/udp-kcp-fec.md) |
| UDP + QUIC | UDP | ✅ (TLS 1.3, session-bound) | ✅ | UDP open | [→](../tutorial/udp-quic.md) |
| WS | WebSocket | — | — | — | [→](../tutorial/websocket.md) |
| WS Mux | WebSocket | — | ✅ | — | [→](../tutorial/websocket.md) |
| WSS | WebSocket | ✅ (TLS) | — | certificate | [→](../tutorial/websocket-tls.md) |
| WSS Mux | WebSocket | ✅ (TLS) | ✅ | certificate | [→](../tutorial/websocket-tls.md) |
| **xDi (ICMP)** | Experimental | ✅ (token key) | ✅ | Linux, root, ICMP open | [→](../tutorial/xdi-icmp.md) |

"Encrypted handshake" means the tunnel's own credential is protected on the
wire. On the plain transports (TCP, TCP Mux, UDP, WS, WS Mux) the token is sent
as-is, so use one of the encrypted transports on an untrusted path. It is not
only the token: something on such a path can also stand in for the Iran server,
and the Iran server is what tells the kharej side which address to dial for each
connection — so the kharej machine would dial wherever it was told. TCP +
Stealth, QUIC and the token-keyed transports (UDP + KCP + FEC, TCP + PCK, xDi)
authenticate the server as well as the client: nothing without the token can
complete their handshake or produce a packet the client accepts. WSS does too
once the Iran server is up to date (see WSS below), except with Simple Token
Auth, where a trusted TLS-terminating proxy in front (NGINX, usually) stands in
for the server and neither end's proof is used.

QUIC binds the credential to its TLS session, as WSS does. The client does
not verify the server's certificate — the tunnel trusts its token — so it does
not send the token either: it proves it holds it with an HMAC over keying
material exported from the TLS session, and the server answers with a proof of
its own. Something that terminates the TLS on the path holds a different
session with each end, so neither proof means anything to it and neither
reveals the token. **Upgrade the Iran server first**: a new server still takes
an older client's plain token, but a new client never sends one, so an older
server refuses it (the client's log says so).

Every transport can carry **UDP on its forwarded ports** — it is a per-tunnel
setting, off by default, and independent of the transport. See
[Forwarded UDP](forwarded-udp.md).

---

## TCP family

### TCP
A plain, reliable TCP stream. The simplest transport and a fine default on a
clean link. Fast, low overhead, no encryption of its own — anything sensitive
inside it should already be encrypted (VPN or TLS traffic usually is).

### TCP Mux
The same TCP stream, but many logical connections are **multiplexed** over a
small pool of real connections (via smux). This cuts the cost of opening a fresh
connection per request and behaves well when a service makes many short-lived
connections. Supports the PROXY protocol.

### TCP + Stealth
A TCP tunnel wrapped in a **Noise (NNpsk0) record layer**. On the wire it is two
short bursts that look like random bytes, followed by an encrypted stream that
looks the same — **no TLS ClientHello, no recognisable protocol, nothing for
deep packet inspection to fingerprint**.

The pre-shared key is derived from the tunnel token, so the transport needs no
key of its own. Because that key is mixed in from the first message, a peer
without the token cannot even complete the handshake: the server replies with
nothing, so a port scan finds a dead port rather than a service. Reach for it
where filtering is heavy and you want the connection itself to be unremarkable.
Costs a little more CPU than plain TCP for the encryption.

### TCP + PCK
A TCP transport that **does not use the kernel's TCP stack**. It builds its own
segments and reads the replies straight off the network device, upstream of
connection tracking and of every netfilter chain — so the machinery that would
normally reset, throttle or drop a long-lived TCP flow has nothing to act on.

Nothing is forged: the addresses and ports are real and the replies route
normally. What does not exist is the connection — no handshake, no socket, no
kernel state — while the segments themselves carry the timestamps, sequence
numbers and window a real one would. KCP underneath supplies the reliability the
absent stack would have.

Reach for it when a plain TCP tunnel connects and then dies, stalls or is
throttled for no reason the logs can explain. Linux only, needs root, and both
ends must be on it. See [TCP + PCK](tcp-pck.md).

---

## UDP family

### UDP
Raw datagrams, for forwarding UDP-based services — and only those: the exposed
ports listen on UDP and nothing else, so a TCP service is not carried at all.
No reliability layer — packets that are lost stay lost, which is correct for
protocols that expect that.

### UDP + KCP + FEC
A **low-latency gaming tunnel**: a reliable, ordered protocol built on top of
UDP, with **always-on forward error correction**. For every batch of data
packets it sends a few parity packets, so the receiver repairs lost packets
**instantly** instead of waiting a full round trip for a retransmit. Every
preset runs the same latency-first ARQ (NoDelay, a 10 ms tick, immediate ACKs,
KCP's own congestion window off) with the window kept near the
bandwidth-delay product so queueing — and therefore ping — stays bounded. This
is the transport for a route that loses packets where TCP keeps backing off, and
for real-time traffic like games where a stall hurts more than a little
overhead. Datagrams are encrypted with a key derived from the tunnel token.

> KCP runs over UDP. **If your provider filters or throttles UDP, it will not
> help** — use a TCP-based transport instead. Test before committing to it.

[Tunnel Metrics](tunnel-metrics.md) shows KCP's retransmits, lost/duplicated
segments and how many packets FEC repaired — the numbers that tell you whether
KCP is earning its overhead on your route.

### UDP + QUIC
The tunnel inside QUIC streams: its own TLS 1.3, its own stream multiplexing,
congestion control and loss recovery, so every byte is encrypted and there is
nothing to hand-tune.

**Offered, not recommended.** QUIC was built here once, tested on a real Iran
route, and dropped because it never completed a handshake there while KCP on the
same link ran at full speed. That finding still stands, which is why the Link
Test's advisor recommends KCP for a lossy link and names QUIC only as the other
thing to try. Test it on your own route before committing to it.

---

## Experimental family

Not flavours of TCP or UDP but different ideas about how to move bytes at all.
Both are Linux-only and need root.

### xDi (ICMP)
The KCP transport with its packets inside **ICMP echo requests and replies**
instead of UDP datagrams. Everything above the packet layer — reliability, error
correction, encryption — is identical.

For the one network where UDP and TCP are filtered but ICMP is not, because ping
is how such a network proves itself reachable. ICMP has no ports, so a raw ICMP
socket receives every ping the host sees; each tunnel derives a **session tag**
from its token, and a packet without this tunnel's tag is dropped without a
second look — which is how several xDi tunnels share one host, and stay clear of
stray pings and the kernel's own replies. Within a tunnel, each session — the
control channel and every pooled connection — takes an **echo identifier** of
its own, which is what stands in for the port ICMP does not have.

Slower than everything else and heavy on ICMP rate limits. A last resort, not a
default.

### IP Spoofing

Moved. The forged-source carrier is part of the **direct tunnel** now, not a
reverse transport: `transport = "spoof"` is refused at startup and the wizard
offers it under **Direct** instead. Everything it does is unchanged — the same
profiles, the same forged sources, the same evasion knobs — and it is documented
in **[IP Spoofing](ip-spoofing.md)** and **[the direct tunnel](l3-direct-tunnel.md)**.

A reverse tunnel could not use it: its pooled sessions all arrive at one address,
because a forged packet carries nothing to tell them apart by, so they collapsed
onto a single session and closed one another. A direct tunnel has one session.

## WebSocket family

These frame the tunnel as ordinary web traffic, which is useful where only
HTTP/HTTPS gets through, or where you want to sit behind a CDN.

### WS / WS Mux
Plain (unencrypted) WebSocket. `WS Mux` adds multiplexing over a connection pool
and supports the PROXY protocol. Because the transport itself is not encrypted,
the token travels in the clear — fine behind TLS termination you control, but
prefer WSS on an untrusted path.

### WSS / WSS Mux
WebSocket over **TLS**. `WSS Mux` adds multiplexing (and the PROXY protocol).
Two things make these more than "WS with TLS":

- **Browser TLS fingerprint.** A WSS tunnel is meant to look like ordinary
  HTTPS, but Go's default TLS ClientHello has a fingerprint of its own that
  filtering can pick out. Backpack sends a current **Chrome** fingerprint
  instead, so the handshake blends into normal browser traffic.
- **Session-bound credential.** The certificate is not verified (the tunnel
  trusts its token, and the cert is often self-signed), which would leave a
  bearer token readable by anything that terminates the TLS on the path. So the
  token is not sent: each side derives keying material from the TLS session and
  the client proves it holds the token with an HMAC over that material. A man in
  the middle has a different session and cannot replay it. The server proves
  itself back the same way, in the upgrade response, and the client refuses a
  wrong answer — and a missing one from a server that has answered it since the
  client process started. An older server sends no answer, so a new client
  still reaches it; upgrade the Iran server to close the gap. (With Simple
  Token Auth neither proof applies.)
- **Decoy site.** Anything that is not a genuine tunnel connection — a browser,
  a scanner, a probe with the wrong token — is answered by a stock **nginx**:
  the "Welcome to nginx!" page at `/`, a normal `404` everywhere else, with the
  headers a real file carries. Each install derives its own nginx version, page
  date and `ETag` from its token, so no two servers answer alike and the fleet
  cannot be found with one scan. Built in and always on. See
  [Decoy site (WSS camouflage)](camouflage.md).

**Certificate:** at setup you can get a **Let's Encrypt** certificate
(renewed automatically, needs a domain pointing at the server) or use a
self-signed one. This is asked during tunnel creation, and can be changed later
from **Edit → Certificate**.

> **CDN note:** to sit behind a CDN, the tunnel must be WSS/WSS Mux on a
> CDN-proxied port (443, 8443, 2053, …). Setup warns you if you point a raw
> transport at a CDN, or at a domain whose AAAA record would send the tunnel
> over IPv6.

---

<div dir="rtl">

## خلاصهٔ فارسی

هر تونل روی **یک ترنسپورت** حمل می‌شود که موقع ساخت انتخاب می‌شود و بعداً از
`Edit → Change transport` قابل تعویض است (روی هر دو طرف). همه یک ترافیک را
جابه‌جا می‌کنند؛ تفاوتشان در چیزی است که روی سیم دیده می‌شود.

**خانوادهٔ TCP:** *TCP* ساده و سریع (نقطهٔ شروع)؛ *TCP Mux* برای سرویس‌هایی با
اتصال‌های کوتاه و زیاد؛ *TCP + Stealth* رمزنگاری Noise بدون هیچ fingerprint —
بهترین گزینه برای فیلترینگ سنگین؛ *TCP + PCK* که اصلاً از استک TCP کرنل استفاده
نمی‌کند و برای وقتی است که تونل TCP وصل می‌شود و بعد بی‌دلیل می‌میرد.

**خانوادهٔ UDP:** *UDP* خام (بدون قابلیت اطمینان)؛ *UDP + KCP + FEC* تونل
کم‌تأخیر بازی با تصحیح خطای همیشه‌روشن؛ *UDP + QUIC* که فقط «در دسترس» است و
پیشنهاد نمی‌شود چون روی مسیر واقعی ایران handshake را کامل نمی‌کرد.

**خانوادهٔ WebSocket:** شبیه ترافیک وب معمولی و سازگار با CDN. *WSS* با
fingerprint واقعی کروم، توکنی که فرستاده نمی‌شود، و یک **سایت تقلبی** که به هر
کاوشگری صفحهٔ nginx نشان می‌دهد.

**خانوادهٔ آزمایشی:** *xDi* که تونل را داخل پینگ می‌برد (برای شبکه‌ای که TCP و
UDP را می‌بندد ولی ICMP را نه) و *IP Spoofing* که مبدأ پکت‌ها را جعل می‌کند
(برای مسیری که بر اساس آدرس محدود می‌کند). هر دو لینوکس + root می‌خواهند.

روی **همهٔ** ترنسپورت‌ها می‌شود UDP پورت‌های forward شده را هم عبور داد — یک
تنظیم جدا و پیش‌فرض خاموش است: [Forwarded UDP](forwarded-udp.md).

</div>

---
[← Back to the docs index](README.md) · [Setup walkthroughs →](../tutorial/README.md)

---

*Last verified against Backpack v1.8.5.*

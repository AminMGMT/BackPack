# Transports

Backpack carries every tunnel over one transport, chosen when you create the
tunnel and changeable later from **Edit → Change transport**. They all move the
same traffic between the two engines — they differ only in what they put on the
wire, and therefore in how fast, how reliable, and how hard to detect they are.

Not sure which to pick? Run **Manage → Link Test** on the kharej server; it
measures your route and recommends one. See
[Choosing a transport](choosing-a-transport.md).

| Transport | Family | Encrypted handshake | PROXY protocol | Needs | Setup guide |
|-----------|--------|:--:|:--:|-------|---|
| TCP | TCP | — | ✅ | — | [→](../tutorial/tcp.md) |
| TCP Mux | TCP | — | ✅ | — | [→](../tutorial/tcp-mux.md) |
| **TCP + Stealth** | TCP | ✅ (Noise) | ✅ | — | [→](../tutorial/tcp-stealth.md) |
| **TCP + PCK** | TCP | ✅ (token key) | ✅ | Linux, root | [→](../tutorial/tcp-pck.md) |
| UDP | UDP | — | — | UDP open | [→](../tutorial/udp.md) |
| **UDP + KCP + FEC** | UDP | ✅ (token key) | ✅ | UDP open | [→](../tutorial/udp-kcp-fec.md) |
| UDP + QUIC | UDP | ✅ (TLS 1.3) | ✅ | UDP open | [→](../tutorial/udp-quic.md) |
| WS | WebSocket | — | — | — | [→](../tutorial/websocket.md) |
| WS Mux | WebSocket | — | ✅ | — | [→](../tutorial/websocket.md) |
| WSS | WebSocket | ✅ (TLS) | — | certificate | [→](../tutorial/websocket-tls.md) |
| WSS Mux | WebSocket | ✅ (TLS) | ✅ | certificate | [→](../tutorial/websocket-tls.md) |
| **xDi (ICMP)** | Experimental | ✅ (token key) | ✅ | Linux, root, ICMP open | [→](../tutorial/xdi-icmp.md) |
| **IP Spoofing** | Experimental | ✅ (token key) | ✅ | Linux, root, a path that passes forged sources | [→](../tutorial/ip-spoofing.md) |

"Encrypted handshake" means the tunnel's own credential is protected on the
wire. On the plain transports (TCP, TCP Mux, UDP, WS, WS Mux) the token is sent
as-is, so use one of the encrypted transports on an untrusted path.

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
Raw datagrams, for forwarding UDP-based services. No reliability layer — packets
that are lost stay lost, which is correct for protocols that expect that.

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
Writes its own IP packets and stamps a **forged source address** on them, for a
path that blocks, throttles or counts by address. Routing still uses the real
peer, so the packet arrives; only the on-wire header says otherwise. KCP above
the packet layer, like `xdi` and `pck`.

It only carries anything where the network above the machine forwards packets
with a forged source — plenty of providers drop them, and the built-in
**IP Spoofing Tester** is how you find out. It also has a **relay mode** that
carries a whole WireGuard VPN (or another tunnel) instead of forwarded ports,
with no KCP underneath. Every setting is documented in
[IP Spoofing](ip-spoofing.md).

---

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
  the middle has a different session and cannot replay it.
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

# Transports

Backpack carries every tunnel over one transport, chosen when you create the
tunnel and changeable later from **Edit → Change transport**. They all move the
same traffic between the two engines — they differ only in what they put on the
wire, and therefore in how fast, how reliable, and how hard to detect they are.

Not sure which to pick? Run **Manage → Link Test** on the kharej server; it
measures your route and recommends one. See
[Choosing a transport](choosing-a-transport.md).

| Transport | Family | Encrypted handshake | PROXY protocol | Needs |
|-----------|--------|:--:|:--:|-------|
| TCP | TCP | — | ✅ | — |
| TCP Mux | TCP | — | ✅ | — |
| **TCP + Stealth** | TCP | ✅ (Noise) | ✅ | — |
| UDP | UDP | — | — | UDP open |
| **UDP + KCP** | UDP | ✅ (token key) | ✅ | UDP open |
| WS | WebSocket | — | — | — |
| WS Mux | WebSocket | — | ✅ | — |
| WSS | WebSocket | ✅ (TLS) | — | certificate |
| WSS Mux | WebSocket | ✅ (TLS) | ✅ | certificate |

"Encrypted handshake" means the tunnel's own credential is protected on the
wire. On the plain transports (TCP, TCP Mux, UDP, WS, WS Mux) the token is sent
as-is, so use one of the encrypted transports on an untrusted path.

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

---

## UDP family

### UDP
Raw datagrams, for forwarding UDP-based services. No reliability layer — packets
that are lost stay lost, which is correct for protocols that expect that.

### UDP + KCP
A reliable, ordered protocol built on top of UDP, with **forward error
correction**: for every batch of data packets it sends a few parity packets, so
the receiver repairs lost packets **instantly** instead of waiting a full round
trip for a retransmit. This is the transport for a route that loses packets
where TCP keeps backing off. Datagrams are encrypted with a key derived from the
tunnel token.

> KCP runs over UDP. **If your provider filters or throttles UDP, it will not
> help** — use a TCP-based transport instead. Test before committing to it.

[Tunnel Metrics](tunnel-metrics.md) shows KCP's retransmits, lost/duplicated
segments and how many packets FEC repaired — the numbers that tell you whether
KCP is earning its overhead on your route.

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
  a scanner, a probe with the wrong token — is answered with an ordinary
  "Welcome to nginx!" page, so the server looks like a normal HTTPS website
  rather than a tunnel. Built in and always on. See
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
[← Back to the main README](../README.md)

# ADR 0004 — What an unproven peer may hold

**Status:** accepted (v1.8.5 cycle, 2026-09-28)

## Context

Every tunnel port is on a public address, and everything a server does before
a peer has proved the token it does for anyone who can reach that port. The
2026-09-27 audit found this handled case by case: some paths bounded it, some
did not, and two let a stranger undo what a genuine peer had set up. The
program that followed found more of the same shape: the reverse udp transport
judged control claims one at a time on its accept loop, so one silent
connection held the genuine client for fifteen seconds; the direct origin gave
every connection a goroutine for its whole handshake with no bound; the l3
listener remembered a handshake's identifier before judging it, so a flood of
made-up ones pushed the genuine ones out of its replay memory.

## Decision

An unproven peer holds a bounded share of the server, and nothing it does can
displace what a proven peer holds. Each ingress, and what bounds it:

| Ingress | Before the token is proved | Bound |
|---|---|---|
| reverse tcp, tcpmux, stealth — tunnel port | announcement 10 s; under stealth the Noise handshake first, up to 25 s in all | `acceptloop.Gate`: 128 per host (IPv6 per /64), 1024 in all |
| reverse udp — control port | control claim, 15 s | same gate; claims judged side by side |
| reverse quic — streams of one connection | announcement, 15 s | at most 8 streams waiting until one proves the token; a connection that opens more is closed |
| reverse ws, wss, wsmux — HTTP | request headers | `ReadHeaderTimeout`; the token is checked before the upgrade |
| reverse kcp, xdi, pck | — | KCP packets are encrypted under a token-derived key and checksummed inside it; a stranger's fail the check and open no session |
| direct origin — tcp, stealth | Noise/TLS handshake, 15 s | the same gate |
| direct origin — ws, wss | TLS and the HTTP upgrade, then the handshake | the upgrade happens before the gate sees the connection: bounded only by the HTTP server's timeouts (open) |
| l3 listener — handshakes | one Noise message | only an authenticated handshake is remembered, so a stranger without the token cannot push one out; one replaying more than 64 captured genuine handshakes still can, which matters only for a legacy dialler without timestamps |
| l3 quic carrier — connections | QUIC handshake | 16 connections; a stranger is evicted before the tunnel's peer |
| l3 pck — peers | one datagram | 1024 peers; strangers never written to are evicted first |

A refused connection is closed at once, and anything refused is simply
redialled. The bounds are shared by everyone who has not proved the token, so
a flood can fill them — which is why a host that *has* proved it passes the
gate without taking a place: the genuine client's control re-dials and pool
refills are not refused however full the gate is. What a flood can still do is
delay a client that has never connected from its address before (a first
start, or a new address), until the flood's connections time out.

## Consequences

- A flood costs the attacker in proportion to what it takes, and the genuine
  peer's tunnel is not what it takes.
- A new ingress is expected to appear in this table, with its bound, before it
  ships.
- What is not here: the control-plane side (the panel's login limits, the bot's
  chat checks) is in SECURITY.md and the panel's own docs.

# ADR 0006 — The plain transports prove both ends

**Status:** accepted (v2.0.0, 2026-11-20). Closes audit finding F-11.
After v2.0.0 it holds for `udp` only: `tcp`, `tcpmux`, `ws` and `wsmux` run
the plain engine and wire (`internal/plain`), which sends the token as it is.
The v2.0.0 engine is kept to serve a v2.0.0 peer until it is updated
(`internal/{server,client}/compat.go`), and proves both ends while it does.

## Context

The plain reverse transports — `tcp`, `tcpmux`, `ws`, `wsmux` and `udp` —
authenticated a control channel by sending the tunnel token as it is, and took
the server's echo of the token as proof that the server knew it. On a path
someone else controls that fails twice:

- the token is read off the wire, and is then good for any later connection;
- whoever read it can stand in for the Iran server. The Iran server is what
  names, for every connection, the address the kharej side dials, so standing
  in for it meant the kharej machine dialled wherever it was told — its own
  loopback services, or anything on its network.

`wss`, `quic`, stealth tcp and the token-keyed transports already proved both
ends. The docs told users to pick one of those on an untrusted path; most
users pick the transport that works.

The fix changes the wire, and the two ends of a tunnel are two machines that
are never upgraded at the same instant, so a v2.0.0 end must still make a
tunnel with a v1.9 end (Amin's decision, 2026-11-20) — without that being a
way for something on the path to push two v2.0.0 ends back onto the old
handshake.

## Decision

A new handshake, under its own signal `SG_ChanAuth` (on the stream
transports) or bearer prefix `bp3.` (on plain ws), in
`internal/utils/network/peerauth.go`:

    client → server  hello    Nc, MAC(K, "c" | label | Nc)
    server → client  reply    Ns, MAC(K, "s" | label | Nc | Ns)
    client → server  confirm  MAC(K, "f" | label | Nc | Ns)

K is derived from the token; Nc and Ns are fresh 16-byte values; the label is
the transport. The reply proves the server (it covers the client's fresh
value); the confirm proves the client (it covers the server's), so a replayed
hello gets nowhere. Both ends then hold a session key S.

- **Destinations are sealed.** The server appends MAC(S, destination) to every
  destination it sends — a tcp pool connection's `SG_TCP` frame, a mux
  stream's header, a ws text message, a udp socket's first reply — and the
  client dials only one that opens under S.
- **Pool connections present a tag derived from S** (tcp, tcpmux, ws, udp
  data sockets), so nothing that admits them crosses the wire.
- **The token never crosses the wire** on this handshake.
- It does not encrypt. A plain transport's traffic is as readable and
  alterable on the path as before; what is gone is learning the token,
  posing as either end, and steering the kharej's dials.

Compatibility, per end (`network.PeerGuard`):

- A client tries the new handshake. A server older than v2.0.0 closes on the
  unknown signal (or answers the ws upgrade with its decoy); after two such
  closes in a row the client offers the previous handshake and logs why. A
  refusal, a timeout or a reply that fails its proof is not a close and never
  causes the fallback — the last is something posing as the server.
- A server still accepts the previous handshake, with a warning once per
  process.
- **Neither end falls back once the other has proved itself**: the first
  completed handshake is remembered in `.<name>.peer` beside the tunnel's
  config (tied to the token), so it survives restarts and reboots.
- `peer_auth = "required"` refuses the previous handshake from the start.
  A Setup Link made by v2.0.0 carries a flag (`pv`) and the kharej built
  from it is written with `peer_auth = "required"`; the Iran server becomes
  strict on that kharej's first connection.
- `wss`/`wssmux` keep their TLS-bound proofs; with `simple_auth` (a trusted
  proxy in front) nothing here applies.

With it, the handshake from before v1.7.0 (`SG_Chan` on tcp and tcpmux, pool
connections admitted by source address) is gone: such a client is refused
with `too-old`. Its fallback was itself a downgrade path.

## Consequences

- v2.0.0 ⇄ v2.0.0 on a plain transport cannot be impersonated or steered.
- v2.0.0 ⇄ v1.9 keeps working, as before, until the older end is upgraded;
  a new client meeting an old server takes two failed attempts (8–16 s) to
  come up, once per run.
- Moving a tunnel back to a v1.9 peer on purpose, after the pair has proved
  itself, needs the `.peer` file deleted — the refusal in the log names it.
- Tested both directions against a real v1.9.0 binary
  (`TestThePreviousReleaseStillTalksToThisOne`), and the refusal after a
  proof, with its control (`TestAProvedTunnelRefusesTheOlderHandshake`).

# Forwarded UDP

A forwarded port can carry **UDP as well as TCP** — but only when you ask it to.
It is **off by default**: expose `443` and the tunnel carries 443/tcp until you
turn UDP on, at which point it also listens on 443/udp, relays both to the
kharej machine, and sends the replies back to whoever sent them.

> **Why off by default.** A browser's QUIC is UDP on port 443, so a tunnel that
> forwarded UDP for every port silently began carrying every QUIC flow a browser
> opened. On the connection-pooled transports (`ws`, `wss`, and the mux family)
> each of those long-lived flows holds a pooled connection for as long as the
> browser keeps it, which starves the TCP forwards sharing the pool — a site
> half-loads (images stall while audio plays) until a restart clears it. So UDP
> forwarding is now a deliberate choice: turn it on for the tunnels that need
> it, and a plain web or proxy tunnel is left to carry only TCP.

## Turning it on

- **Panel** — Edit the *server* tunnel → Fine Tune → switch on **Forward UDP as
  well as TCP on the exposed ports** → Save.
- **CLI** — answer *yes* to the UDP question under "Fine-tune the advanced
  settings by hand" at setup.
- **By hand** — add `accept_udp = true` under `[server]` and restart.

Turn it on because a lot of what people put behind a tunnel is not TCP-only:

| Service | What needs UDP |
|---|---|
| Xray / 3x-ui | VLESS/VMess with a UDP outbound, XUDP, QUIC-based inbounds |
| Shadowsocks | UDP relay (`-u`), which is what carries DNS and most games |
| WireGuard | everything — it is UDP and nothing else |
| DNS | every query |
| Game and voice traffic | almost all of it |

---

## How it works

With UDP forwarding on, the forwarded port opens a UDP socket alongside the TCP
listener. Each source address that sends a datagram becomes a **flow**, and a
flow is carried over the tunnel exactly the way a TCP connection is — same pool,
same limits, same metrics, same teardown. That shared pool is the reason it is
off by default: a flow holds a pooled connection for its whole life, so on a
pooled transport a crowd of long-lived flows (a browser's QUIC) squeezes out the
TCP forwards. Datagrams are length-prefixed on the wire so a packet that goes in
as one message comes out as one message of the same size.

On the kharej side the tunnel opens one UDP socket per flow to the backend, and
sends its replies back through the same flow. The source sees the answer coming
from the address it sent to, which is what any UDP client expects.

A flow ends when it goes quiet for **60 seconds** in both directions. UDP has no
close, so the mapping has a lifetime, and 60s comfortably outlasts the gaps a
real protocol leaves — it is also inside the window most home routers use for
their own UDP mappings, so a peer that keeps its own side alive keeps this one
alive too.

---

## Open the port for UDP in your firewall

This is the one thing that catches people. Opening a port for TCP does not open
it for UDP:

```bash
ufw allow 443/tcp
ufw allow 443/udp        # ← this one too
```

or with iptables:

```bash
iptables -A INPUT -p udp --dport 443 -j ACCEPT
```

If TCP works through the tunnel and UDP does not, check this first.

---

## Turning it off

Some servers already have something on the UDP side of a port they want to
forward over TCP. Set it per tunnel:

- **CLI** — Setup or Edit → fine-tune the advanced settings → *Forward UDP as
  well as TCP on the exposed ports*
- **Web panel** — Edit → Fine Tune → the same switch
- **By hand** — `accept_udp = false` in the `[server]` section

With it off, the forwarded port binds TCP only, which is what Backpack did
before v1.7.1.

If the UDP side of a port cannot be bound — something else already has it — the
tunnel logs a warning and carries on with TCP on that port. It is never fatal.

---

## What it does not cover

- **The `udp` transport is a different thing.** That is the *tunnel* itself
  running over bare datagrams, and it forwards UDP only. Forwarded UDP described
  here works on `tcp`, `tcpmux`, `stealth`, `ws`, `wss`, `wsmux`, `wssmux`,
  `kcp`, `xdi`, `spoof` and `quic` — the transports that carry streams.

  **You almost certainly do not want the `udp` transport.** It has no
  retransmission, no ordering and no error correction, so on a route with any
  loss or throttling it performs far worse than **UDP + KCP + FEC**, which is the same
  UDP carrier with those things on top. Since v1.7.1 there is no reason to pick
  it for the sake of forwarding a UDP service — every transport does that.
  Choose the transport that suits your route, and UDP follows.
- **Both ends must be on v1.7.1 or newer.** The client has to understand a flow
  marked as UDP; an older one logs a resolve error for that flow and carries on
  with TCP. Upgrade both sides.
- **PROXY protocol does not apply to a UDP flow.** There is nowhere in a
  datagram stream to put the header, and a UDP backend has no connection to
  attribute it to. TCP forwarding on the same port is unaffected.

---

## If UDP still does not pass

1. **Firewall** — `ufw allow <port>/udp`, on the Iran server. See above.
2. **Both ends upgraded?** `backpack -v` on each. A mixed pair forwards TCP
   only.
3. **Is the backend actually listening on UDP?** On the kharej machine:
   `ss -ulnp | grep <port>`. An Xray inbound with no UDP outbound configured has
   nothing to answer with.
4. **Is it turned off?** `grep accept_udp /etc/backpack/<tunnel>.toml`. An
   explicit `false` is honoured.
5. **Check the log** — `journalctl -u backpack-<tunnel> -n 50`. The line
   `UDP listener started successfully, listening on address: …` should appear
   once per forwarded port at startup.

---

## Related

- [Transports — every one explained](transports.md)
- [Real client IP (PROXY protocol)](real-client-ip.md)
- [Per-tunnel limits](limits.md)

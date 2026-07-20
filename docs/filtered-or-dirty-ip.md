# When a server is filtered, blocked, or its IP is dirty

Three different problems get lumped together as "it's filtered." They have
different fixes, so it helps to tell them apart.

| Problem | What you see | What fixes it |
|---------|--------------|---------------|
| **Pattern / DPI filtering** | The connection is throttled, reset, or dies after a moment — the *traffic* is what's flagged. | An obfuscated transport — **TCP + Stealth** or **WSS**. |
| **The IP is hard-blocked (L3)** | The address is simply unreachable; nothing connects. | A **clean IP**, or reach the server through a **CDN edge**. |
| **The IP is "dirty"** | The tunnel is fine, but destination sites block it, throw captchas, etc. | A **cleaner exit IP** — this is the exit's reputation, not the tunnel. |

## If the kharej (client) is filtered

This is the common case: a foreign VPS whose connection into Iran is throttled
or dropped by **how the traffic looks** (DPI). This is exactly what the stealth
transports are for.

- **Use TCP + Stealth.** It wraps the tunnel in a Noise layer with no
  fingerprint — on the wire it is indistinguishable from random bytes, so there
  is nothing for inspection to match, and the connection comes up.
- **Or use WSS**, which looks like a browser loading an ordinary HTTPS website
  (Chrome TLS fingerprint + a [decoy site](camouflage.md)).

> This works in practice, not just in theory: a Germany server that was filtered
> from Iran came back online as soon as its tunnel was switched to **TCP +
> Stealth** on port 443.

See [Transports](transports.md) for how each carrier looks on the wire.

## If the Iran server's address is filtered

The client can hold several addresses for the same server and fail over
automatically, and can reach it through a CDN edge instead of the origin:

- **[Backup server addresses + failover + load balancing](failover-load-balancing.md)** —
  a second IP, another port, or a CDN edge in front of the server.
- **CDN edge** (`Edge IP` at setup) — the client connects to the CDN, which
  forwards to the server, so the server's own IP is never exposed to the client
  side. WSS on a CDN-proxied port (443, 8443, 2053, …) is the combination that
  works through a CDN.

## The honest limits

- **Obfuscation cannot un-block an IP.** If an address is blocked at the IP layer
  (L3) — not by pattern — no transport makes it reachable. You need a different,
  clean IP, or a CDN edge in front of it.
- **A dirty exit is not a tunnel problem.** If the kharej IP is blacklisted by the
  services you reach through it, the tunnel still establishes; the fix is a
  cleaner exit IP, not a different transport.
- Backpack does **not** relay through a third hop, so a fully IP-blocked endpoint
  with no CDN option needs a clean address.

---
[← Back to the main README](../README.md)

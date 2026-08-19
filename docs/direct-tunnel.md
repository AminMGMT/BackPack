# Direct tunnel

The same forwarded ports Backpack has always served, with the tunnel dialled
the other way round.

In the **reverse** tunnel the Iran server listens and kharej dials in. That is
right when Iran can accept an inbound connection on the tunnel port. Where it
cannot — a provider that filters connections arriving from abroad, a port
blocked in one direction only — the tunnel never comes up, even though the
user-facing ports on Iran work perfectly well.

**Direct** turns it around. Iran dials out, which is the ordinary direction and
the one a filter is least likely to touch:

```
reverse:  users ──▶ [ IRAN: listens ] ◀── dials ── [ KHAREJ ] ──▶ service
direct:   users ──▶ [ IRAN: dials   ] ── dials ──▶ [ KHAREJ: listens ] ──▶ service
```

**The ports do not move.** Iran still exposes them, kharej still holds the real
service. Only who reaches out first has changed.

> Not to be confused with the [layer-3 direct tunnel](l3-direct-tunnel.md),
> which carries whole IP packets over a TUN interface. This one forwards ports,
> exactly like the reverse tunnel.

---

## Setting one up

**From the menu — the easy way.** Run `sudo backpack` and choose **Setup Iran** or
**Setup Kharej** — whichever machine you are on. It asks which direction you
want, then what kind of tunnel and how it should travel, and writes the config
and starts the service for you. Run it on the kharej server first, copy the
token it shows you, then run it on the Iran server.

The rest of this page is what it writes, for anyone who would rather edit the
file directly.

Two files. They must agree on the token and the transport.

**Iran** — dials out, needs no inbound port of its own:

```toml
[direct]
role  = "iran"
addr  = "KHAREJ_IP:8443"
token = "USE_A_LONG_RANDOM_TOKEN"
ports = ["443", "2053-2060", "8080=80"]
```

**Kharej** — listens, so port 8443 must be open inbound:

```toml
[direct]
role  = "kharej"
addr  = "0.0.0.0:8443"
token = "USE_A_LONG_RANDOM_TOKEN"
```

Start both. Port 443 on Iran now reaches port 443 on kharej.

Note that **kharej needs no `ports`**. Every target arrives on the stream that
asks for it, so what is forwarded is a change to the Iran config alone.

---

## Options

| Key | Default | What it does |
|---|---|---|
| `role` | *required* | `iran` or `kharej`. Its absence is what tells Backpack there is no direct tunnel here |
| `addr` | *required* | Kharej's `host:port` on Iran; the bind address on kharej |
| `token` | *required* | The shared secret. Never sent on the wire |
| `transport` | `tcp` | `tcp`, `stealth`, `ws` or `wss` |
| `ports` | *required on Iran* | Forwarded mappings — same syntax as the reverse tunnel |
| `sessions` | `1` | How many mux sessions to keep open |
| `nodelay` | `false` | Disable Nagle on the tunnel connection |
| `keepalive_period` | none | TCP keepalive, in seconds |
| `dial_timeout` | `10` | Seconds to allow for a dial |
| `retry_interval` | `3` | Seconds before redialling a dropped session |
| `accept_udp` | `false` | Forward UDP as well as TCP on those ports |
| `server_name` | none | SNI and Host on `ws`/`wss` — the domain in front of a CDN |
| `tls_cert`, `tls_key` | generated | The kharej side's certificate for `wss` |
| `acme_domain`, `acme_email` | none | Let's Encrypt instead of a generated certificate |
| `mux_version`, `mux_framesize`, `mux_recievebuffer`, `mux_streambuffer` | auto | The same mux tuning the reverse transports take |

### Port mappings

Identical to the reverse tunnel, so a config moves across unchanged:

| Mapping | Effect |
|---|---|
| `443` | `:443` on Iran → `127.0.0.1:443` on kharej |
| `443=8443` | a different port at the far end |
| `443=10.0.0.5:8443` | an explicit host on the kharej network |
| `127.0.0.1:443=8443` | bind to one local address on Iran only |
| `10000-10009` | a range, each to the same port |
| `10000-10009=20000-20009` | a range, preserving the offset |
| `443=10.0.0.1:80\|10.0.0.2:80` | two backends, load-balanced |

A target with no host of its own means the **loopback of the kharej machine**,
which is where the real service almost always listens.

### Which transport

| | What is on the wire | Take it when |
|---|---|---|
| `tcp` | a plain stream | the payload is already TLS, and nothing is filtering |
| `stealth` | Noise — no handshake, no fingerprint, random-looking | inspection is the concern |
| `ws` | an HTTP upgrade, then the stream | something in the path wants to see HTTP |
| `wss` | the same upgrade inside TLS | you are going through a CDN, or want ordinary HTTPS on the wire |

All four carry exactly the same thing; only the way the connection opens
differs. Once it is open, `ws` and `wss` stop using websocket framing and use
the stream underneath — the upgrade is camouflage, not a data format.

**On `wss` and certificates.** The kharej side generates a certificate in
memory if none is configured, which is what a direct connection to an IP wants.
That is safe because TLS here is transport and camouflage, not the trust
anchor: the token is, proved inside the connection, over every transport alike.

Set `server_name` on the Iran side to send that name in SNI and the Host header
— which is how a CDN knows which origin a connection belongs to. Doing so also
**turns certificate verification on**, so pair it with `acme_domain` or a real
`tls_cert`/`tls_key` on the kharej side.

### UDP

`accept_udp = true` forwards UDP alongside TCP on the same mappings. Each
client gets a stream of its own, so two clients are never mixed together, and
datagram boundaries are preserved end to end — a datagram that goes in whole
comes out whole. A flow with no traffic in either direction for two minutes is
released.

### `sessions`

One mux session carries every connection, so one is enough for most traffic.
Raise it where a single long-lived connection is being shaped or throttled, or
where head-of-line blocking in the transport underneath starts to show. New
connections are spread across whichever sessions are live.

---

## How it works

The reverse tunnel needs a control channel, a pool of pre-dialled connections,
a nonce to tell a pool connection from a stranger's, and a signal protocol
between the two ends. **None of that exists here**, because of one property of
stream multiplexing: once a mux session is up, *either* end can open a stream
on it.

So Iran dials one session, and every user connection becomes a stream on it,
opened on demand. There is nothing to pool and nothing to signal.

**Authentication.** The token never travels. Each end draws a nonce and proves
it holds the token by returning an HMAC over both:

```
Iran   -> kharej   nonceI
kharej -> Iran     nonceK, HMAC(token, "origin" || nonceI || nonceK)
Iran   -> kharej   HMAC(token, "edge"   || nonceI || nonceK)
```

It is mutual, so Iran learns kharej holds the token before sending a proof of
its own; it cannot be replayed, because both ends contribute freshness; and the
two directions use different labels, so a captured proof is not valid in the
other direction. A peer without the token gets no reply at all.

**Reconnection.** If the session drops, Iran redials on its own. The forwarded
ports stay bound throughout — a port that vanished on every reconnect would be
worse than one that accepts and refuses — and while no session is up, a
connection is refused promptly rather than left hanging.

---

## Limits

- **KCP and QUIC are reverse-only.** The four transports above are what a
  direct tunnel has.

## It cannot disturb a reverse tunnel

The direct tunnel lives in its own `[direct]` table and its own package. A
configuration that does not mention `[direct]` cannot reach any of this code,
and one that does never reaches the reverse engine. They share no keys, no
sockets and no state.

If something here misbehaves, delete the `[direct]` file. Nothing else changes.

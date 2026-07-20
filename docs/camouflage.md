# Decoy site (WSS camouflage)

A WSS tunnel is meant to be indistinguishable from an ordinary HTTPS website —
same port 443, a real domain, a valid certificate. But looking like a website
only to the tunnel's own client is not enough: **anything else that reaches the
server has to see a website too.** A browser that opens the domain, a scanner
sweeping the IP, or an active probe testing the port must all get a plausible
page — not a `401`, not a blank close, not anything that says "this is a tunnel."

So the WSS/WSS Mux server answers as a normal web server for every request that
is **not** a genuine tunnel connection.

## What counts as a genuine tunnel connection

All three must hold, or the request gets the decoy:

1. it is a **WebSocket upgrade**,
2. on a **tunnel path** (`/channel` or `/tunnel…`), and
3. it carries a **valid credential** (the token, or the session-bound proof over
   TLS — see [Transports → WSS](transports.md)).

A browser GET, a request on any other path, or a WebSocket upgrade with the
wrong token fails one of these and is served the decoy instead.

## What a probe sees

A plain, common **"Welcome to nginx!"** placeholder page, returned with
`200 OK` and a `Server: nginx` header — one of the most ordinary things on the
web, the kind a freshly set-up server serves. Nothing about it hints at a
tunnel. The real tunnel only ever answers a WebSocket upgrade, on its own path,
with the right credential.

## Why this matters against filtering

This is the difference between "the tunnel is encrypted" and "the tunnel is
invisible." Combined with the [Chrome TLS fingerprint](transports.md) on the
client and a Let's Encrypt certificate, the server is, to anyone probing it, a
normal HTTPS website — which is exactly what survives filtering that blocks the
unfamiliar. It is built in and always on for `wss` / `wssmux`; there is nothing
to configure.

> This does not replace picking a good transport. Where the connection itself is
> being filtered rather than fingerprinted, [TCP + Stealth](transports.md) — which
> looks like nothing at all — is the other tool.

---
[← Back to the main README](../README.md)

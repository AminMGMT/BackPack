# Real client IP (PROXY protocol v2)

The service behind the tunnel normally sees every connection as coming from the
tunnel itself — so a VPN panel counts all users as one device, and per-user
device limits stop working.

Turn on **Edit → Real client IP (PROXY protocol)** and Backpack prefixes each
forwarded connection with a PROXY protocol v2 header carrying the user's real IP
and port, so the backend sees each user's own address.

## Availability

Works on **TCP, TCP Mux, KCP, WS Mux and WSS Mux**. The plain WebSocket and raw
UDP transports have nowhere to put the header.

## Important

**The backend must be configured to accept PROXY Protocol v2 first.** If it is
not, it reads the header as ordinary traffic and every connection breaks. It is
**off by default** for exactly this reason — enable it on both sides together.

---
[← Back to the main README](../README.md)

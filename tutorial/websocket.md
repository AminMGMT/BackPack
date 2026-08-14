# Setting up a WS / WS Mux tunnel

Plain WebSocket. The tunnel is framed as ordinary HTTP traffic, which is what
gets it through a path that only passes HTTP — and what lets it sit behind a CDN.

**WS Mux** is the same thing with multiplexing over a connection pool, and it
supports the PROXY protocol (plain WS does not).

**The token travels in the clear on both.** That is fine behind TLS termination
you control; on an untrusted path use [WSS / WSS Mux](websocket-tls.md) instead.

> Read [TCP](tcp.md) for the parts of the wizard not covered here.

---

## The setup

The TCP walkthrough, with **`WebSocket` → `WS`** (or `WS Mux`) on both ends.
Firewall on the Iran server: the tunnel port and the forwarded ports on `tcp`.

There is one extra question, **on the client only**:

### `Edge IP` (optional)

Connect to a **CDN edge** instead of resolving the server address directly, so
the Iran server's own IP is never contacted by the client. Leave empty to skip.

Two conditions for it to work at all:

- the tunnel port must be one the CDN actually proxies — 80, 8080, 8880, 2052,
  2082, 2086, 2095 for plain HTTP on Cloudflare;
- the domain must be proxied by the CDN, not just hosted there.

Setup checks the address you enter and warns you if a raw transport is pointed at
a CDN, or if a domain's AAAA record would send the tunnel over IPv6.

---

## WS vs WS Mux

| | WS | WS Mux |
|---|---|---|
| PROXY protocol (real client IP) | ✗ | ✅ |
| Many short connections | fine | **better** |
| One heavy stream | **better** | fine |
| Pool starvation risk | none | shared pool |

Because WS Mux pools connections, keep
[UDP forwarding](udp-forwarding.md) off on it unless you genuinely need UDP —
browser QUIC arriving on a forwarded port is what drains that pool.

---

## Behind a CDN

The point of the WebSocket family is that a CDN will carry it. But note:

- **Plain WS through a CDN is unencrypted between you and the edge.** For a CDN
  setup you almost always want [WSS](websocket-tls.md), which is also what
  Cloudflare's proxied ports expect.
- The CDN terminates the connection, so if a reverse proxy (NGINX and the like)
  sits in front, see **simple auth** on the
  [WSS page](websocket-tls.md#simple-auth-for-a-tls-terminating-proxy) — the same
  applies here when something terminates in front of the tunnel.

---

<div dir="rtl">

## خلاصهٔ فارسی

**WS** تونل را به شکل ترافیک HTTP معمولی درمی‌آورد — مناسب مسیری که فقط HTTP رد
می‌کند، و لازمهٔ نشستن پشت CDN. **WS Mux** همان است با مالتی‌پلکس و پشتیبانی از
PROXY protocol.

**توکن روی هر دو به‌صورت خام رد می‌شود.** روی مسیر نامطمئن به‌جایش
[WSS](websocket-tls.md) را بگیر.

راه‌اندازی مثل [TCP](tcp.md) با انتخاب `WebSocket` → `WS` یا `WS Mux` در دو طرف.
فقط **روی کلاینت** یک سؤال اضافه دارد: **Edge IP** — اگر می‌خواهی کلاینت به‌جای
آی‌پی سرور ایران به یک لبهٔ CDN وصل شود. پورت تونل باید از پورت‌هایی باشد که CDN
پروکسی می‌کند و دامنه هم باید واقعاً proxied باشد.

چون WS Mux از pool اتصال استفاده می‌کند، تا وقتی واقعاً UDP لازم نداری
[UDP forwarding](udp-forwarding.md) را روشن نکن.

</div>

---
[← Back to the tutorials](README.md)

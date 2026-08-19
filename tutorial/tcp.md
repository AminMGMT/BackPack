# Setting up a TCP tunnel

The plain TCP transport: one reliable stream, no encryption of its own, the
lowest overhead of anything here. **Start with this one.** If it works, you are
done; if it connects and then misbehaves, the other tutorials tell you what to
switch to and why.

This page walks the wizard **question by question**. The other transport pages
assume you have read it and only cover what differs.

> First time? [Before you start](before-you-start.md) — roles, token, ports,
> firewall.

**Good for:** a clean route, maximum throughput, minimum CPU.
**Not for:** a DPI-filtered link (the token travels in the clear and the flow is
an ordinary TCP flow) — use [TCP + Stealth](tcp-stealth.md) there.

---

## Part 1 — the Iran server

```bash
sudo backpack
```
Choose **1) Setup Iran**, then **Reverse**.

### Select transport family → `TCP`
### Select TCP transport → `TCP`

### `Tunnel (control) port:`
The port the kharej client will dial. Anything free — `8443`, `2087`, `9000`.
It is not what your users connect to, and it does not have to look like
anything. Setup refuses a port already in use.

### `Listen on IPv6 as well [y/N]`
`N` unless you know you need it. Yes binds `::`, which on a normal dual-stack
host accepts IPv4 too — it is "IPv6 as well", not "IPv6 instead".

### `Tunnel name [server-8443]`
Cosmetic. It names the systemd service (`backpack-<name>`) and the config file.
Press Enter.

### `Security token`
A 64-character token is generated and printed. **Copy it now** — the client
needs the identical string. Press Enter to accept it.

### `Exposed ports (comma separated…)`
What your users connect to on the Iran IP. `443` alone means the kharej server
hands it to its own `127.0.0.1:443`; write `443=127.0.0.1:2096` if the service
listens elsewhere there. See [the mapping table](before-you-start.md#3-the-ports--and-what-a-mapping-means).

Setup then prints the resolved list — **read it**, this is where a wrong mapping
becomes obvious:

```
On the KHAREJ server, these must be listening:
  443  →  127.0.0.1:2096
```

### `Carry UDP as well as TCP on the exposed ports [y/N]`
`N` for a web or proxy tunnel. `y` for Xray/3x-ui UDP, WireGuard, DNS or games.
Details: [Adding UDP to a tunnel](udp-forwarding.md).

### `Enable PROXY protocol (send real client IP) [y/N]`
`N` unless the service behind the tunnel is already configured to **accept** the
PROXY protocol (in X-UI/Marzban: the inbound option *Accept Proxy Protocol*).
Turning it on without that breaks every connection.
See [real client IP](../docs/real-client-ip.md).

### `Performance preset:`
**Turbo** — the recommended default. [What each one does](../docs/performance-presets.md).

### `Fine-tune the advanced settings by hand [y/N]`
`N`. The preset has already filled in every value. If you do say yes, every
question is documented in the [CLI menu reference](../docs/cli-menu.md#the-advanced-settings-fine-tune).

The tunnel is created, started and verified. Now open the firewall:

```bash
ufw allow 8443/tcp      # the tunnel port
ufw allow 443/tcp       # each forwarded port
```

---

## Part 2 — the kharej server

```bash
sudo backpack
```
Choose **2) Setup Kharej**, then **Reverse**.

### Select transport family → `TCP` → `TCP`
Must match the server exactly.

### `Server address (IP or domain of the server):`
The **Iran** server's IP (or a domain pointing at it). Setup resolves a domain
and warns if it lands on a CDN, or if an AAAA record would send the tunnel over
IPv6 — the usual reason a bare IP works where its own domain does not.

### `Server tunnel port:`
The same tunnel port you chose on the Iran side (`8443` here).

### `Tunnel name [client-8443]`
Press Enter.

### `Security token [backpack]`
Paste the **exact** token from the server. This is the field people get wrong.

### `Configure optional connection settings… [y/N]`
`N` for a normal setup. Behind it: an outbound SOCKS5/HTTP proxy, pinning the
tunnel to one interface or source address, and **backup server addresses** with
failover or load balancing. See
[failover & load balancing](../docs/failover-load-balancing.md).

### `Performance preset:` → the same one as the server.
### `Fine-tune the advanced settings by hand [y/N]` → `N`.

---

## Part 3 — check it

On either machine:

```
sudo backpack  →  3. Manage  →  Status          # live table, both ends
sudo backpack  →  3. Manage  →  Health Check    # finds problems, prints the fix
```

Then connect to `IRAN_IP:443` the way a user would. If the tunnel is running but
the port refuses, the service is not listening where the mapping says it is —
check on the kharej machine with `ss -tlnp | grep 2096`.

---

## Tuning worth knowing about

- **Zero-copy** (`Edit → …` / fine-tune, plain `tcp` only, Linux, no bandwidth
  limit): hands forwarded traffic straight to the kernel. Fastest path here and
  the least proven — try it on a spare tunnel first. Purely local, so the two
  ends need not agree.
- **MSS clamp**: leave at 0. If big transfers stall while the tunnel looks
  healthy, run **Health Check** — it measures the path MTU and prints the exact
  number. [More](../docs/mss-clamp.md).
- **Limits**: cap simultaneous connections and Mbit/s per tunnel under
  **Edit → Limits**. [More](../docs/limits.md).

## If TCP is not working out

| What you see | Go to |
|---|---|
| Connects, then dies or is throttled for no reason | [TCP + PCK](tcp-pck.md) |
| Never connects from Iran, or dies under DPI | [TCP + Stealth](tcp-stealth.md) |
| Many short connections, high latency per request | [TCP Mux](tcp-mux.md) |
| Lossy route, gaming, ping spikes | [UDP + KCP + FEC](udp-kcp-fec.md) |
| Only HTTP/HTTPS gets out | [WS](websocket.md) / [WSS](websocket-tls.md) |

Switching later keeps the token, ports and name: **Manage → Edit → Change
transport**, on both ends.

---

<div dir="rtl">

## خلاصهٔ فارسی

ترنسپورت **TCP** ساده‌ترین و سبک‌ترین گزینه است و نقطهٔ شروع درست. اگر مسیر تمیز
باشد، همین بهترین کارایی را می‌دهد.

**روی سرور ایران:** `sudo backpack` → گزینهٔ ۱ (Setup Iran) → Reverse → خانوادهٔ TCP →
TCP → پورت تونل (مثلاً 8443) → IPv6 را `N` → نام را Enter → **توکن را کپی کن** →
پورت‌های forward (مثلاً `443` یا `443=127.0.0.1:2096`) → سؤال UDP (برای وب `N`،
برای Xray/وایرگارد `y`) → PROXY protocol را `N` بگذار مگر پنل تنظیمش کرده باشی →
پریست **Turbo** → تنظیمات پیشرفته `N`.

بعد فایروال ایران: `ufw allow 8443/tcp` (پورت تونل) و `ufw allow 443/tcp` (هر
پورت forward شده).

**روی سرور خارج:** `sudo backpack` → گزینهٔ ۲ (Setup Kharej) → Reverse → همان ترنسپورت →
آی‌پی ایران + همان پورت تونل → نام → **همان توکن** → تنظیمات اختیاری `N` → همان
پریست.

بعد با `Manage → Status` و `Manage → Health Check` چک کن. اگر تونل بالاست ولی
پورت جواب نمی‌دهد، سرویس روی سرور خارج جای درستی گوش نمی‌دهد —
با `ss -tlnp` ببین.

اگر TCP مشکل داشت: قطع‌وصل و throttle → [PCK](tcp-pck.md)، فیلترینگ سنگین →
[Stealth](tcp-stealth.md)، بازی و مسیر پرافت → [KCP](udp-kcp-fec.md).

</div>

---
[← Back to the tutorials](README.md)

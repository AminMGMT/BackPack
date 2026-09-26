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

### Select Transport Family → `TCP`
### Select TCP Transport → `TCP`

### `Iran IP Or Domain (What Kharej Dials) [detected]`
This server's address as the kharej will reach it. The detected public IP is
the default — press Enter, or give a domain. The setup link carries it.

### `Tunnel Port:`
The port the kharej client will dial. Anything free — `8443`, `2087`, `9000`.
It is not what your users connect to. `85.10.11.51:8443` pins it to one address.
Setup refuses a port already in use.

### `Listen On IPv6 As Well (y/N)`
`N` unless you know you need it. Yes binds `::`, which on a normal dual-stack
host accepts IPv4 too.

### `Forwarded Ports (e.g. 443, 8080=127.0.0.1:2096):`
What your users connect to on the Iran IP. `443` alone means the kharej server
hands it to its own `127.0.0.1:443`; write `443=127.0.0.1:2096` if the service
listens elsewhere there. The summary shows where each one lands
(**Kharej Serves**). See [the mapping table](before-you-start.md#3-the-ports--and-what-a-mapping-means).

### `Tunnel Name [server-8443]`
Names the systemd service (`backpack-<name>`) and the config file. Enter.

### `Security Token [generated]`
Press Enter. The setup link carries it to kharej — nothing to copy by hand.

### `Carry UDP As Well As TCP On Those Ports (y/N)`
`N` for a web or proxy tunnel. `y` for Xray/3x-ui UDP, WireGuard, DNS or games.
Details: [Adding UDP to a tunnel](udp-forwarding.md).

### `Send Real Client IP (PROXY Protocol — The Service Must Accept It) (y/N)`
`N` unless the service behind the tunnel **accepts** the PROXY protocol (X-UI /
Marzban: *Accept Proxy Protocol*). On without that, every connection breaks.
See [real client IP](../docs/real-client-ip.md).

### `How Should The Tunnel Be Tuned?`
**Turbo** — the recommended default. [What each one does](../docs/performance-presets.md).

### `Fine-Tune The Advanced Settings (y/N)`
`N`. The preset has already filled in every value.

Then one short summary, with the **Setup Link** under it:

```
Reverse TCP

Listens On      : 0.0.0.0:8443
Kharej Dials    : 203.0.113.9:8443
Forwarded Ports : 443=2096
Kharej Serves   : 443 → 127.0.0.1:2096
Tuning          : Turbo
Config File     : /etc/backpack/server-8443.toml

Setup Link (Setup Kharej → Reverse → The Same Transport → Setup Link) :
backpack://1.H4sI…
```

**Create This Tunnel** → the tunnel is created and started. Copy the link, and
open the firewall:

```bash
ufw allow 8443/tcp      # the tunnel port
ufw allow 443/tcp       # each forwarded port
```

---

## Part 2 — the kharej server

```bash
sudo backpack
```
Choose **2) Setup Kharej**, then **Reverse**, then **the same transport**
(`TCP` → `TCP`).

### `How Do You Want To Set Up This Side?` → **Setup Link**
Paste the line the Iran server printed, press Enter for the name, and
**Create This Tunnel**. The address, port, token, preset and every paired
setting come from the link.

**Manual** instead asks `Iran IP Or Domain`, `Tunnel Port`, `Tunnel Name`,
`Security Token (From The Iran Server)` (no default — paste Iran's), the
optional connection settings (proxy, interface, **backup addresses** with
failover or load balancing — see [failover & load balancing](../docs/failover-load-balancing.md)),
the preset — the same one as Iran — and fine-tune. A domain is resolved and
checked: a CDN in front of it, or an AAAA record that would send the tunnel
over IPv6, is warned about.

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
TCP → آی‌پی یا دامنهٔ ایران (پیش‌فرض را Enter بزن) → پورت تونل (مثلاً 8443) → IPv6
را `N` → پورت‌های forward (مثلاً `443` یا `443=127.0.0.1:2096`) → نام را Enter →
توکن را Enter → سؤال UDP (برای وب `N`، برای Xray/وایرگارد `y`) → PROXY protocol
را `N` بگذار مگر پنل تنظیمش کرده باشی → پریست **Turbo** → تنظیمات پیشرفته `N`. در
خلاصه یک **Setup Link** (`backpack://…`) نشان داده می‌شود؛ کپی‌اش کن.

بعد فایروال ایران: `ufw allow 8443/tcp` (پورت تونل) و `ufw allow 443/tcp` (هر
پورت forward شده).

**روی سرور خارج:** `sudo backpack` → گزینهٔ ۲ (Setup Kharej) → Reverse → همان ترنسپورت →
**Setup Link** → لینک را پیست کن → نام را Enter → Create. (با **Manual** هم می‌شود:
آی‌پی ایران، همان پورت تونل، نام، همان توکن، همان پریست.)

بعد با `Manage → Status` و `Manage → Health Check` چک کن. اگر تونل بالاست ولی
پورت جواب نمی‌دهد، سرویس روی سرور خارج جای درستی گوش نمی‌دهد —
با `ss -tlnp` ببین.

اگر TCP مشکل داشت: قطع‌وصل و throttle → [PCK](tcp-pck.md)، فیلترینگ سنگین →
[Stealth](tcp-stealth.md)، بازی و مسیر پرافت → [KCP](udp-kcp-fec.md).

</div>

---
[← Back to the tutorials](README.md)

# Setting up a TCP Mux tunnel

The same TCP stream as [plain TCP](tcp.md), except many logical connections are
**multiplexed** over a small pool of real ones (via smux). A service that opens a
fresh connection per request stops paying for a handshake every time.

**Good for:** panels and web backends, anything with many short-lived
connections, and links where opening new connections is slow.
**Watch out for:** the connection pool is shared. A few long-lived flows can hold
it and starve everything else — which is exactly why
[UDP forwarding is off by default](udp-forwarding.md).

> Read [TCP](tcp.md) first. Only the differences are below.

---

## The setup

Identical to the TCP walkthrough, with one change:

**Select transport family → `TCP`, Select TCP transport → `TCP Mux`**

on **both** ends. Everything else — tunnel port, token, forwarded ports, UDP
question, preset — is answered exactly the same way.

Firewall on the Iran server: the tunnel port and the forwarded ports on `tcp`.

---

## The mux settings

They come from the preset and rarely need touching. They appear under
**Fine-tune the advanced settings by hand** at setup, and per-tunnel afterwards
in the panel's Fine Tune drawer:

| Setting | What it is |
|---|---|
| **Mux connections/sessions** | how many real TCP connections carry the multiplexed streams. More = more parallelism, more sockets. |
| **Mux version** | smux protocol version, 1 or 2. Leave it. |
| **Mux frame size** | the largest chunk written per frame. |
| **Mux receive buffer** | per-session receive window — the ceiling on in-flight data for the whole session. |
| **Mux stream buffer** | per-stream window. This is what flow-controls one heavy download so it does not consume the session. |

**Both ends should carry the same preset.** Mismatched windows do not break the
tunnel, but the smaller side decides the throughput.

If you change these by hand the tunnel is marked **Custom**, and a later preset
change will not silently overwrite your answers.

---

## Choosing between TCP and TCP Mux

| | TCP | TCP Mux |
|---|---|---|
| One heavy stream (download, big transfer) | **better** | fine |
| Many small requests (panel, web app, API) | fine | **better** |
| Head-of-line blocking risk | none | shared pool — one stalled flow can affect others |
| CPU | lowest | slightly more |

If you are unsure, run both and watch **Manage → Tunnel Metrics**.

## Common issue: "some sites half-load"

Images stall while audio keeps playing, and a restart fixes it for a while. That
is the connection pool being drained by long-lived flows — almost always browser
QUIC arriving on a forwarded port with UDP forwarding switched on. Turn it off
(**Manage → Edit → Forward UDP**) unless the tunnel genuinely carries UDP.

---

<div dir="rtl">

## خلاصهٔ فارسی

**TCP Mux** همان TCP است، با این تفاوت که چند اتصال منطقی روی تعداد کمی اتصال
واقعی سوار می‌شوند. برای پنل‌ها و سرویس‌هایی که مدام اتصال کوتاه باز می‌کنند
بهتر است؛ برای یک دانلود سنگین تک‌جریانی، TCP ساده بهتر است.

راه‌اندازی دقیقاً مثل [TCP](tcp.md) است، فقط در منوی ترنسپورت به‌جای TCP گزینهٔ
**TCP Mux** را بزن — روی **هر دو** سرور.

تنظیمات mux (تعداد کانکشن، نسخه، اندازهٔ فریم، بافر session و stream) از پریست
می‌آید و معمولاً نیازی به دست زدن ندارد؛ فقط پریست دو طرف را یکی نگه دار.

**مشکل رایج:** بعضی سایت‌ها نصفه لود می‌شوند و ری‌استارت موقتاً درستش می‌کند —
یعنی pool اتصال‌ها پر شده، معمولاً به‌خاطر QUIC مرورگر که وقتی UDP forwarding
روشن باشد وارد تونل می‌شود. اگر واقعاً به UDP نیاز نداری خاموشش کن:
`Manage → Edit → Forward UDP`.

</div>

---
[← Back to the tutorials](README.md)

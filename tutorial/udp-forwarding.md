# Adding UDP to a tunnel

**"TCP works, UDP does not."** This page is the fix. It applies to every
transport — you do not need to change transport to carry UDP.

A forwarded port carries **TCP only** until you turn UDP on. Expose `443` and the
tunnel carries 443/tcp; turn UDP on and it also listens on 443/udp, relays both
to the kharej machine, and sends the replies back to whoever sent them.

---

## Do you need it?

**Yes, if the service behind the tunnel uses UDP:**

| Service | What needs UDP |
|---|---|
| Xray / 3x-ui | VLESS/VMess with a UDP outbound, XUDP, QUIC-based inbounds |
| Shadowsocks | the UDP relay — which is what carries DNS and most games |
| WireGuard | everything. It is UDP and nothing else |
| DNS | every query |
| Games, voice | almost all of it |

**No, for a plain web or proxy tunnel.** A browser's QUIC is UDP on port 443, so
leaving this on funnels every QUIC flow the browser opens into the tunnel. On the
pooled transports (`ws`, `wss`, TCP Mux, WS Mux, WSS Mux) each of those long-lived
flows holds a pooled connection for as long as the browser keeps it, starving the
TCP forwards that share the pool. The symptom is a site half-loading — images
stalled while audio plays — that a restart fixes for a while. That is what
leaving it off prevents.

---

## Turning it on

### At setup

The server wizard asks, right after the exposed ports:

```
Carry UDP as well as TCP on the exposed ports [y/N]
```

### On a tunnel that already exists

```
sudo backpack  →  3. Manage  →  Manage Tunnels  →  <tunnel>  →  Edit
                →  Forward UDP
```

It shows the current state, explains the cost, and restarts the tunnel on
confirm.

### In the web panel

Edit the **server** tunnel → Fine Tune → **Forward UDP as well as TCP on the
exposed ports** → Save.

### By hand

In `/etc/backpack/<name>.toml`, under `[server]`:

```toml
accept_udp = true
```

then restart the tunnel.

**Only the server (Iran) side has this setting.** The client needs no change.

---

## Then open the firewall — both protocols

A rule opened for TCP is **not** opened for UDP. This is the step people miss:

```bash
ufw allow 443/tcp
ufw allow 443/udp
```

And the reverse trap: **opening `443/udp` without turning the setting on does
nothing.** Both are required.

---

## Checking it

On the Iran server, the forwarded port should now be bound on both protocols:

```bash
ss -lnup | grep :443      # UDP  — should show backpack
ss -lntp | grep :443      # TCP
```

If the UDP line is missing, the setting is not on. If the UDP side of a port
cannot be bound because something else already holds it, Backpack warns and
leaves the TCP side working rather than failing the whole tunnel — check the log:

```bash
journalctl -u backpack-<name> -n 50
```

---

## Why the default changed, and why an upgraded server behaves differently

v1.7.1 made UDP forwarding the default. v1.7.2 reverted it to opt-in, for the
QUIC reason above.

The value is written into each tunnel's config file, and **an upgrade does not
rewrite existing configs**. So a tunnel created on v1.7.1 still has
`accept_udp = true` and keeps forwarding UDP, while a tunnel created fresh on
v1.7.2 does not. Same binary, different config — that is the whole difference.

Full background: [docs/forwarded-udp.md](../docs/forwarded-udp.md).

---

<div dir="rtl">

## خلاصهٔ فارسی

اگر «TCP کار می‌کند ولی UDP رد نمی‌شود»، مشکل همین‌جاست. هر پورت forward شده
به‌صورت پیش‌فرض **فقط TCP** را عبور می‌دهد. این تنظیم روی **همهٔ** ترنسپورت‌ها
هست و لازم نیست ترنسپورت را عوض کنی.

**چه‌وقت لازم است:** Xray/3x-ui با خروجی UDP یا XUDP، رلهٔ UDP شدوساکس،
وایرگارد (که کلاً UDP است)، DNS، بازی و ویس.

**چه‌وقت لازم نیست:** تونل وب یا پروکسی ساده. QUIC مرورگر روی UDP/443 است و اگر
روشن باشد همهٔ آن جریان‌ها وارد تونل می‌شوند؛ روی ترنسپورت‌های pool‌دار
(ws/wss و خانوادهٔ mux) هر جریان یک اتصال از pool را نگه می‌دارد و forward‌های
TCP گرسنه می‌مانند — همان حالتی که سایت نصفه لود می‌شود و ری‌استارت موقتاً
درستش می‌کند.

**روشن کردنش:** سر نصب، سؤال «Carry UDP as well as TCP…» را `y` بزن. روی تونل
موجود: `Manage → Manage Tunnels → <تونل> → Edit → Forward UDP`. یا دستی در
`/etc/backpack/<name>.toml` زیر `[server]` بنویس `accept_udp = true` و ری‌استارت
کن. **فقط سمت سرور (ایران) این تنظیم را دارد.**

**بعدش فایروال:** هم `ufw allow 443/tcp` هم `ufw allow 443/udp`. باز کردن پورت
UDP بدون روشن کردن این گزینه هیچ اثری ندارد و برعکسش هم همین‌طور.

**چرا سرور آپگرید‌شده فرق دارد؟** در نسخهٔ ۱.۷.۱ پیش‌فرض روشن بود و در ۱.۷.۲ به
حالت اختیاری برگشت. مقدار داخل فایل کانفیگ هر تونل نوشته می‌شود و آپگرید
کانفیگ‌های موجود را بازنویسی نمی‌کند — پس تونل ساخته‌شده با ۱.۷.۱ همچنان
`accept_udp = true` دارد و کار می‌کند، ولی نصب تازهٔ ۱.۷.۲ ندارد.

</div>

---
[← Back to the tutorials](README.md)

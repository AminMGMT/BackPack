# Behind a panel (X-UI / 3x-ui / Marzban)

The most common Backpack deployment: a VPN panel on the kharej server, users
connecting to the Iran IP. This page covers the four things that are specific to
that setup.

Assumes you have a working tunnel — build one first with any transport tutorial,
e.g. [TCP](tcp.md) or [TCP Mux](tcp-mux.md).

```
users ──▶ IRAN:443 ══ tunnel ══▶ KHAREJ ──▶ 127.0.0.1:2096 (X-UI inbound)
```

---

## 1. Map the port to where the panel actually listens

On the **kharej** server, find the inbound's real port:

```bash
ss -tlnp | grep xray
```

Then, on the **Iran** server, map the exposed port to it:

| Panel inbound on kharej | What to enter as the forwarded port |
|---|---|
| `127.0.0.1:443` | `443` |
| `127.0.0.1:2096` | `443=127.0.0.1:2096` |
| `0.0.0.0:2096` (bound to all) | `443=127.0.0.1:2096` still works |
| bound to the **public** IP only | `443=<that public IP>:2096` |

A panel bound to a public IP instead of `127.0.0.1` will refuse the tunnel's
connection — that is the "tunnel is up, port refuses" case.

Change it later with **Manage → Edit → Change forwarded ports** (enter the full
new list).

### Several inbounds, one tunnel

```
443=127.0.0.1:2096, 8443=127.0.0.1:2097, 2053=127.0.0.1:2098
```

### Two panel backends, balanced

```
443=127.0.0.1:2096|127.0.0.1:2097
```

Both are health-checked continuously and traffic is balanced over the live ones.

---

## 2. Turn on UDP if the inbound uses it

VLESS/VMess with a UDP outbound, XUDP, a Shadowsocks UDP relay, or a QUIC-based
inbound all need it, and it is **off by default**:

**Manage → Edit → Forward UDP**, then `ufw allow 443/udp` as well as
`ufw allow 443/tcp`. Full page: [Adding UDP to a tunnel](udp-forwarding.md).

If your inbound is plain VLESS+TCP or VMess+WS, leave it off.

---

## 3. Real client IP, so device limits work

Without it, the panel sees every user arriving from the tunnel's own address, so
per-user IP/device limits count all your users as one device.

**Order matters — the panel first:**

1. In X-UI / 3x-ui / Marzban, enable the inbound option **Accept Proxy
   Protocol**.
2. Then on the Iran server: **Manage → Edit → Real client IP**.

Doing it the other way round breaks every connection in between: the panel reads
the PROXY protocol v2 header as traffic.

Not available on the raw `udp` and `ws` transports. More:
[real client IP](../docs/real-client-ip.md).

---

## 4. Do not tunnel the panel's admin port

Expose the **inbound** ports, not the panel's web UI. If you need the admin
interface remotely, reach it over SSH port-forwarding rather than putting it on a
public forwarded port.

---

## Sanity check

```bash
# on kharej — the inbound is listening where you mapped it
ss -tlnp | grep 2096

# on Iran — Backpack holds the exposed port
ss -tlnp | grep :443
ss -lnup  | grep :443        # only if UDP forwarding is on

sudo backpack → Manage → Health Check
```

Then add the **Iran IP** and the exposed port to the client config — users never
touch the kharej address.

---

<div dir="rtl">

## خلاصهٔ فارسی

رایج‌ترین حالت استفاده: پنل روی سرور خارج، کاربر به آی‌پی ایران وصل می‌شود.
چهار نکتهٔ مخصوص این حالت:

**۱. نگاشت پورت.** روی سرور خارج با `ss -tlnp | grep xray` ببین inbound روی چه
پورتی گوش می‌دهد، بعد روی ایران همان را بنویس: اگر روی `127.0.0.1:2096` است
باید بنویسی `443=127.0.0.1:2096`، نه فقط `443`. اگر پنل روی آی‌پی عمومی bind شده
باشد اتصال تونل را رد می‌کند — همان حالت «تونل بالاست ولی پورت جواب نمی‌دهد».
چند inbound را با کاما جدا کن و دو backend را با `|` بنویس تا بین زنده‌ها بالانس
شود.

**۲. UDP.** اگر inbound تو UDP لازم دارد (خروجی UDP، XUDP، رلهٔ UDP شدوساکس،
inbound مبتنی بر QUIC) از `Manage → Edit → Forward UDP` روشنش کن و `ufw allow
443/udp` را هم بزن. پیش‌فرض خاموش است.

**۳. آی‌پی واقعی کاربر.** بدون آن پنل همهٔ کاربران را یک دستگاه می‌بیند و
محدودیت تعداد کاربر کار نمی‌کند. **اول** در پنل گزینهٔ «Accept Proxy Protocol»
را روشن کن، **بعد** در بک‌پک `Manage → Edit → Real client IP`. برعکسش همهٔ
اتصال‌ها را خراب می‌کند.

**۴. پورت پنل ادمین را تونل نکن** — فقط پورت‌های inbound را در معرض بگذار.

</div>

---
[← Back to the tutorials](README.md)

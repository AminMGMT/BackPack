# Setting up a WSS / WSS Mux tunnel

WebSocket over **TLS** — the transport that makes the Iran server look like an
ordinary HTTPS website. **WSS Mux** adds multiplexing and the PROXY protocol.

Three things make it more than "WS with TLS":

- **A real Chrome TLS fingerprint.** Go's default ClientHello is itself
  identifiable; Backpack sends a current Chrome one, so the handshake blends into
  normal browser traffic.
- **A session-bound credential.** The token is never sent. Each side derives
  keying material from the TLS session and the client proves it holds the token
  with an HMAC over that material — a man in the middle has a different session
  and cannot replay it.
- **A decoy site.** Anything that is not a genuine tunnel connection — a browser,
  a scanner, a probe with the wrong token — gets an ordinary "Welcome to nginx!"
  page. Built in, always on. [More](../docs/camouflage.md).

**Reach for it when:** you want a CDN in front, or where unidentifiable traffic
would itself be suspicious. (Where it would not be, [Stealth](tcp-stealth.md) is
lighter.)

> Read [TCP](tcp.md) for the parts of the wizard not covered here.

---

## The setup — Iran server

**`WebSocket` → `WSS`** (or `WSS Mux`). The wizard adds a certificate screen
after the forwarded ports.

### `TLS certificate:`

| Choice | When |
|---|---|
| **Self-signed, generated now** | **the default.** Works anywhere, including on a bare IP. |
| **Let's Encrypt, automatic** | you have a domain pointing at this server, and you want the connection to look completely ordinary |
| **Use existing certificate/key files** | you already have a cert on disk |

A self-signed certificate **encrypts exactly as well** — the client is Backpack's
own code and does not verify it. The reason to get a real one is how the
connection *looks*: real HTTPS on port 443 is never self-signed, so a self-signed
cert is a distinguishing mark. A CDN in front also requires a real one.

Choosing self-signed asks for an optional domain or IP to embed in the cert.

**Let's Encrypt requires all three of these**, and setup checks them:

- a domain whose A record points at **this** server's IP;
- port 80 reachable from outside, **or** this tunnel on port 443;
- this server able to reach `acme-v02.api.letsencrypt.org`.

The certificate is requested on the first connection, which takes a few seconds.
If it does not appear: `journalctl -u backpack-<name> -n 50`.

Change any of this later with **Manage → Edit → Certificate**.

### Simple auth (for a TLS-terminating proxy)

Asked on **both** ends:

```
Use simple token auth (for a TLS-terminating proxy in front) [y/N]
```

`N` normally. Say yes only when a reverse proxy — NGINX and the like —
terminates TLS in front of the tunnel. That proxy holds a different TLS session
from the client, so the session-bound proof can never match and the tunnel is
rejected. Simple auth sends the raw token instead, which works through such a
proxy — and hands the token to whatever terminates the TLS. **Set the same answer
on both ends.**

---

## The setup — kharej client

Same transport, the Iran address and tunnel port, the same token, and:

### `Edge IP` (optional)

Connect through a **CDN edge** rather than the server address directly, so the
client never contacts the Iran IP. For Cloudflare the tunnel port must be one of
the proxied HTTPS ports — 443, 2053, 2083, 2087, 2096, 8443 — and the domain must
be proxied, not merely hosted.

Setup resolves whatever address you type and warns you if it lands on a CDN with
a raw transport, or if an AAAA record would send the tunnel over IPv6.

---

## Firewall

Iran server: the tunnel port and the forwarded ports on `tcp`. If you chose
Let's Encrypt with the port-80 challenge, 80/tcp too.

---

## Checking the camouflage

Open `https://IRAN_IP:<tunnel port>/` in a browser. You should get the nginx
welcome page, not an error and not a hint of a tunnel. That is the decoy
answering, and it is what a scanner sees.

---

<div dir="rtl">

## خلاصهٔ فارسی

**WSS** یعنی WebSocket روی TLS — ترنسپورتی که سرور ایران را شبیه یک سایت HTTPS
معمولی می‌کند. **WSS Mux** نسخهٔ مالتی‌پلکس‌شده با پشتیبانی PROXY protocol است.

سه چیز آن را از «WS + TLS» جدا می‌کند: **fingerprint واقعی کروم**، **توکنی که
اصلاً فرستاده نمی‌شود** (به‌جایش با HMAC روی session اثبات می‌شود)، و **سایت
تقلبی** که به هر کاوشگر و مرورگری صفحهٔ «Welcome to nginx» نشان می‌دهد.

سر راه‌اندازی روی ایران یک صفحهٔ **گواهی** اضافه می‌شود: *self-signed* (پیش‌فرض،
همه‌جا کار می‌کند و رمزنگاری‌اش دقیقاً به همان خوبی است)، *Let's Encrypt* (نیاز
به دامنه‌ای که به همین سرور اشاره کند + پورت ۸۰ باز یا تونل روی ۴۴۳)، یا فایل
گواهی موجود. دلیل گرفتن گواهی واقعی فقط ظاهر کار است، نه امنیت.

**Simple auth** را فقط وقتی `y` بزن که یک ریورس‌پروکسی (مثل NGINX) جلوی تونل TLS
را terminate می‌کند؛ در این حالت توکن خام فرستاده می‌شود و باید دو طرف یکی باشد.

روی کلاینت می‌توانی **Edge IP** بدهی تا از لبهٔ CDN وصل شود (پورت تونل باید جزو
پورت‌های proxied کلادفلر باشد و دامنه هم proxied باشد).

برای تست استتار، در مرورگر `https://IRAN_IP:<پورت تونل>/` را باز کن — باید صفحهٔ
nginx بیاید.

</div>

---
[← Back to the tutorials](README.md)

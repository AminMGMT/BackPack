# Setting up a TCP + Stealth tunnel

A TCP tunnel wrapped in a **Noise (NNpsk0) record layer**. On the wire it is two
short bursts of what looks like random bytes, followed by an encrypted stream
that looks the same — no TLS ClientHello, no recognisable protocol header,
nothing for deep packet inspection to match on.

**Reach for it when:** the link is DPI-filtered, plain TCP connects and then dies
under load, or the tunnel keeps being killed after a while. This is the transport
that brought a filtered Germany server back online in the field.

**Cost:** a little CPU for the encryption. That is all.

> Read [TCP](tcp.md) first. Only the differences are below.

---

## Why it hides better than the alternatives

- **No fingerprint at all.** TLS-based camouflage tries to look like something
  specific; Stealth looks like *nothing*. There is no field to match, no version
  string, no handshake shape belonging to a known protocol.
- **The key comes from the token.** No separate key material to distribute, and
  the pre-shared key is mixed in from the very first message.
- **A wrong token gets no reply.** A peer without the token cannot complete the
  handshake, so the server answers with silence — a port scan finds a dead port,
  not a service. That is also why a token typo looks identical to a firewall
  problem.

---

## The setup

Exactly the TCP walkthrough, with:

**Select transport family → `TCP`, Select TCP transport → `TCP + Stealth`**

on **both** ends. No certificate, no domain, no extra questions. Tunnel port,
token, forwarded ports, UDP question and preset are all answered as usual.

Firewall on the Iran server: the tunnel port and the forwarded ports on `tcp`.

### Pick an unremarkable tunnel port

Stealth removes the fingerprint from the *content*; the port is still a hint. A
long-lived flow on 8443 or 2087 draws less attention than one on 1194 or 51820.

---

## Verifying it is really up

```
sudo backpack  →  3. Manage  →  Status
```

If the client says it is connecting and the server shows nothing:

1. **Token.** Compare byte for byte — this is the one failure Stealth gives you
   no error message for.
2. **Transport.** Both sides must say `TCP + Stealth`; a peer on plain TCP is
   just noise to a Stealth listener.
3. **Firewall.** `ufw allow <tunnel port>/tcp` on Iran.

---

## Stealth vs WSS

Both survive DPI; they hide in opposite directions.

| | TCP + Stealth | [WSS / WSS Mux](websocket-tls.md) |
|---|---|---|
| What it looks like | random bytes — no protocol at all | ordinary HTTPS to a website |
| Needs a certificate | no | yes (self-signed or Let's Encrypt) |
| Can sit behind a CDN | no | yes |
| A browser hitting the port sees | nothing | a normal "Welcome to nginx" page |
| A scanner sweeping for it finds | nothing to match on | a different nginx on every install |
| Overhead | lower | TLS on top of WebSocket framing |

Rule of thumb: on a bare IP where "unidentifiable traffic" is fine, use Stealth.
Where unidentifiable traffic is itself suspicious, or you want a CDN in front,
use WSS.

---

<div dir="rtl">

## خلاصهٔ فارسی

**TCP + Stealth** یک تونل TCP است که داخل یک لایهٔ رمزنگاری Noise پیچیده شده.
روی سیم فقط بایت تصادفی دیده می‌شود — نه TLS، نه هیچ پروتکل قابل‌تشخیص دیگری —
پس DPI چیزی برای گیر دادن ندارد. بهترین انتخاب برای مسیرهای فیلترشده و لینکی که
TCP ساده روی آن وصل می‌شود و بعد می‌میرد.

کلید رمزنگاری از خود توکن ساخته می‌شود، پس چیز اضافه‌ای برای تنظیم نیست. اگر
توکن غلط باشد سرور اصلاً **جواب نمی‌دهد** و پورت بسته به‌نظر می‌رسد؛ پس اگر وصل
نشد، اول توکن و ترنسپورت دو طرف را چک کن.

راه‌اندازی مو‌به‌مو مثل [TCP](tcp.md) است، فقط در منو **TCP + Stealth** را
انتخاب کن — روی هر دو سرور. نه دامنه لازم دارد نه گواهی.

**Stealth یا WSS؟** اگر روی آی‌پی خام کار می‌کنی و «ترافیک ناشناس» مشکلی ندارد،
Stealth؛ اگر می‌خواهی دقیقاً شبیه یک سایت HTTPS باشی یا CDN جلویش بگذاری،
[WSS](websocket-tls.md).

</div>

---
[← Back to the tutorials](README.md)

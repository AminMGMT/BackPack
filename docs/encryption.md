# Encryption on the stream transports

TCP, TCP Mux, WS, WSS, WS Mux and WSS Mux run the plain engine, whose wire is
the reference one: the handshake carries the token as it is, and the traffic
is not encrypted (WSS and WSS Mux are under TLS). The `encryption` setting wraps
each of the tunnel's connections in something else. It is asked at setup
(**Encryption**), changed later from **Edit → Encryption**, and the Setup Link
carries it to the kharej.

**Both ends must have the same.** An end with another setting, or none, is a
tunnel that never comes up — never one that quietly runs in the clear.

| `encryption` | On | What it is |
|---|---|---|
| `none` (default) | all six | the plain wire — fastest |
| `stealth` | all six, Vision | Noise encryption: no fingerprint, the token never on the wire, and a probe on a TCP port is answered with nothing |
| `stealth+` | all six, Vision | Stealth, with the whole connection masked — handshake and record lengths included — and the sizes of small records shaped from the token, so packet sizes and timing say less |
| `scrap` | TCP, TCP Mux | Stealth+ inside an ordinary HTTP/1.1 upload: a `POST` with a one-use cookie and a chunked body. Anything without the token is answered as nginx: `404` to a request, `400` to anything that is not HTTP, and a copied request is refused the second time |

On WS and WSS with an encryption, the upgrade's `Authorization` header carries
a value derived from the token; the token itself is refused.

Under [Vision](transports.md#vision) the encryption runs inside the Xray
helper, end to end between the two engines; Vision takes no `scrap` or
`splash`.

## Splash

`splash = true` pads the first records of each connection, Vision-style, so
their sizes do not give the inner handshake away. Padding sent in the clear
hides nothing, so it needs WSS / WSS Mux, or an encryption on the others.

```toml
[server]
transport = "tcpmux"
encryption = "stealth+"
splash = true
```

## Speed

Measured locally, one connection: `stealth+` about 250 MB/s, `scrap` about
280 MB/s down and 180 MB/s up. On a real path the line is the limit long before
the encryption is.

---

<div dir="rtl">

## خلاصهٔ فارسی

روی TCP، TCP Mux، WS، WSS، WS Mux و WSS Mux گزینهٔ **Encryption** هر اتصال تونل را
در یک لایهٔ دیگر می‌پیچد: `stealth` (رمزنگاری Noise بدون fingerprint، توکن هرگز
روی سیم نمی‌رود)، `stealth+` (همان، به‌اضافهٔ پوشاندن طول رکوردها و اندازهٔ
پکت‌ها) و روی TCP و TCP Mux گزینهٔ `scrap` (شبیه آپلود HTTP معمولی؛ هر چیز
دیگری جواب nginx می‌گیرد). هر دو سرور باید یکسان باشند، وگرنه تونل اصلاً بالا
نمی‌آید. `splash = true` چند رکورد اول را padding می‌کند و روی WSS یا همراه یک
encryption کار می‌کند.

</div>

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v2.0.0.*

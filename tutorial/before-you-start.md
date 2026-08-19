# Before you start

Read this once. Every transport tutorial builds on it, and the four things below
account for nearly every tunnel that comes up and then carries nothing.

---

## 1. The two roles

Backpack is a **reverse** tunnel. The kharej machine dials the Iran machine, and
traffic flows the other way. So the roles are not what people expect:

```
  end users ──▶  IRAN server  ══ tunnel ══▶  KHAREJ server  ──▶  real service
                 "Setup Iran"                  "Setup Kharej"      (X-UI, panel,
                 exposes the ports             dials out to Iran     WireGuard…)
```

| Machine | Menu option | What it does |
|---|---|---|
| **Iran** (entry point) | **1. Setup Iran** | Listens on the tunnel port, exposes the forwarded ports. Your users connect **here**. |
| **Kharej** (exit) | **2. Setup Kharej** | Dials the Iran server and hands traffic to the real service on itself. |

Two consequences worth memorising:

- **The kharej server needs no open inbound port.** It only dials out.
- **Always build the Iran side first.** The client needs the Iran address, the
  tunnel port and the token the server generates.

## 2. The token

The server suggests a random 64-character token. **Copy it.** The client asks for
the same string, and a mismatch produces a tunnel that connects and is then
dropped — on the encrypted transports (Stealth, KCP, PCK, WSS) the server does
not even answer, so a wrong token looks exactly like a dead port.

Both ends must also run the **same transport** and, ideally, the **same preset**.

## 3. The ports — and what a mapping means

Two different kinds of port are asked for, and mixing them up is the single most
common setup mistake.

**Tunnel (control) port** — where the tunnel itself lives, on the Iran server.
The client dials this. It has nothing to do with your users.

**Forwarded ports** — what your users connect to on the Iran IP. Each one is
mapped to something on the kharej machine:

| You type | It means |
|---|---|
| `443` | expose 443 on Iran → kharej forwards it to **its own `127.0.0.1:443`** |
| `443=127.0.0.1:2096` | expose 443 on Iran → kharej's `127.0.0.1:2096` |
| `443=10.0.0.5:443` | expose 443 on Iran → another machine the kharej server can reach |
| `443=127.0.0.1:2096\|127.0.0.1:2097` | two backends, health-checked, balanced over the live ones |
| `8080,8443,443` | several ports at once |

> **A bare port is not "any port".** `443` means the service must be listening on
> `127.0.0.1:443` on the **kharej** machine. If your panel is on 2096 there, say
> so: `443=127.0.0.1:2096`. Setup prints the resolved list before it builds
> anything — read it.

A panel bound to the kharej machine's **public** IP instead of `127.0.0.1` will
refuse the tunnel's connection. Check with `ss -tlnp | grep <port>` on the kharej
server, and map it explicitly if needed: `443=<that IP>:443`.

## 4. The firewall

On the **Iran** server, open:

- the **tunnel port**, on the protocol the transport uses —
  `tcp` for the TCP and WebSocket families, `udp` for udp/kcp/quic, nothing for
  xDi (it rides in ICMP);
- every **forwarded port**, on `tcp`;
- every forwarded port on `udp` **as well**, but only if you turned UDP
  forwarding on — see below.

```bash
ufw allow 443/tcp        # forwarded port
ufw allow 8443/tcp       # tunnel port (TCP-family transport)
```

Nothing needs opening on the kharej server.

## 5. UDP is off by default

A forwarded port carries **TCP only** unless you say otherwise. Setup asks, right
after the ports:

```
Carry UDP as well as TCP on the exposed ports [y/N]:
```

Say **yes** for Xray/3x-ui UDP, Shadowsocks UDP relay, WireGuard, DNS or games.
Say **no** for a plain web or proxy tunnel — a browser's QUIC is UDP on 443, and
carrying it crowds out the TCP forwards on the pooled transports.

Answering yes also means `ufw allow <port>/udp`. Answering no and opening the UDP
port anyway does nothing. Full detail: [Adding UDP to a tunnel](udp-forwarding.md).

---

## The 60-second version

On the **Iran** server:

```bash
sudo backpack        →  1. Setup Iran  →  Reverse
```
transport → tunnel port → name → **copy the token** → forwarded ports → UDP? →
preset (**Turbo**) → done.

On the **kharej** server:

```bash
sudo backpack        →  2. Setup Kharej  →  Reverse
```
same transport → Iran IP + same tunnel port → name → **same token** → same preset
→ done.

Then check both sides with **Manage → Status**, and if anything is off,
**Manage → Health Check** prints a fix under each problem.

---

## When it does not work

| Symptom | Look at |
|---|---|
| Tunnel never connects | token mismatch, transport mismatch, tunnel port closed in the Iran firewall |
| Tunnel is up, port refuses | the service is not listening on the mapped address on **kharej** — `ss -tlnp` |
| Tunnel is up, TCP fine, UDP dead | UDP forwarding is off — [see here](udp-forwarding.md) |
| Connects, then stalls on big transfers | path MTU — **Manage → Health Check**, then [MSS clamp](../docs/mss-clamp.md) |
| Panel counts all users as one device | [real client IP](../docs/real-client-ip.md) |
| Works on the IP, not on the domain | an AAAA record sending the tunnel over IPv6, or a CDN in front — setup warns about both |

---

<div dir="rtl">

## خلاصهٔ فارسی

**نقش‌ها:** سرور **ایران** با گزینهٔ «Setup Iran» ساخته می‌شود و پورت‌ها را در
معرض کاربر می‌گذارد؛ سرور **خارج** با «Setup Kharej» ساخته می‌شود و به ایران وصل
می‌شود. همیشه **اول سمت ایران** را بساز، چون کلاینت به آدرس و توکن آن نیاز دارد.
سمت خارج هیچ پورت ورودی بازی لازم ندارد.

**توکن:** سرور یک توکن ۶۴ کاراکتری پیشنهاد می‌دهد؛ همان را روی کلاینت وارد کن.
توکن اشتباه = تونلی که وصل نمی‌شود (روی ترنسپورت‌های رمزنگاری‌شده اصلاً جواب داده
نمی‌شود و شبیه پورت بسته دیده می‌شود). ترنسپورت دو طرف هم باید یکی باشد.

**پورت‌ها:** «Tunnel port» پورت خود تونل است و کاربر با آن کاری ندارد. «Forwarded
ports» همان‌هایی است که کاربر روی آی‌پی ایران به آن وصل می‌شود. پورت خالی مثل
`443` یعنی «روی خارج به `127.0.0.1:443` خودش تحویل بده» — اگر پنلت آنجا روی ۲۰۹۶
است باید بنویسی `443=127.0.0.1:2096`.

**فایروال (فقط روی ایران):** پورت تونل + همهٔ پورت‌های forward شده روی `tcp`، و
اگر UDP را روشن کرده‌ای روی `udp` هم.

**UDP به‌صورت پیش‌فرض خاموش است.** ویزارد بعد از پورت‌ها می‌پرسد؛ برای
Xray/3x-ui، وایرگارد، DNS و بازی جواب بده «y». باز کردن پورت UDP در فایروال بدون
روشن کردن این گزینه هیچ اثری ندارد.

</div>

---
[← Back to the tutorials](README.md)

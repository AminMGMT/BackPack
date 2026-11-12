# Setting up a direct PCK tunnel

A tunnel that forwards the ports you choose from the Iran server to the kharej
server, in packets that look like an ordinary TCP flow. Since v2.0.0 it runs on
**[paqet](https://github.com/hanselime/paqet)** (MIT) — KCP for reliability, smux
to carry many connections, libpcap to build and read the packets.

**Reach for it when** a plain TCP tunnel connects and then dies, stalls or is
throttled, and you want it to survive loss: KCP retransmits what the path drops.

**Requirements:** Linux, root (or `CAP_NET_RAW`), `iptables`, a **free** tunnel
port on the kharej, an **IPv4** kharej address, and **both servers on v2.0.0 or
later**.

> This is the *direct* PCK carrier. The reverse **TCP + PCK** transport is a
> different engine: [TCP + PCK](tcp-pck.md). Reference page:
> [docs/direct-pck.md](../docs/direct-pck.md).

---

## What you will not be asked

There is no TUN here, so none of the questions about one: no interface, no tunnel
addresses, no MTU, no error correction, no GRE key. And no token — it is
generated on the Iran server, it is the encryption key, and the setup link carries
it to the kharej.

## On the Iran server

```bash
sudo backpack
```

**Setup Iran → Direct → PCK**, then:

| Prompt | Answer |
|---|---|
| `Kharej IP Or Domain:` | the kharej's IPv4 address or a domain |
| `Tunnel Port [9000]:` | Enter, or any free port on the kharej |
| `Forwarded Ports:` | `443`, `2053=8443` (listen here on 2053, dial `127.0.0.1:8443` on the kharej), or a range |
| `Carry UDP As Well As TCP On Those Ports (y/N)` | `y` only if you forward UDP |
| `Tunnel Name` | Enter |
| `How Should The Tunnel Be Tuned?` | Turbo (default), Balance or Aggressive — each shows what it sets |
| `Fine-Tune The Advanced Settings (y/N)` | Enter. Sessions, speed mode, windows, packet size and cipher, all optional |
| `Restart This Tunnel On Both Servers Every N Hours` | `0` |

The summary ends with the **Setup Link**. Press Enter on **Create This Tunnel**.

## On the kharej server

```bash
sudo backpack
```

**Setup Kharej → Direct → PCK → Setup Link**, paste the line from the Iran
server. It asks for a name and nothing else. The tunnel starts and comes up when
the Iran server dials in.

On a kharej that does not run Backpack yet, the Iran summary also prints one
command that installs it and applies the link.

## Check it

On the Iran server: **Manage → Health Check** shows the three things PCK needs —
libpcap, the firewall guard and the gateway — then **Connection Test** measures
it. A tunnel that is "running but not connected" is nearly always one of:

- the two servers are not on the same version (the log says "still running the
  previous PCK" or "no answer from …");
- the tunnel port is blocked, or a service listens on it on the kharej;
- different tokens or `pck_kcp_block` on the two ends — a mismatch looks exactly
  like a blocked port.

## From the previous PCK

An existing PCK tunnel keeps running after the update, unchanged, as long as
**both** servers are updated. Its file is not rewritten; the old TUN keys in it
are ignored. Saving it in **Edit** writes the new format. A tunnel that carried
routed traffic (no ports) cannot go on doing that: use xDi, SNI Spoofing, IP
Spoofing or GRE.

---

<div dir="rtl">

## خلاصهٔ فارسی

PCK مستقیم یک تونل **فوروارد پورت** روی [paqet](https://github.com/hanselime/paqet)
است: بسته‌های شبیه TCP معمولی، KCP برای بازیابی از دست‌رفتن بسته. TUN، آدرس تونل،
MTU، FEC و حتی سؤال توکن ندارد؛ توکن روی ایران ساخته می‌شود و همان کلید رمز است.

۱. ایران: `Setup Iran ← Direct ← PCK` — آدرس خارج، پورت تونل، پورت‌های فوروارد،
UDP، نام و پریست. لینک تنظیم را از خلاصه کپی کن.
۲. خارج: `Setup Kharej ← Direct ← PCK ← Setup Link` و لینک را بچسبان.

هر دو سرور باید v2.0.0 یا جدیدتر باشند. تونل PCK قدیمی همان‌طور که هست کار می‌کند؛
با اولین ذخیره فایل به قالب جدید می‌رود. [راهنمای کامل](../docs/direct-pck.md)

</div>

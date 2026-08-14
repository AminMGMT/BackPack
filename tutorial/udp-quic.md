# Setting up a UDP + QUIC tunnel

QUIC carries the tunnel inside its own streams over UDP: its own TLS 1.3, its own
stream multiplexing, congestion control and loss recovery. Every byte is
encrypted and there is nothing to hand-tune.

**It is offered, not recommended.**

---

## Read this before choosing it

Backpack built, tested and *dropped* QUIC once already. On a real Iran route it
never completed a handshake, while [KCP](udp-kcp-fec.md) on the same link ran at
full speed. That finding still stands and nothing since has disproved it — which
is why the Link Test's advisor keeps recommending KCP for a lossy link and names
QUIC only as the other thing to try.

So: **test it on your own route before committing to it.** If it works for you,
it is a good transport — self-tuning, encrypted end to end, strong under loss. If
the handshake never completes, that is the known behaviour, not a misconfiguration
on your side.

---

## The setup

The [TCP walkthrough](tcp.md), with **`UDP` → `UDP + QUIC`** on both ends.

- **The tunnel port is UDP:** `ufw allow 8443/udp` on the Iran server.
- **No certificate to arrange** — QUIC brings its own TLS 1.3, keyed from the
  tunnel token.
- **No MSS clamp**, no mux settings, no KCP knobs. Congestion control and loss
  recovery are QUIC's own, and the presets have little left to tune.
- **PROXY protocol is available.**

---

## Testing it honestly

1. Build both ends and watch **Manage → Status** for a minute.
2. If the client keeps dialling and the server never shows a peer, read the log:
   `journalctl -u backpack-<name> -f`. A handshake that never completes is the
   documented failure mode.
3. Compare against KCP on the same route: **Manage → Link Test** on the kharej
   server gives you a measured recommendation rather than a guess.

If QUIC does not connect, switch with **Manage → Edit → Change transport** on
both ends — the token, ports and name are kept.

---

<div dir="rtl">

## خلاصهٔ فارسی

**UDP + QUIC** تونل را داخل استریم‌های QUIC روی UDP می‌برد: TLS 1.3 خودش،
مالتی‌پلکس خودش، کنترل ازدحام و بازیابی افت خودش. چیزی برای تنظیم دستی ندارد.

**پیشنهاد نمی‌شود، فقط در دسترس است.** یک‌بار ساخته و روی مسیر واقعی ایران تست و
کنار گذاشته شد، چون handshake اصلاً کامل نمی‌شد در حالی که
[KCP](udp-kcp-fec.md) روی همان لینک با تمام سرعت کار می‌کرد. اگر روی مسیر تو
جواب داد، ترنسپورت خوبی است؛ اگر وصل نشد، همان رفتار شناخته‌شده است نه اشتباه
تنظیمات تو.

راه‌اندازی مثل [TCP](tcp.md) با انتخاب `UDP` → `UDP + QUIC` در دو طرف. پورت تونل
را روی **`udp`** باز کن. گواهی TLS لازم ندارد و MSS clamp هم ندارد.

</div>

---
[← Back to the tutorials](README.md)

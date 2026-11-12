# Direct tunnel (stream transports)

> **Removed in v2.0.0.** The layer-4 `[direct]` engine described on this page no
> longer exists. For a direct tunnel use **Setup Iran → Direct**, which builds the
> [layer-3 direct tunnel](l3-direct-tunnel.md).

## What it was

The same forwarded ports as the reverse tunnel, with the tunnel dialled the other
way round: Iran dialled out and kharej listened, over `tcp`, `stealth`, `ws` or
`wss`, with one smux session carrying every user connection. Since v1.7.3 the
wizard no longer offered it; it only survived as a hand-written `[direct]` file
and for tunnels built before then.

## What happens to an existing `[direct]` file

The file is still recognised, so nothing disappears silently:

- the tunnel is **still listed** in the menu, the health check and the web panel;
- it **cannot run**: starting it, reloading it and `backpack check` all refuse it
  with a clear error saying the `[direct]` engine was removed in v2.0.0;
- it **can still be deleted**, from the menu or the panel.

There is no automatic conversion. The two engines work differently (a stream
tunnel against a packet tunnel with its own carriers and addresses), so a
`[direct]` file cannot be translated line by line.

## How to rebuild it

1. Delete the old tunnel (**Manage → Manage Tunnels → the tunnel → Delete**, or in
   the panel).
2. On the Iran server run `sudo backpack` → **Setup Iran** → **Direct**, pick a
   carrier (`pck` — ports only, no TUN, see [Direct PCK](direct-pck.md) — or `xdi`, `sni`, `spoof`, or the unencrypted kernel `gre`), and paste the **Setup Link** it
   prints on the kharej server under **Setup Kharej → Direct**.

The new tunnel keeps the same shape — Iran dials out, kharej listens, and
**Forwarded Ports** exposes the same ports on Iran — but it needs root or
`CAP_NET_RAW` on both servers and runs on Linux only. Everything about it is in
[Direct layer-3 tunnel](l3-direct-tunnel.md).

If you want the reverse direction instead, the reverse tunnel is untouched: see
[Choosing a transport](choosing-a-transport.md).

---

<div dir="rtl">

## خلاصهٔ فارسی

**در v2.0.0 حذف شد.** موتور لایه ۴ `[direct]` (ایران dial می‌کرد؛ `tcp`، `stealth`،
`ws`، `wss` با یک سشن smux) دیگر وجود ندارد. فایلی که جدول `[direct]` دارد هنوز
شناخته می‌شود: تونل در لیست می‌ماند و قابل حذف است، ولی اجرا نمی‌شود و با پیام
روشن رد می‌شود. تبدیل خودکار وجود ندارد. برای ساختن دوباره: **Setup Iran ← Direct**
(تونل [direct لایه ۳](l3-direct-tunnel.md)) با یکی از حامل‌های `pck`، `xdi`، `sni`
یا `spoof`؛ لینک را روی خارج در **Setup Kharej ← Direct** پیست کن. به root یا
`CAP_NET_RAW` روی هر دو سرور نیاز دارد و فقط روی لینوکس کار می‌کند. تونل معکوس
دست‌نخورده است.

</div>

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v2.0.0.*

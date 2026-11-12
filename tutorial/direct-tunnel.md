# Setting up a direct tunnel (stream transports)

> **This engine was removed in v2.0.0.** The layer-4 `[direct]` tunnel — Iran
> dialling out over `tcp`, `stealth`, `ws` or `wss` with one smux session — no
> longer exists, so there is nothing here to set up. Use the
> [direct layer-3 tunnel](../docs/l3-direct-tunnel.md) instead.

The reference page, [Direct tunnel](../docs/direct-tunnel.md), says what happens to
an existing `[direct]` file: the tunnel is still listed and can be deleted, but it
is refused at start with a clear error and cannot run.

## Rebuilding it

1. Delete the old tunnel: `sudo backpack` → **Manage** → **Manage Tunnels** → the
   tunnel → **Delete**, or in the web panel.
2. On the Iran server: **Setup Iran** → **Direct** → a carrier (**PCK**, **xDi**,
   **SNI Spoofing**, **IP Spoofing** or **GRE** — GRE is unencrypted and has no
   tunnel port or token) → the kharej address, tunnel port,
   **Forwarded Ports** (the ones the old tunnel exposed), name and token.
3. Copy the **Setup Link** from the summary. On the kharej server: **Setup Kharej**
   → **Direct** → the same carrier → **Setup Link** → paste it.

The ports do not move: Iran still exposes them, kharej still holds the service,
and Iran still dials out. The difference is that the new tunnel is a network
interface carrying IP packets, so it needs root or `CAP_NET_RAW` on both servers
and runs on Linux only. Every option is on the
[reference page](../docs/l3-direct-tunnel.md).

---

<div dir="rtl">

## خلاصهٔ فارسی

این موتور (تونل مستقیم لایه ۴ با `tcp`، `stealth`، `ws`، `wss`) در **v2.0.0 حذف شد**.
فایل `[direct]` قدیمی هنوز در لیست می‌ماند و قابل حذف است، ولی اجرا نمی‌شود. برای
ساختن دوباره: تونل قدیمی را حذف کن، روی ایران **Setup Iran ← Direct** و یک حامل
(PCK، xDi، SNI Spoofing یا IP Spoofing) را انتخاب کن و پورت‌های forward قبلی را بده؛
بعد لینک را روی خارج در **Setup Kharej ← Direct** پیست کن. به root یا `CAP_NET_RAW`
روی هر دو سرور نیاز دارد. [راهنمای کامل](../docs/l3-direct-tunnel.md)

</div>

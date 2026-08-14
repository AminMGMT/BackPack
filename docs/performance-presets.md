# Performance presets

Instead of a yes/no "best performance" switch, every tunnel picks a preset. Each
fills in **every** tuning value at once — connection pools, socket buffers,
receive windows, mux windows, and on KCP the retransmission and error-correction
settings — and applies the kernel tuning (BBR + fq, buffer ceilings, file
limits).

| Preset | For | Notes |
|--------|-----|-------|
| **Balance** | a small or shared VPS | Light on CPU and RAM. |
| **Turbo** | most people (**recommended**) | **Byte-for-byte identical to the old "Best Performance"**, so upgrading changes nothing about an existing tunnel. |
| **Aggressive** | maximum gaming headroom | 32 MB socket buffers, 16 MB per mux stream. Noticeably more CPU. The KCP window deliberately stays at 2048 — with congestion control off, a deeper window *is* a deeper queue. |
| **Throughput** | **bandwidth, not play** | Offered on the plain `kcp` transport only. |

## About Throughput

Balance, Turbo and Aggressive are all **gaming** profiles: they differ in how
much headroom they buy, but every one of them spends bandwidth to hold the ping
steady — immediate ACKs, a 10 ms tick, and enough parity to repair a loss rather
than wait a round trip for the retransmit. That is the right trade for a game and
the wrong one for a download.

Throughput makes the opposite trade on the same transport: ACKs batched, a 20 ms
tick, 10:1 parity instead of 10:4, a 4096-packet window and a 32 MB per-stream
buffer, so one stream can fill a long fat path (~210 Mbit/s at 200 ms round trip,
against ~105 on Aggressive). Measured end to end it carries roughly twice what
Aggressive does.

**It is not a gaming preset.** With congestion control off, a window that large
is a queue that deep. Use Turbo or Aggressive for play and this for transfers.

It is offered on `kcp` alone — the other KCP carriers (`xdi`, `spoof`, `pck`)
build their packets by hand and pay a syscall per datagram, so bandwidth is not
what they are for, and on a TCP transport every knob it changes belongs to the
kernel rather than to this process. Choosing it elsewhere is **refused with a
message saying so**, rather than written to the config and quietly ignored.

## Changing it later

**Edit → Change performance preset**, and apply the **same preset on both ends**.
Configs written before presets existed carry no preset field and are left exactly
as they are.

Editing any tuning value by hand marks the tunnel **Custom**, so a later preset
change will not silently overwrite your answers.

The [TCP MSS clamp](mss-clamp.md) is not one of these values and a preset change
leaves it alone: it describes the path the tunnel crosses rather than how hard
the tunnel is being pushed.

---

<div dir="rtl">

## خلاصهٔ فارسی

هر تونل یک **پریست** می‌گیرد که **همهٔ** مقادیر تنظیم را یک‌جا پر می‌کند — اندازهٔ
pool، بافر سوکت، پنجره‌های دریافت و mux، و روی KCP تنظیمات ارسال مجدد و تصحیح
خطا — و تنظیمات کرنل (BBR + fq و سقف بافرها) را هم اعمال می‌کند.

**Balance** برای VPS کوچک یا اشتراکی · **Turbo** پیشنهادی برای اکثر آدم‌ها (دقیقاً
همان چیزی که قبلاً «Best Performance» بود) · **Aggressive** حداکثر فضای تنفس برای
بازی با مصرف CPU بیشتر · **Throughput** فقط روی ترنسپورت `kcp` و فقط برای
**پهنای باند، نه بازی**.

**دربارهٔ Throughput:** سه پریست دیگر همه پروفایل *بازی* هستند و پهنای باند خرج
می‌کنند تا پینگ ثابت بماند. Throughput معاملهٔ برعکس را می‌کند (ACK دسته‌ای، تیک
۲۰ms، parity ۱۰:۱، پنجرهٔ ۴۰۹۶) و حدود دو برابر Aggressive داده جابه‌جا می‌کند —
ولی با کنترل ازدحام خاموش، پنجرهٔ آن‌قدر بزرگ یعنی صف آن‌قدر عمیق. برای بازی
Turbo یا Aggressive، برای انتقال فایل Throughput. انتخابش روی ترنسپورت‌های دیگر
**با پیام صریح رد می‌شود**، نه اینکه بی‌صدا نادیده گرفته شود.

پریست را بعداً از `Edit → Change performance preset` عوض کن و **روی هر دو طرف
یکی بگذار**. اگر مقداری را دستی تغییر بدهی تونل «Custom» علامت می‌خورد تا تغییر
پریست بعدی جواب‌هایت را پاک نکند. [MSS clamp](mss-clamp.md) جزو پریست نیست و
دست‌نخورده می‌ماند.

</div>

---
[← Back to the docs index](README.md)

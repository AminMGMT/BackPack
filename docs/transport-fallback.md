# Transport fallback

A tunnel is pinned to one carrier. When that carrier is the one being filtered,
the tunnel retries it for ever and the operator is the failover mechanism.

A **fallback chain** is a list of carriers the tunnel may move to on its own.

```toml
[client]
transport            = "wss"
fallback_transports  = ["quic", "kcp", "tcpmux"]
fallback_dwell       = 60      # seconds; optional, this is the default
```

```toml
[server]
transport            = "wss"
fallback_transports  = ["quic", "kcp", "tcpmux"]
fallback_dwell       = 60
```

## Both ends need the same list

This is the one way to configure it wrongly, so it is worth saying plainly: the
two ends never tell each other which carrier they are on. A list on the client
alone leaves the server listening for a single carrier, and the tunnel spends
its time dialling an ear that is not there.

Put the same carriers in the same order on both ends.

## How they meet without negotiating

The server holds each candidate for the whole dwell. The client gives each
candidate `dwell / number-of-candidates`, so the client tries every carrier at
least once inside one server dwell. They therefore meet within one dwell, with
no handshake, no shared clock and nothing new on the wire.

The client's window has a floor of five seconds, so a carrier is never rejected
for being slower than a dial timeout.

## Why one carrier at a time

Starting a transport on the server binds the forwarded ports. Two live
candidates would fight over them and the second would lose, so candidates run in
sequence — which costs nothing, because only one can be carrying traffic anyway.
The outgoing candidate is cancelled and given time to release its ports before
the next one binds.

## What it does not do

It is a better *connect*, not live switching. There is no session migration: a
carrier that is up keeps its own reconnect behaviour, and the chain leaves it
alone. Rotation resumes only if a carrier that was working stays down for five
minutes, which is the case where the carrier really has been taken away rather
than a transient drop.

## Relationship to backup addresses

`fallback_addrs` answers *this address stopped answering* — a filtered IP, a
blocked port, a different CDN edge. `fallback_transports` answers *this protocol
stopped getting through* — the server is perfectly reachable and it is the shape
of the traffic that is being blocked. They are independent and a tunnel may use
both.

## From the menu

`Manage tunnels → <tunnel> → Transport fallback chain`, on either end.

## Where it lives

`internal/tunnel/chain` is the whole mechanism; the package comment carries the
reasoning. `internal/client/client.go` and `internal/server/server.go` each hand
it a `startTransport` and a way to ask whether the control channel is up.

---

<div dir="rtl">

## خلاصهٔ فارسی

یک تونل به **یک** حامل سنجاق شده است. وقتی همان حامل چیزی است که فیلتر می‌شود،
تونل تا ابد همان را دوباره امتحان می‌کند و در عمل *اپراتور* مکانیزم failover
است. **زنجیرهٔ fallback** فهرستی از حامل‌هاست که تونل خودش می‌تواند به آن‌ها برود:

```toml
transport           = "wss"
fallback_transports = ["quic", "kcp", "tcpmux"]
fallback_dwell      = 60      # ثانیه، اختیاری — همین پیش‌فرض است
```

**هر دو طرف باید همان فهرست را با همان ترتیب داشته باشند.** این تنها راهِ اشتباه
تنظیم‌کردنش است و صریح گفته می‌شود: دو طرف هیچ‌وقت به هم نمی‌گویند روی کدام حامل
هستند. فهرست فقط روی کلاینت یعنی سرور هنوز منتظر یک حامل است و تونل وقتش را صرف
زنگ‌زدن به گوشی می‌کند که آنجا نیست.

**چطور بدون مذاکره همدیگر را پیدا می‌کنند:** سرور هر کاندید را کل `dwell` نگه
می‌دارد؛ کلاینت به هر کاندید `dwell ÷ تعداد کاندیدها` وقت می‌دهد، پس داخل یک
dwellِ سرور همهٔ حامل‌ها را دست‌کم یک‌بار امتحان می‌کند. نتیجه: حداکثر ظرف یک
dwell به هم می‌رسند، بدون handshake، بدون ساعت مشترک و بدون هیچ چیز تازه‌ای روی
سیم. پنجرهٔ کلاینت کفِ پنج ثانیه دارد تا حاملی فقط به‌خاطر کندتربودن از یک dial
timeout رد نشود.

**چرا هر بار یک حامل:** بالاآوردن یک ترنسپورت روی سرور پورت‌های forward را
می‌گیرد؛ دو کاندید زنده سر همان پورت‌ها دعوا می‌کنند و دومی می‌بازد. پس پشت سر هم
اجرا می‌شوند — که هزینه‌ای ندارد، چون در هر حال فقط یکی می‌تواند ترافیک حمل کند.

**کاری که نمی‌کند:** این یک *اتصالِ* بهتر است، نه جابه‌جایی زنده. مهاجرت سشن وجود
ندارد؛ حاملی که بالاست رفتار reconnect خودش را دارد و زنجیره کاری به آن ندارد.
چرخش فقط وقتی از سر گرفته می‌شود که حاملی که کار می‌کرد **پنج دقیقه** پایین
بماند — یعنی جایی که واقعاً حامل را گرفته‌اند، نه یک قطعی گذرا.

**نسبتش با آدرس‌های پشتیبان:** `fallback_addrs` جوابِ «این آدرس دیگر جواب نمی‌دهد»
است (IP فیلترشده، پورت بسته، edge دیگری از CDN). `fallback_transports` جوابِ «این
پروتکل دیگر رد نمی‌شود» است — سرور کاملاً در دسترس است و *شکل* ترافیک بلاک شده.
مستقل‌اند و یک تونل می‌تواند هر دو را داشته باشد.

**از منو:** `Manage tunnels → <tunnel> → Transport fallback chain`، روی هر دو سر.

</div>

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v1.8.2.*

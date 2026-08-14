# Failover & load balancing

A client tunnel can hold more than one address for the same server, so a single
filtered IP or blocked port does not take the tunnel down.

## Add backup addresses

On the **client** (kharej), go to **Edit → Backup server addresses** and enter a
comma-separated list:

```
1.2.3.4, 5.6.7.8:8443, edge.example.com:443
```

If the main address stops answering, the client fails over to the next one
automatically. This is what keeps a tunnel alive after a server IP gets
filtered — the most common way a route dies on this path.

## Automatic failover (health-scored)

Plain failover only reacts when an address stops answering. A route usually gets
*worse* long before it dies — latency climbs, jitter sets in, a few percent of
packets drop — and none of that trips a failed connection. **Automatic failover**
handles that: it scores every address every few seconds by

```
score = mean_rtt + 2 × jitter + 20 × loss%      (lower is better)
```

and keeps traffic on the healthiest exit. Jitter and loss are weighted far above
raw latency because a steady 90 ms exit beats a 60 ms one that stutters — a
stutter is a lost frame, latency alone is not. A new exit only takes over when it
is at least **15% better for three checks in a row**, so the choice does not flap.

Turn it on at setup when a tunnel has backup addresses, or later from the menu.
It is the multi-exit setup for gaming, and it **replaces** load balancing — one
picks the single best exit, the other spreads across all of them, so only one can
be on at a time.

**Manage → Exit Health** scores and ranks every address on demand, and offers to
pin the healthiest as the primary — the manual version of the same thing.

> Scoring pings each address, so an exit that filters ICMP shows as unreachable
> even when the tunnel through it works. Use plain failover or load balancing for
> such a server.

## Load balancing

By default the extra addresses are only spares. Turn on **load balancing** and
they become active routes at the same time: the tunnel's data connections are
spread across all of them, so one throttled route slows only its own share of
the traffic rather than the whole tunnel. The control channel stays pinned to a
single address, since that is what identifies the peer.

## The one rule

**Every address must reach the same server.** That can be:

- a second IP of the same server,
- another of its ports, or
- a CDN edge in front of it.

Pointing them at different machines will not work — the token and the exposed
ports belong to one server.

## CDN edge (hiding the server's IP)

At client setup you can enter an **Edge IP** — the client then connects to a CDN
edge (e.g. Cloudflare) instead of the server's origin, and the CDN forwards to
the server. The origin IP is never exposed to the client side, which is one way
to keep a server's address off a blocklist. This works with **WSS/WSS Mux on a
CDN-proxied port** (443, 8443, 2053, …); a raw transport cannot go through a CDN.

See [When a server is filtered, blocked, or its IP is dirty](filtered-or-dirty-ip.md)
for the full picture.

---

<div dir="rtl">

## خلاصهٔ فارسی

یک تونل کلاینت می‌تواند چند آدرس برای **همان سرور** داشته باشد تا فیلتر شدن یک
آی‌پی یا بسته شدن یک پورت، تونل را از کار نیندازد.

**آدرس‌های پشتیبان:** روی سرور خارج، `Edit → Backup server addresses` و لیستی با
کاما مثل `1.2.3.4, 5.6.7.8:8443, edge.example.com:443`. اگر آدرس اصلی جواب ندهد،
کلاینت خودکار سراغ بعدی می‌رود.

**فِیل‌اوور خودکار (امتیازدهی سلامت):** فِیل‌اوور ساده فقط وقتی واکنش نشان می‌دهد
که آدرسی کاملاً بمیرد، ولی مسیر معمولاً خیلی قبل‌تر **بد** می‌شود. این حالت هر
چند ثانیه همهٔ آدرس‌ها را با فرمول `rtt + 2×jitter + 20×loss%` امتیاز می‌دهد و
ترافیک را روی سالم‌ترین خروجی نگه می‌دارد. جیتر و افت خیلی بیشتر از تأخیر خام
وزن دارند، چون یک خروجی ۹۰ms پایدار بهتر از ۶۰ms پرنوسان است. یک آدرس جدید فقط
وقتی جایگزین می‌شود که **حداقل ۱۵٪ بهتر باشد، سه بار پشت سر هم** — تا انتخاب
مدام تغییر نکند. این همان ست‌آپ چند-خروجی مخصوص بازی است و **جایگزین** لود
بالانس می‌شود.

**لود بالانس:** به‌جای اینکه آدرس‌های اضافی فقط یدکی باشند، همه هم‌زمان فعال
می‌شوند و اتصال‌ها بینشان پخش می‌شود، پس یک مسیر throttle‌شده فقط سهم خودش را کند
می‌کند.

**تنها قانون:** همهٔ آدرس‌ها باید به **همان سرور** برسند — آی‌پی دوم همان سرور،
پورت دیگرش، یا یک لبهٔ CDN جلویش. آدرس‌هایی که به ماشین دیگری می‌روند کار
نمی‌کنند.

**نکته:** امتیازدهی از پینگ استفاده می‌کند، پس خروجی‌ای که ICMP را می‌بندد
«در دسترس نیست» دیده می‌شود حتی اگر تونلش کار کند — برای چنین سروری از فِیل‌اوور
ساده یا لود بالانس استفاده کن.

</div>

---
[← Back to the docs index](README.md)

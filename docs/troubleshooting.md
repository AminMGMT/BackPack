# Troubleshooting

Ordered by how often each one is actually the answer, not by how interesting it
is. Work down the list; most problems are settled in the first two sections.

Everything here can be done from `sudo backpack` → **Health Check** and
**Diagnose**. The commands are given as well, for when the menu is not
convenient — over a phone, from a script, or when the panel is the thing that
is broken.

---

## The tunnel says it is up and nothing goes through

This is the most expensive failure in the product and it has a short list of
causes. The tunnel holds its control channel, the panel shows green, and
traffic dies — so nothing looks wrong anywhere.

Backpack now notices this on its own: the watchdog watches for **one direction
moving while the other is frozen**, which is what a stall looks like from the
outside, and reports it before it restarts anything. If you have a Telegram bot
configured you will have had a message. This is what to do about it.

### 1. Check the MTU first

It is the cause more often than everything else here put together, and it is
invisible: the handshake is small and gets through, the first real transfer is
full-sized and does not.

```
sudo backpack        # → Diagnose → the tunnel
```

Look for the path MTU line. If it is below 1500, clamp to it:

```
sudo backpack        # → Manage tunnels → <tunnel> → TCP MSS clamp
```

A sensible clamp is **path MTU − 40** for IPv4, **− 60** for IPv6. If Diagnose
could not probe the path, try 1400 first and go down in steps of 20.

For a layer-3 tunnel, set `auto_mtu = true` instead and let it find the number.

### 2. Check the service at the far end

The tunnel delivers each connection one hop further, to whatever the forwarded
port points at. If nothing is listening there, every connection dies one step
past the end of the tunnel — and the tunnel itself is perfectly healthy.

On the **kharej** machine:

```
ss -tlnp | grep <the backend port>
```

Backpack reports this itself when it can see it. The panel's tunnel card shows a
"last hop" warning, and so does the terminal:

```
backpack tunnel status <name>
...
last hop  127.0.0.1:8080 refused (42 failed)
```

`refused` is a service that is not running. `timeout` is usually a firewall on
the same machine. The two have different fixes, which is why the word is there.

### 3. Check the two ends agree

Every paired setting has to match, and a mismatch produces exactly this failure:
connected, carrying nothing. The token, the transport, the packet profile, the
FEC scheme, `stealth`, the encapsulation.

The fastest check is to read the two files side by side:

```
sudo cat /etc/backpack/<name>.toml          # on this machine
```

and the same on the other. `token`, `transport`, the port, and — for a layer-3
tunnel — `carrier`, `encap`, `fec_data`/`fec_parity` and `profile`.

If the far end is a server the panel manages, do not compare files at all —
build the pair from the panel instead. Creating a tunnel there writes **both**
ends, so the paired settings cannot disagree, and an existing tunnel can be
linked to the server holding its other half from the tunnel's card. See
[managed servers](managed-servers.md).

### 4. Look at what it is actually carrying

```
backpack tunnel status <name>
```

```
name      kharej-443
role      server
transport tcp
address   0.0.0.0:443
state     online
peer      203.0.113.9:51234
carried   1.4 GB in, 22.8 MB out (3s ago)
```

Bytes in and bytes out are counted separately, and that is the whole point:
**one climbing and the other frozen** is the stall above. **Both frozen** is an
idle tunnel, which is not a fault — most tunnels are idle most of the time.

Run it twice a few seconds apart. If neither number moved while you know traffic
is being sent at it, the tunnel is not carrying.

The figure is dated because a reading nobody can date is a reading nobody can
use; if the engine has not written one recently the line is absent rather than
stale.

---

## The tunnel will not connect at all

### It never connected once

Then it is configuration, not the network. In order:

```
backpack check -c /etc/backpack/<name>.toml
```

This validates the file without starting anything, and it catches the quiet one:
a file describing two kinds of tunnel, where the engine runs the first and
silently ignores the rest.

Then check, in this order:

- **The token matches.** It is the single most common mistake and it produces no
  message that names it: the server refuses the connection and neither log says
  why, because saying why would tell an attacker they were close.
- **The port matches.** The client's `remote_addr` port must be the server's
  `bind_addr` port.
- **The transport matches.** `tcp` on one end and `tcpmux` on the other is two
  different protocols.
- **The firewall.** On the Iran server, the tunnel port must be reachable:
  ```
  sudo ufw allow <port>/tcp     # or /udp for kcp, quic, udp
  ```

### It connected before and stopped

Then something changed on the path, not in the config.

```
sudo backpack        # → Diagnose → the tunnel
```

If the address is unreachable but the server is up, the IP has probably been
filtered. Two answers, both built in:

- **Backup addresses** — a second IP, a different port, a CDN edge.
  `Manage tunnels → <tunnel> → Backup server addresses`
- **A transport fallback chain** — for when it is the *carrier* being blocked
  rather than the address. `Manage tunnels → <tunnel> → Transport fallback
  chain`. Both ends need the same list; see [transport fallback](transport-fallback.md).

If you do not know which of the two it is: if a plain `nc -vz <ip> <port>` from
outside Iran works and from inside does not, the address is filtered. If it
works from both and the tunnel still will not come up, the carrier is.

---

## The tunnel keeps restarting

A tunnel that mostly works is harder to notice than one that is plainly down,
and several real faults present this way. Backpack reports repeated restarts as
**one** condition rather than twenty separate lines, so check the alerts first:

```
sudo backpack        # → Alerts
```

Common causes, in order:

- **MTU.** A path that drops full-sized packets kills the session on the first
  real transfer, every time. See the top of this page.
- **A keepalive set too tight.** `keepalive_period` too low tears down a tunnel
  that is merely slow. On a bad path, raise it.
- **The far end is restarting.** Check the other machine before changing
  anything on this one.

---

## The panel will not open

```
systemctl status backpack-webui
journalctl -u backpack-webui -n 50
```

The panel answers under a **secret path** and nowhere else, so a bookmark
without it gets a 404 rather than a login page. The path is printed at startup
and is shown in the menu:

```
sudo backpack        # → Web Panel
```

If the port was changed and the page did not come back, the address is in the
same screen.

---

## A managed server shows as offline

The panel dials out over SSH; nothing is opened on the far side. So "offline"
means one of:

- **The machine is down**, or its SSH port changed.
- **The password changed.** The panel keeps it sealed with a key that is
  deliberately not in the backup, so restoring a panel onto a new machine
  returns the fleet list without the credentials. See
  [backup and restore](backup-restore.md) for the recovery.
- **The host key changed.** The panel pins the key it first saw and refuses a
  different one. If the server was rebuilt, this is expected — remove it from
  the fleet and add it again.

The card shows which of the three it is.

---

## After an update

Backpack takes a snapshot before every update and rolls back on its own if the
tunnels do not come back. If you are reading this, that either worked or you
want to do it by hand:

```
sudo backpack        # → Update → Rollback
```

If tunnels came back but something behaves differently, check that the two ends
are on the same version:

```
backpack version
```

A new end talking to an old one is supported and tested in both directions, but
a feature added on one side is not available until both have it.

---

## Reading the logs

```
journalctl -u backpack-<name> -f       # one tunnel, live
journalctl -u backpack-monitor -n 100   # the watchdog, the bot, the alerts
journalctl -u backpack-webui -n 100     # the panel
```

The engine logs at `info` by default. For a fault you are actively chasing,
raise it for that tunnel and watch:

```
sudo sed -i 's/^log_level = .*/log_level = "debug"/' /etc/backpack/<name>.toml
sudo systemctl restart backpack-<name>
journalctl -u backpack-<name> -f
```

Put it back to `info` afterwards. `trace` on a busy tunnel writes a line per
connection and will fill a journal quickly.

---

## When you are stuck

Collect this before asking anyone:

```
backpack version
backpack tunnel list
sudo backpack        # → Diagnose, and copy the output
journalctl -u backpack-<name> -n 200
```

The same four from **both** machines. A tunnel has two ends and nearly every
question about one of them is answered by the other.

---

<div dir="rtl">

## خلاصهٔ فارسی

به ترتیبِ اینکه هر کدام **چند وقت یک‌بار واقعاً جوابند** مرتب شده، نه به ترتیب
جذابیت. از بالا برو؛ بیشتر مشکل‌ها در دو بخش اول تمام می‌شوند. همهٔ این‌ها از
`sudo backpack` → **Health Check** و **Diagnose** هم در دسترس است.

### تونل می‌گوید بالاست ولی چیزی رد نمی‌شود

گران‌ترین خرابی این محصول، و فهرست علت‌هایش کوتاه است. خود Backpack این را
می‌بیند: watchdog دنبال حالتی می‌گردد که **یک جهت حرکت می‌کند و جهت دیگر یخ زده**
— شکلِ بیرونیِ یک stall — و قبل از هر restart خبر می‌دهد.

۱. **اول MTU.** از همهٔ بقیه رویِ‌هم بیشتر علت است و نامرئی است: handshake کوچک
است و رد می‌شود، اولین انتقال واقعی کامل است و نمی‌شود. `Diagnose` خط path MTU را
می‌دهد؛ اگر زیر ۱۵۰۰ بود، در `Manage tunnels → <tunnel> → TCP MSS clamp` به همان
اندازه clamp کن: **path MTU منهای ۴۰** برای IPv4 و **منهای ۶۰** برای IPv6. اگر
probe جواب نداد، از ۱۴۰۰ شروع کن و ۲۰ تا ۲۰ تا پایین بیا. برای تونل لایه‌۳
به‌جایش `auto_mtu = true` بگذار تا خودش پیدا کند.

۲. **سرویسِ آن‌طرف.** تونل هر اتصال را یک قدم جلوتر تحویل می‌دهد؛ اگر آنجا چیزی
گوش نمی‌دهد، هر اتصال یک قدم بعدِ تونل می‌میرد و خود تونل کاملاً سالم است. روی
سرور **خارج**: `ss -tlnp | grep <port>`. خود Backpack هم وقتی ببیند می‌گوید —
`last hop ... refused` یعنی سرویس بالا نیست، `timeout` معمولاً یعنی فایروال روی
همان ماشین. این دو راه‌حلشان فرق دارد و برای همین کلمه‌اش نوشته شده.

۳. **دو طرف باید بخوانند.** هر تنظیمی که جفتی است باید یکی باشد و ناهماهنگی دقیقاً
همین خرابی را می‌دهد: وصل، بدون عبور. `token`، `transport`، پورت، و برای لایه‌۳
`carrier`، `encap`، `fec_data`/`fec_parity` و `profile`. اگر آن‌طرف سروری است که
پنل مدیریتش می‌کند، اصلاً فایل‌ها را مقایسه نکن — جفت را از خود پنل بساز، چون
**هر دو سر** را می‌نویسد.

۴. **ببین واقعاً چه حمل می‌کند:** `backpack tunnel status <name>`. بایت ورودی و
خروجی جدا شمرده می‌شوند و نکته همین است: **یکی بالا برود و دیگری یخ باشد** همان
stall است؛ **هر دو یخ** یعنی تونل بیکار است، که خرابی نیست.

### تونل اصلاً وصل نمی‌شود

**اگر هیچ‌وقت وصل نشده،** مشکل تنظیمات است نه شبکه. اول
`backpack check -c /etc/backpack/<name>.toml`، بعد به همین ترتیب: توکن یکی باشد
(رایج‌ترین اشتباه، و هیچ پیامی اسمش را نمی‌برد — سرور رد می‌کند و هیچ لاگی نمی‌گوید
چرا، چون گفتنش به مهاجم می‌گوید نزدیک شده)، پورت یکی باشد، ترنسپورت یکی باشد، و
فایروال سرور ایران پورت تونل را باز داشته باشد.

**اگر قبلاً وصل می‌شده و حالا نه،** چیزی در مسیر عوض شده نه در فایل. `Diagnose`
بگیر. اگر آدرس در دسترس نیست ولی سرور بالاست، احتمالاً IP فیلتر شده. دو جواب، هر
دو داخلی: **آدرس‌های پشتیبان** برای IP فیلترشده، و **زنجیرهٔ fallback ترنسپورت**
برای وقتی خودِ *حامل* بلاک شده. اگر نمی‌دانی کدام است: `nc -vz <ip> <port>` از
بیرون ایران جواب بدهد و از داخل ندهد یعنی آدرس فیلتر است؛ از هر دو جواب بدهد و
تونل بالا نیاید یعنی حامل.

### تونل مدام ری‌استارت می‌شود

اول `sudo backpack → Alerts` — ری‌استارت‌های پیاپی به‌صورت **یک** وضعیت گزارش
می‌شوند نه بیست خط جدا. علت‌های رایج به ترتیب: **MTU** (مسیری که پکت کامل را دور
می‌ریزد، سشن را روی اولین انتقال واقعی می‌کشد)، **keepalive خیلی تنگ** (روی مسیر
بد بالاتر ببر)، و **ری‌استارت‌شدنِ آن‌طرف** — قبل از دست‌زدن به این ماشین آن یکی را
ببین.

### پنل باز نمی‌شود

`systemctl status backpack-webui` و `journalctl -u backpack-webui -n 50`. پنل زیر
یک **مسیر مخفی** جواب می‌دهد و جای دیگری نه، پس bookmark بدون آن ۴۰۴ می‌گیرد نه
صفحهٔ ورود. مسیر در `sudo backpack → Web Panel` نوشته شده.

### سرور مدیریت‌شده offline است

پنل خودش SSH می‌زند و چیزی روی آن‌طرف باز نمی‌شود، پس offline یعنی یکی از سه چیز:
ماشین خاموش است یا پورت SSH عوض شده؛ رمز عوض شده؛ یا host key عوض شده (پنل کلیدِ
اولین‌بار را pin می‌کند و کلید دیگری را رد می‌کند — اگر سرور از نو ساخته شده،
همین درست است: از ناوگان حذفش کن و دوباره اضافه کن). خود کارت می‌گوید کدام است.

### بعد از آپدیت

قبل از هر آپدیت snapshot گرفته می‌شود و اگر تونل‌ها برنگردند خودش برمی‌گردد.
دستی: `sudo backpack → Update → Rollback`. اگر تونل‌ها برگشتند ولی رفتار فرق
دارد، نسخهٔ دو طرف را با `backpack version` مقایسه کن — نسخهٔ جدید با قدیم کار
می‌کند و در هر دو جهت تست شده، ولی قابلیتی که یک طرف اضافه کرده تا وقتی هر دو
نداشته باشند در دسترس نیست.

### خواندن لاگ

```
journalctl -u backpack-<name> -f
journalctl -u backpack-monitor -n 100
journalctl -u backpack-webui -n 100
```

پیش‌فرض `info` است. برای خطایی که دنبالش هستی موقتاً `log_level = "debug"` بگذار و
بعد برش گردان. `trace` روی تونل شلوغ برای هر اتصال یک خط می‌نویسد و journal را
سریع پر می‌کند.

### وقتی گیر کردی

قبل از پرسیدن از کسی این چهارتا را جمع کن — **از هر دو ماشین**:
`backpack version`، `backpack tunnel list`، خروجی `Diagnose`، و
`journalctl -u backpack-<name> -n 200`. تونل دو سر دارد و تقریباً هر سؤالی دربارهٔ
یک سر را آن یکی جواب می‌دهد.

</div>

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v1.8.4.*

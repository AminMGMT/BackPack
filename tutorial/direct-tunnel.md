# Setting up a direct tunnel

The tunnel every other page describes is **reverse**: the Iran server listens,
and the kharej server dials in. This one is the other way round — Iran dials
out — and the ports do not move: Iran still exposes them, kharej still holds
the real service.

**Reach for it when:** the reverse tunnel will not come up. The kharej side
connects, or seems to, and the Iran side never sees it; or the provider filters
connections arriving from abroad; or the tunnel port is blocked inbound but
outbound is fine. An outbound connection from Iran to abroad is the ordinary
direction and the one least likely to be touched.

**Cost:** the kharej server now needs an open inbound port instead of Iran.

> Read [Before you start](before-you-start.md) first. The two roles, the token
> and the firewall work exactly the same here.

---

## The one thing that is different

You start from the same two menu entries either way — **Setup Iran** and
**Setup Kharej** — and each one asks which direction you want before anything
else:

```
1) Setup Iran     →  Reverse  |  Direct
2) Setup Kharej   →  Reverse  |  Direct
```

Pick the machine you are sitting on, then **Direct**. Everything after that
follows from those two answers, so the two sides never get the same set of
questions.

---

## Do the kharej side first

It is the side that listens, so it has to be up before Iran has anything to
dial — and it is where the token is generated, which you then carry across.

```
sudo backpack
→ 2) Setup Kharej
```

| Question | Answer |
|---|---|
| Which direction? | **Direct** |
| What kind of tunnel? | **Forwarded ports** |
| How should the tunnel travel? | **Stealth** (see below) |
| Tunnel port to listen on | `8443`, or anything unremarkable |
| Listen on IPv6 as well | `n` unless you need it |
| Tunnel name | press Enter |
| Security token | press **Enter** to take the suggested one |

**Copy the token it prints.** You need it verbatim on the other machine.

Notice what it does *not* ask: there is no port list. Every target arrives on
the connection that wants it, so what is forwarded is decided entirely on the
Iran side. Changing your forwarded ports later never involves this machine.

Open the tunnel port inbound:

```bash
ufw allow 8443/tcp
```

---

## Then the Iran side

```
sudo backpack
→ 1) Setup Iran
```

| Question | Answer |
|---|---|
| Which direction? | **Direct** |
| What kind of tunnel? | **Forwarded ports** |
| How should the tunnel travel? | the **same** as kharej |
| Kharej server address | the kharej server's IP or domain |
| Tunnel port on the kharej server | `8443` |
| Tunnel name | press Enter |
| Security token | **paste the token from kharej** |
| Ports to expose here | `443`, or whatever your users connect to |
| Carry UDP as well as TCP | see [Adding UDP](udp-forwarding.md) |
| How should the tunnel be tuned? | **Turbo**, the default |
| Fine-tune the advanced settings by hand | `n` |

Both wizards ask in the order the reverse one does — transport, address, name,
token, ports, tuning — so nothing is in an unfamiliar place if you have set up
a reverse tunnel before. The last question is the only one that hides anything:
saying `n` takes the preset's tuning and leaves both caps off, which is what
almost every tunnel wants. Saying `y` asks about session count and the
connection and bandwidth caps; see [Limits](../docs/limits.md).

The wizard prints a summary, you confirm, and it writes the config and starts
the service.

Open the **forwarded** ports on Iran — but not the tunnel port, because nothing
connects to it here:

```bash
ufw allow 443/tcp
```

---

## Which transport

The wizard describes each by what it is for. In short:

| | Take it when |
|---|---|
| **Stealth** | the default. Encrypted, no fingerprint, nothing for DPI to match |
| **TCP** | the payload is already encrypted (Xray, TLS) and nothing is filtering |
| **WSS** | you are putting a CDN like Cloudflare in front of the kharej server |
| **WS** | something in the path insists on seeing plain HTTP |

Both ends must choose the same one.

**On WSS:** if you are connecting straight to an IP, answer the domain question
with nothing — a certificate is generated automatically and the token is what
authenticates the tunnel. Only give a domain when a CDN is involved, and then
set up Let's Encrypt on the kharej side, because naming a domain also turns
certificate checking on.

---

## Checking it worked

On either machine:

```bash
sudo backpack
→ 3) Manage → Manage Tunnels
```

Your tunnel is listed with role `iran` or `kharej` and transport
`direct/stealth`. Green means the two ends have found each other.

The log tells you the same thing in one line:

```
journalctl -u backpack-<name> -f
```

**Iran**, when it is working:

```
direct: forwarding :443 -> 127.0.0.1:443
direct: session 0 established with 203.0.113.9:8443
```

**Kharej**:

```
direct: origin listening on 0.0.0.0:8443 (stealth)
direct: session established with 198.51.100.4:41022
```

Then connect to `443` on the Iran server as a user would.

---

## When it does not

**`session 0 could not be established: ... EOF — retrying`**
The tokens differ, or the transports differ. A wrong token looks exactly like a
firewall problem, because the kharej side answers a bad handshake with silence
by design. Check both files:

```bash
grep -E 'token|transport' /etc/backpack/*.toml
```

**`session 0 could not be established: ... connection refused` / timeout**
Nothing is listening, or the port is closed. On kharej: is the service running,
and is `8443/tcp` open inbound at the firewall *and* at the provider?

**The tunnel is up, but users get nothing**
The tunnel is fine and the far end is not. A bare `443` means kharej hands it
to its own `127.0.0.1:443` — so something must be listening there. If your
service is on another port, say so: `443=127.0.0.1:2096`. See
[Behind a panel](behind-a-panel.md).

**Only some things work; large downloads stall**
That is an MTU symptom, and it belongs to the [layer-3
tunnel](l3-direct-tunnel.md), not this one. A direct tunnel forwards streams
and has no MTU of its own.

---

## Changing it later

**Manage → Manage Tunnels → your tunnel → Edit**

The Iran side can change its forwarded ports and the UDP switch, and can show
the token again to copy across. The kharej side has nothing to change — which
the screen says, rather than presenting an empty form.

---

## A private network instead

If you want the two servers to reach each other by address and carry anything —
protocols with no ports, routing, ICMP — that is the other option under
**Direct → Full IP tunnel**. It creates a network interface rather than
forwarding ports.

**→ [Direct layer-3 tunnel](../docs/l3-direct-tunnel.md)**

---

<div dir="rtl">

## خلاصهٔ فارسی

تونل **مستقیم** برعکس تونل معکوس است: به‌جای اینکه سرور خارج به ایران وصل شود،
**سرور ایران به خارج وصل می‌شود**. پورت‌ها جابه‌جا نمی‌شوند — ایران همچنان
پورت‌ها را باز می‌کند و خارج سرویس واقعی را دارد.

**کِی استفاده کن:** وقتی تونل معکوس بالا نمی‌آید. مثلاً سرویس‌دهنده اتصال‌های
ورودی از خارج را فیلتر می‌کند، یا پورت تونل ورودی بسته است ولی خروجی باز است.
اتصال خروجی از ایران به خارج جهت عادی است و کمتر دستکاری می‌شود.

**هزینه‌اش:** حالا سرور خارج باید یک پورت ورودی باز داشته باشد، نه ایران.

### راه‌اندازی

هر دو ماشین از همان دو گزینهٔ همیشگی ساخته می‌شوند — **Setup Iran** و
**Setup Kharej** — و هرکدام قبل از هر چیز می‌پرسد کدام **جهت** را می‌خواهی:
معکوس یا مستقیم. ماشینی که رویش نشسته‌ای را انتخاب کن، بعد **Direct**.

**اول سمت خارج** را بساز، چون آن طرف گوش می‌دهد و توکن هم آنجا ساخته می‌شود:
گزینهٔ **Setup Kharej ← Direct**، نوع **Forwarded ports**، ترنسپورت **Stealth**، پورت `8443`،
و توکن پیشنهادی را با Enter قبول کن. **توکن را کپی کن.** بعد پورت را باز کن:
`ufw allow 8443/tcp`.

دقت کن که از خارج **لیست پورت نمی‌پرسد** — هر مقصدی روی همان اتصالی که می‌خواهدش
می‌رسد، پس تصمیم دربارهٔ پورت‌ها فقط سمت ایران گرفته می‌شود.

**بعد سمت ایران:** گزینهٔ **Setup Iran ← Direct**، همان ترنسپورت، آدرس و پورت سرور خارج،
پورت‌هایی که می‌خواهی باز شوند (مثلاً `443`)، و **توکن را از خارج پیست کن**.
روی ایران فقط پورت‌های forward‌شده را باز کن، نه پورت تونل را.

### ترنسپورت‌ها

**Stealth** پیش‌فرض است: رمزنگاری‌شده و بدون اثر انگشت. **TCP** وقتی خودِ
ترافیک از قبل رمز است. **WSS** وقتی CDN مثل کلادفلر جلوی سرور خارج می‌گذاری.
**WS** فقط جایی که مسیر حتماً باید HTTP ساده ببیند. هر دو طرف باید یکی را
انتخاب کنند.

دربارهٔ WSS: اگر مستقیم به IP وصل می‌شوی، سوال دامنه را خالی بگذار — گواهی
خودکار ساخته می‌شود و چیزی که تونل را احراز می‌کند توکن است. دامنه را فقط وقتی
بده که CDN در کار باشد، و آن‌وقت روی خارج Let's Encrypt را هم تنظیم کن، چون
دادن دامنه بررسی گواهی را روشن می‌کند.

### وقتی کار نکرد

پیام `EOF — retrying` یعنی توکن یا ترنسپورت دو طرف فرق دارد. توکن اشتباه دقیقاً
شبیه مشکل فایروال دیده می‌شود، چون سمت خارج عمداً به هندشیک نامعتبر جواب نمی‌دهد.

پیام `connection refused` یا timeout یعنی چیزی گوش نمی‌دهد یا پورت بسته است.

اگر تونل بالاست ولی کاربر چیزی نمی‌گیرد، مشکل آن‌طرف است نه تونل: `443` خالی
یعنی خارج آن را به `127.0.0.1:443` خودش می‌دهد، پس باید چیزی آنجا گوش بدهد.
اگر سرویست روی پورت دیگری است، صریح بنویس: `443=127.0.0.1:2096`.

### شبکهٔ خصوصی به‌جای پورت

اگر می‌خواهی دو سرور با آدرس همدیگر را ببینند و هر چیزی را حمل کنند — پروتکل‌های
بدون پورت، مسیریابی، ICMP — گزینهٔ دیگر همان منو را بزن: **Full IP tunnel**.
آن یکی به‌جای forward کردن پورت، یک اینترفیس شبکه می‌سازد.

</div>

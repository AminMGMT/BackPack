# Setting up an IP Spoofing tunnel

This carrier writes its own IP packets and stamps a **fake source address** on
them, so what leaves the machine does not look like it came from there. It is for
a path that blocks, throttles or counts by address.

It is **experimental**, and it only carries anything where the network above your
machine forwards packets with a forged source. Plenty of providers drop them, and
there is no way to tell from the outside — you have to measure it.

**Requirements:** Linux, root, on **both** ends.

> This page is the **walkthrough**. Every individual setting, including the ones
> the wizard does not ask about, is documented in
> **[docs/ip-spoofing.md](../docs/ip-spoofing.md)**.

---

## The plan

Do it in this order. It is designed so you always have a working tunnel and never
have to debug two unknowns at once:

1. **Build the tunnel with no forgery.** Press Enter through the forged-source
   question. You get an ordinary working tunnel on your real address.
2. **Run the tester** to find which forged sources actually cross the path.
3. **Set one of those** from the Edit menu.

Doing it the other way round gives you a tunnel that connects and carries
nothing — which looks exactly like every other fault there is.

---

## Part 1 — build it unforged

On both machines: `sudo backpack` → Setup Iran / Setup Kharej → Reverse →
**`Experimental` → `IP Spoofing`**.

After the usual questions the wizard runs a **4-step** spoof screen.

### Step 1 — what the packets look like on the wire

```
Use UDP — the recommended profile [Y/n]
```

Say yes. The forged packets can be dressed as UDP, as ping (ICMP), or as a TCP
flow; UDP is the plainest and passes nearly everywhere. The other two are for a
path that filters UDP specifically.

**The other end must pick the same profile.**

```
Set the two directions separately [y/N]
```

`N`. A path *can* filter one direction differently — uplink is kharej → Iran,
downlink is Iran → kharej — but almost none does, and both ends have to be set
the same way.

### Step 2 — where this end sends its replies

**On the Iran server only**, and not optional:

```
Real IPv4 of the kharej server:
```

The kharej machine forges its source, so its packets do not say where they came
from. This server has to be told, or it has nowhere to send the answers. Enter
the **real public IPv4** — the address you SSH into it with, never a forged one.

On the kharej client there is nothing to answer: it dialled the Iran server, so
it already knows where to send.

### Step 3 — the address to forge

```
Forged source IPv4 (empty = do not forge):
```

**Leave it empty.** Nothing is forged, the tunnel comes up on this machine's real
address and works normally. That is the right answer for a first run.

(Several addresses, comma separated, are rotated one per session — that is what
gets past a block that counts by address. Come back for that in Part 3.)

If the machine has **more than one** network interface you are also asked which
one the raw packets leave by. Empty lets the kernel route, which is right unless
you know it picks wrong.

### Step 4 — Relay mode, instead of a KCP tunnel over forwarded ports

```
Enable relay mode [y/N]
```

`N` unless you are already running WireGuard (or another tunnel) and want it
carried whole. Relay mode strips KCP and runs a bare datagram relay to a local
UDP target instead of the forwarded ports — the inner transport brings its own
reliability and encryption, so there is no KCP underneath, and **the forwarded
ports are ignored**. Both ends have to be in the same mode.

The wizard then prints a summary of what this end will do, and what must be true
on the other server. Read it — everything in a spoof setup is paired, and the
pairing is the part that goes wrong.

**Confirm the tunnel works now**, before forging anything:
`Manage → Status`, then actually pass traffic through a forwarded port.

---

## Part 2 — find a forged source that survives the path

```
sudo backpack  →  3. Manage  →  IP Spoofing Tester
```

It is a two-node test. **Start the receiver first.**

### On the node that should *receive* (usually the Iran server)

Choose **Receiver**:

| Prompt | Answer |
|---|---|
| Shared token | anything, as long as the sender uses the same string |
| Listen UDP port | `45000` (default) — open it in the firewall |
| Probes the sender emits per IP | `5` |
| Capture window (seconds) | `30` — long enough for the sender to finish |
| Report IPs with loss% at or below | `20` |
| Write passing IPs to file | optional, e.g. `/root/passing.txt` |

### On the node that should *send* (usually the kharej server)

Choose **Sender** (needs root):

| Prompt | Answer |
|---|---|
| Shared token | the same string |
| Receiver node's REAL IPv4 | the Iran server's real address |
| Receiver's listen UDP port | `45000` |
| Probes per candidate IP | `5` |
| Candidate IPs | a list, range or CIDR — see below |
| Send interface | empty |

Candidates can be `1.0.0.0/24`, `8.8.4.4`, `9.9.9.0-9.9.9.255`, several of those
comma-separated, or `@/path/to/file` with one spec per line.

### Reading the result

The receiver prints a table:

```
  SPOOF_IP           ARRIVED    LOSS%
  1.0.0.4            5/5        0.0
  8.8.4.4            0/5        100.0
```

Anything that arrived at or below your loss threshold passed. **`0/5` everywhere
means your provider drops forged packets** — the transport cannot work from that
machine, and no setting will change it.

Then **swap the roles and run it again** to map the other direction. The two
directions are independent: an address that gets from kharej to Iran says nothing
about the reverse.

---

## Part 3 — set the source that passed

```
sudo backpack  →  3. Manage  →  Manage Tunnels  →  <tunnel>  →  Edit  →  IP Spoofing
```

Choose **Forged source IP(s)** and enter an address the tester says arrives — or
several, comma separated, to rotate one per session. The tunnel restarts on the
new settings.

Then, on the **other** end's IP Spoofing screen, set **Fingerprint & evasion →
"Expected forged source from the other end"** to the same address, so it knows
what to expect.

Do the same for the reverse direction with the addresses that passed that way.

**Nothing here is proven until traffic actually crosses.** If the tunnel comes up
but carries nothing, the forged source is being dropped — go back to the tester.

---

## The rest of the settings

Everything else — per-direction profiles, the egress interface, TTL jitter, DSCP
randomisation, port shuffling, padding, fake TLS, fragmenting, socket buffers,
relay mode — lives under **Edit → IP Spoofing** and is documented field
by field in **[docs/ip-spoofing.md](../docs/ip-spoofing.md)**.

All of them are **off by default and none is needed for a working tunnel**. Each
costs something: bandwidth, CPU, or a shape that a different filter notices
instead. Change one at a time and test.

---

## Troubleshooting

| Symptom | Cause |
|---|---|
| Tunnel connects, carries nothing | the forged source is being dropped — run the tester |
| Nothing at all, even unforged | profile mismatch between the ends, or the Iran side has no "other end's real IPv4" |
| Iran side refuses to save any setting | `The other end's real IPv4` is empty — fill it first |
| Tester: everything `0/5` | your provider drops forged sources; this transport is not available to you |
| Worked, then stopped | if you rotate a pool, one of the addresses may have started being dropped — retest the pool |

---

<div dir="rtl">

## خلاصهٔ فارسی

**IP Spoofing** خودش پکت‌های IP را می‌سازد و روی آن‌ها یک **آدرس مبدأ جعلی**
می‌زند، تا آنچه از سرور بیرون می‌رود شبیه ترافیک همان سرور نباشد. مخصوص مسیری که
بر اساس آدرس مسدود یا محدود می‌کند. **آزمایشی است**، لینوکس و root روی **هر دو**
طرف می‌خواهد، و فقط جایی کار می‌کند که شبکهٔ بالادست پکت با مبدأ جعلی را عبور
دهد — خیلی از سرویس‌دهنده‌ها این کار را نمی‌کنند.

**ترتیب درست کار (مهم):**

۱. **اول تونل را بدون جعل بساز.** در مرحلهٔ «Forged source» فقط Enter بزن. تونلی
سالم روی آدرس واقعی خودت می‌گیری. مطمئن شو کار می‌کند.

۲. بعد `Manage → IP Spoofing Tester` را اجرا کن: روی یک سرور **Receiver** (اول
این را استارت کن) و روی سرور دیگر **Sender**. لیست/رنج/CIDR آدرس‌های کاندید را
بده. جدول خروجی نشان می‌دهد کدام آدرس‌ها واقعاً رسیده‌اند. اگر همه `0/5` بود،
یعنی سرویس‌دهنده‌ات پکت جعلی را دور می‌ریزد و این ترنسپورت برایت کار نمی‌کند.
بعد جای دو نقش را عوض کن تا جهت برعکس را هم بسنجی.

۳. آدرسی که رد شده را از `Manage → Edit → IP Spoofing → Forged source IP(s)`
ست کن (چند آدرس با کاما = هر session یکی). در طرف مقابل هم در بخش
«Fingerprint & evasion» گزینهٔ «Expected forged source from the other end» را
همان بگذار.

**نکات ویزارد:** پروفایل پکت را روی **UDP** بگذار و دو طرف باید یکی باشد. روی
سرور **ایران** حتماً باید «آی‌پی واقعی سرور خارج» را وارد کنی، وگرنه سرور جایی
برای فرستادن جواب ندارد و هیچ تنظیمی ذخیره نمی‌شود. حالت **relay** را
`N` بگذار مگر بخواهی کل وایرگارد (یا تونلی دیگر) را حمل کنی (در آن حالت KCP و
پورت‌های forward نادیده گرفته می‌شوند).

بقیهٔ تنظیمات (TTL jitter، DSCP، shuffle پورت، padding، fake TLS، تکه‌تکه کردن و…)
همه پیش‌فرض خاموش‌اند و برای کار کردن تونل لازم نیستند — توضیح تک‌تکشان در
[docs/ip-spoofing.md](../docs/ip-spoofing.md) است. هر بار فقط یکی را عوض کن و
تست بگیر.

</div>

---
[← Back to the tutorials](README.md)

# Checking a heavily filtered path

A failed **Connection Test coordinator** is not a verdict on every tunnel.
The coordinator fetches settings and results over a temporary TCP/UDP port.
If that port is blocked, the two sides cannot start the measurement even when
ICMP can carry a working tunnel. First check that the Iran test is still open,
the copied address is correct, and its control port is reachable.

For an ICMP-only path, test **Setup Iran → Direct → xDi** and its paired Kharej
configuration separately. Select the full IP tunnel if you need routed traffic;
forwarded ports can use the same carrier. Use the same token and keep the default
automatic MTU and MSS clamp. ICMP has no TCP/UDP carrier port to open.
Both machines need Linux, root/CAP_NET_RAW and a working TUN device.

An independent two-server check on 2026-10-09 carried encrypted xDi traffic
while a separate Backhaul tunnel kept running. A five-minute check with a
1280-byte tunnel MTU returned 297/300 and 298/300 pings, with average RTTs of
80 and 83 ms. Six 8 MiB random-file round trips completed with matching SHA-256
hashes. The ordinary Iran-dials/Kharej-listens setup also connected and its
automatic MTU probe selected 1434 bytes. These are bounded measurements of one
path, not a promise of future availability or a default MTU for every network.

A second five-minute test with the ordinary direction and automatic MTU returned
300/300 pings in each direction. Twelve 8 MiB file round trips (six initiated
from each server, 192 MiB of payload in total) had matching hashes. Measured
download rates were 8.47–23.75 Mbit/s into Iran and 31.72–53.73 Mbit/s into Kharej
while existing services were active. Connection Test also reported Direct xDi
as `ok`, with 60/60 echoes.

That Connection Test exposed a separate local issue: ports picked on Iran were
already occupied by unrelated Xray UDP/TCP sockets on Kharej. New peers choose
the temporary direct listener ports on Kharej and announce them before joining;
older peers retain the original ports. This removes known clashes at selection
time, while the operating system still decides the final bind. Setup failures
reported by Kharej appear as skipped cases on both machines rather than evidence
of a blocked carrier. Check local logs if a listener cannot start.

## An isolated manual check

Choose an unused interface and private subnet on **both** machines. The example
uses `bpcheck0` and `10.233.151.0/30`; check for collisions before using them.
Replace `KHAREJ_IP` and `SAME_RANDOM_TOKEN`. Generate one strong token and copy it
to both files. Store each file with mode `0600`.

Iran:

```toml
[l3]
mode = "dial"
addr = "KHAREJ_IP:49093"
token = "SAME_RANDOM_TOKEN"
carrier = "xdi"
iface = "bpcheck0"
local_ip = "10.233.151.1/30"
peer_ip = "10.233.151.2"
mtu = 1280
auto_mtu = false
```

Kharej:

```toml
[l3]
mode = "listen"
addr = "0.0.0.0:49093"
token = "SAME_RANDOM_TOKEN"
carrier = "xdi"
iface = "bpcheck0"
local_ip = "10.233.151.2/30"
peer_ip = "10.233.151.1"
mtu = 1280
auto_mtu = false
```

Start the Kharej first, then Iran, using a separate terminal on each host:

```sh
sudo timeout --signal=TERM 600 backpack -c /root/bp-check.toml
```

On Iran, `ping -I bpcheck0 -c 300 -i 1 10.233.151.2` checks five minutes of
delivery. Transfer a random file over the private tunnel address and compare
SHA-256 at both ends; a successful ping alone does not establish throughput.
The example forwards no public ports, changes no default route and uses a
temporary foreground process. On exit, BackPack removes its TUN and its own
tag-scoped firewall rules. Keep existing tunnel services running.

The fixed 1280-byte MTU is a conservative starting point for this manual IPv4
check. Once it works, the setup wizard can probe the actual MTU automatically.
If xDi also fails, compare packet arrival at both public interfaces, verify
matching tokens and direction, and inspect the engine logs before concluding
that a new protocol is needed.

<div dir="rtl">

## خلاصهٔ فارسی

خطای ارتباط با هماهنگ‌کنندهٔ Connection Test فقط یعنی ارتباط کنترل تست روی
TCP/UDP برقرار نشده است؛ نتیجهٔ همهٔ پروتکل‌ها نیست. روی مسیرهایی که ICMP
عبور می‌کند، xDi را از بخش Direct جداگانه تست کنید. توکن دو طرف باید یکی باشد.
برای بررسی مستقل، از رابط و subnet آزاد استفاده کنید و تونل فعلی را نگه دارید.
موفقیت چند دقیقه تست، تضمین پایداری دائمی در برابر تغییرات مسیر یا فیلترینگ نیست.

</div>

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v1.9.0.*

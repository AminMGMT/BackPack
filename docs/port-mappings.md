# Port mappings

What `ports = [...]` accepts, in full. The syntax is the same on the reverse
tunnel, the [direct tunnel](direct-tunnel.md) and the [layer-3
tunnel](l3-direct-tunnel.md), so a list of mappings moves between them unchanged.

A mapping has two halves: **where this machine listens**, and **what the far end
forwards it to**.

```
85.11.12.13:443 = 127.0.0.1:2053
└──── listen ───┘ └──── target ───┘
```

Only the listening half is optional in its address — the port is always
required. A mapping with no `=` uses the same port on both sides.

## Every form

| Mapping | Effect |
|---|---|
| `443` | listen on `:443`, forward to the far end's own `127.0.0.1:443` |
| `443=2096` | a different port at the far end |
| `443=127.0.0.1:2096` | the same thing, written out |
| `443=10.0.0.5:8443` | an explicit host the far end can reach |
| `443=10.0.0.1:80\|10.0.0.2:80` | two backends, health-checked and balanced |
| `10000-10009` | a range, each port to the same port |
| `10000-10009=20000-20009` | a range, preserving the offset |
| `127.0.0.1:443=2096` | bind the listener to one local address |
| `85.11.12.13:443=127.0.0.1:2053` | bind to one public address |
| `85.11.12.13:10000-10009` | a range bound to one local address |
| `[2a01:4f8::1]:443=127.0.0.1:2053` | an IPv6 bind address, bracketed |

Several mappings are comma separated: `443,8080,2053-2060`.

> **A bare port is not "any port".** `443` means the service must actually be
> listening on `127.0.0.1:443` at the far end. If your panel is on 2096 there,
> say so: `443=127.0.0.1:2096`. Setup prints the resolved list before it builds
> anything — read it.

## Binding to one local address

With no address in front of it, a listener binds every interface — `0.0.0.0`,
which on a dual-stack host also accepts IPv6. Naming an address pins that
listener to it. Two cases want this:

**A multi-homed server.** Two public IPs, and you want the control channel on
one and the exposed ports on the other. Without a bind address both land on the
wildcard, so if they share a port number the second listener fails with `bind:
address already in use` — which is the usual way this gets discovered, when
someone wants the control channel on 443 to look like HTTPS *and* a forwarded
443 for users.

```toml
bind_addr = "85.11.12.13:443"                        # control channel, first IP
ports     = ["85.11.12.14:443=127.0.0.1:2053"]       # users, second IP
```

`bind_addr` takes the same `host:port` form, and so does the **Tunnel port**
field everywhere it is asked for — the setup wizard, the CLI's edit screen, and
the panel's create and edit forms all accept `85.11.12.13:443` as well as a bare
`443`. A port on its own still means every interface, which is what every
existing tunnel has.

A few details worth knowing:

- The "listen on IPv6 as well" switch only chooses between the `0.0.0.0` and
  `::` wildcards. Naming an address answers that question already, so the switch
  is ignored for a pinned tunnel.
- Changing only the port on a tunnel that is pinned keeps it pinned. To widen it
  back to every interface, say so: `0.0.0.0:443`.
- A **client** tunnel binds nothing — its tunnel port is the port on the server —
  so it takes a port alone, and an address there is refused rather than ignored.
- The address must be one this machine holds. The CLI warns if it is not on any
  interface right now (a floating or not-yet-configured address is legitimate,
  so this is a warning, not a refusal), and if the bind does fail the log says
  `this server does not have that address` and points at `ip -brief address`.

**Keeping a port off the public internet.** `127.0.0.1:8080=8080` exposes the
mapping to this machine only — useful for something a local reverse proxy
terminates.

### What binding does not fix

It does **not** fix asymmetric routing on a host with two NICs, and it is worth
being explicit because this is the usual reason people go looking for it.

A wildcard-bound listener does not reply from the wrong source address. An
accepted TCP socket takes its local address from the destination of the incoming
SYN, so a connection arriving on the second IP already replies from the second
IP, bound or not.

What actually breaks is the **egress route** lookup: the reply leaves by
whichever interface holds the lowest-metric default route, carrying a source
address that does not belong to that interface, and then `rp_filter` or the
upstream provider drops it. That is a kernel routing decision, not a socket one.
It needs `ip rule` and a routing table per NIC, and no amount of bind-address
configuration replaces it.

## Ranges

`10000-10009` opens ten listeners. Both ends are inclusive, and ports 1 and
65535 are valid.

Paired with a target range of the same width, the offset is preserved:
`10000-10009=20000-20009` sends `10000` to `20000` and `10009` to `20009`.
A range may carry a bind address, which applies to every port in it.

Wide ranges cost a listener and a goroutine each, so they open a millisecond
apart rather than all at once.

## Several backends

A target may name more than one address, separated by `|`:

```toml
ports = ["443=10.0.0.1:80|10.0.0.2:80"]
```

Connections are spread round-robin over the backends that answer. A backend that
fails three consecutive checks drops out of the rotation and returns the moment
it answers again, so one dead service behind the tunnel no longer takes the
tunnel down with it. The separator is `|` because `,` already separates whole
mappings.

A single backend is never health-checked, so ordinary tunnels behave exactly as
before.

On a **layer-3** tunnel the choice is by load instead: each new connection goes
to the backend with the fewest connections open, and one whose dial fails is
passed over for 20 seconds. That is what lets one port be spread over several
kharej servers — see [several kharej behind one Iran server](l3-direct-tunnel.md#several-kharej-servers-behind-one-iran-server).

## UDP

Forwarded ports carry TCP only until you ask for UDP as well:

```toml
accept_udp = true
```

It applies to every mapping in the list, on every transport. See [Forwarded
UDP](forwarded-udp.md) for what it does and does not cover.

## Where each half resolves

| | Reverse tunnel | Direct tunnel | Layer-3 tunnel |
|---|---|---|---|
| Listener | Iran server | Iran server | Iran server |
| Target with no host | kharej's `127.0.0.1` | kharej's `127.0.0.1` | `peer_ip` |

A target host is always resolved **by the far end**, on its own network — which
is what makes `443=10.0.0.5:8443` reach a machine this side cannot see.

---

<div dir="rtl">

## خلاصهٔ فارسی

هر چیزی که `ports = [...]` قبول می‌کند. نحوش روی تونل معکوس، [تونل
مستقیم](direct-tunnel.md) و [تونل لایه‌۳](l3-direct-tunnel.md) یکی است، پس یک
فهرست mapping بدون تغییر بین‌شان جابه‌جا می‌شود.

هر mapping دو نیمه دارد: **این ماشین کجا گوش می‌دهد** و **آن‌طرف آن را به کجا
می‌فرستد**:

```
85.11.12.13:443 = 127.0.0.1:2053
└──── گوش می‌دهد ───┘ └──── مقصد ───┘
```

فقط *آدرسِ* نیمهٔ گوش‌دهنده اختیاری است؛ پورت همیشه لازم است. mappingای که `=`
ندارد، همان پورت را روی هر دو طرف می‌گذارد. چند mapping را با `,` جدا کن:
`443,8080,2053-2060`.

**پورت خالی یعنی «همان پورت»، نه «هر پورتی».** `443` یعنی آن‌طرف واقعاً باید روی
`127.0.0.1:443` گوش بدهد. اگر پنلت آنجا روی ۲۰۹۶ است بنویس
`443=127.0.0.1:2096`. Setup قبل از ساختن، فهرست حل‌شده را چاپ می‌کند — بخوانش.

**بستن به یک آدرس محلی:** بدون آدرس، listener روی همهٔ اینترفیس‌ها می‌نشیند
(`0.0.0.0`، که روی هاست دوپشته IPv6 را هم قبول می‌کند). نوشتن آدرس آن را به همان
یکی سنجاق می‌کند. برای IPv6 آدرس را داخل `[]` بگذار.

**بازه‌ها:** `10000-10009` ده listener باز می‌کند و هر دو سر بازه شامل‌اند. با
بازهٔ مقصدِ هم‌عرض، فاصله حفظ می‌شود: `10000-10009=20000-20009` یعنی ۱۰۰۰۰ به
۲۰۰۰۰ و ۱۰۰۰۹ به ۲۰۰۰۹. بازهٔ پهن برای هر پورت یک listener و یک goroutine خرج
می‌کند، برای همین یک میلی‌ثانیه فاصله باز می‌شوند.

**چند backend:** مقصد می‌تواند چند آدرس داشته باشد که با `|` جدا شده‌اند —
`"443=10.0.0.1:80|10.0.0.2:80"`. اتصال‌ها round-robin بین آن‌هایی که جواب می‌دهند
پخش می‌شود؛ backendای که سه بار پشت سر هم رد شود از چرخش بیرون می‌رود و به‌محض
جواب‌دادن برمی‌گردد. جداکننده `|` است چون `,` خودش mappingها را جدا می‌کند. یک
backend تنها اصلاً health-check نمی‌شود، پس تونل‌های معمولی دقیقاً مثل قبل‌اند.

**UDP:** پورت‌های forward فقط TCP حمل می‌کنند تا وقتی `accept_udp = true` بگذاری.
روی همهٔ mappingها و همهٔ ترنسپورت‌ها اعمال می‌شود.

**هر نیمه کجا resolve می‌شود:** listener همیشه روی سرور ایران است. مقصدِ بدون
host روی تونل معکوس و مستقیم یعنی `127.0.0.1` خارج، و روی لایه‌۳ یعنی `peer_ip`.
هاستِ مقصد **همیشه آن‌طرف** و روی شبکهٔ خودش resolve می‌شود — و همین است که
`443=10.0.0.5:8443` را به ماشینی می‌رساند که این طرف اصلاً نمی‌بیندش.

</div>

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v1.8.2.*

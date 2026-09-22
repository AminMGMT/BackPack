# Architecture

What BackPack is made of, and which part answers which question. The topic
guides beside this one explain how to do things; this one explains where things
are.

---

## One binary, seven modes

`main.go` dispatches before anything else is decided:

| Invocation | What runs |
|---|---|
| `backpack` | the interactive management menu — `internal/menu` |
| `backpack -c <file>` | **engine mode**: one tunnel, from one config |
| `backpack --webui` | the web panel — `internal/webui` |
| `backpack --monitor` | watchdog, Telegram bot, alerts, history — `internal/monitor` |
| `backpack --proxy` | the built-in SOCKS5/HTTP proxy — `internal/localproxy` |
| `backpack --restart-all` / `--telegram-report` | one-shot jobs, run from cron |
| `backpack node exec <base64>` | the one operation a panel runs on a managed server over SSH |

Everything except engine mode is management. Engine mode is the product.

**One process per tunnel.** Each tunnel is its own systemd unit running
`backpack -c /etc/backpack/<name>.toml`. This is the single most valuable
reliability property in the system and it is free: a panic, a leak or a bad
config affects exactly one tunnel, and systemd restarts exactly that one. Any
proposal to run several tunnels in one process is trading it away.

---

## Three engines

`cmd/cmd.go runEngine` chooses, in this order, and the order is load-bearing:

```
        ┌─ [l3] table present?    ──yes──►  internal/tunnel/l3       layer 3
config ─┼─ [direct] table present? ─yes──►  internal/tunnel/direct   layer 4, dials out
        └─ [server]/[client]      ────────►  internal/server + internal/client
                                             the reverse tunnel
```

They share **no configuration key and no code**. A file that does not mention
`[l3]` cannot reach the layer-3 engine; one that does never reaches the reverse
tunnel. `L3Config.Enabled()` is one trimmed string, and it is the whole gate.

### The reverse tunnel — `[server]` / `[client]`

The Iran side listens and exposes ports; the kharej side dials out. A control
channel plus a pool of data connections. Ten transports: `tcp`, `tcpmux`,
`stealth`, `ws`, `wss`, `wsmux`, `wssmux`, `kcp`, `quic`, `udp`, plus `xdi` and
`pck` which ride the KCP stack over raw and packet sockets.

- `internal/server` / `internal/client` — pick the transport, build its config
- `internal/server/transport` / `internal/client/transport` — the transports
- `internal/utils/handlers` — the forwarding itself: buffered relay, and kernel
  `splice` behind a flag

### The direct tunnel — `[direct]`

The same forwarded ports, dialled the other way round: Iran dials out, so it
needs no inbound port of its own. `internal/tunnel/direct`.

### The layer-3 tunnel — `[l3]`

An interface on each host carrying whole IP packets. Noise NNpsk0, explicit
nonces, a 2048-bit replay window (RFC 4303, as WireGuard uses). Six carriers
underneath: `udp`, `quic`, `pck`, `sni`, `xdi`, `spoof`. `internal/tunnel/l3`.

A reliable carrier is deliberately not an option here — stacking retransmission
under a reliable tunnel is actively harmful, not merely wasteful. The package
doc explains why.

---

## The seam between the engines and everything watching them

This is the piece worth knowing before changing anything.

```
  engine process                    every other process
  ──────────────                    ───────────────────
  internal/metrics  ──writes──►  /etc/backpack/<name>.metrics.json
                                        │
                                        ├──►  the panel
                                        ├──►  the Telegram bot
                                        ├──►  the CLI
                                        └──►  the watchdog
```

Nothing reaches into a running tunnel. The engine writes a JSON snapshot every
thirty seconds — traffic, peer, pool state, whether the control channel is up,
and what the process is holding (goroutines, descriptors, heap) — and every
other process reads that file.

It is why the watchdog asks the engine rather than the kernel: a socket outlives
the tunnel it belongs to by a long way, and every failure the watchdog used to
miss looked healthy in the socket table.

---

## Management

| Package | Responsibility |
|---|---|
| `internal/manage` | the real work: wizards, edit, backup, restore, update, migrate, diagnose, speed test, presets. 27,302 lines, and the seam the panel and the CLI both call |
| `internal/menu` | the interactive TUI. One 1,400-line file with no tests and no non-interactive entry point, which is why nothing can drive it |
| `internal/webui` | the panel: a Go mux plus a vanilla-JS SPA under `panel/`, served beneath a random secret base path. Also currently owns the fleet |
| `internal/telegram` | bot, alerts, scheduled reports. Has a read-only admin tier |
| `internal/node` | the fleet: JSON over the managed server's own SSH, with a closed list of operations |
| `internal/monitor` | the always-on service: watchdog, bot, alerts, history sampler, auto-backup — each in its own supervised goroutine |

`internal/manage` is the seam. The panel and the CLI are two callers of the same
functions, which is why a setting can be changed wherever it can be chosen.

### The fleet

The panel dials **out** over the managed server's own SSH. The far side needs no
agent, no daemon and no state — `backpack node exec <base64>` performs one
operation from a closed list and prints the answer. The list is the security
boundary: there is no operation that runs a command, reads a path or installs a
binary.

---

## Update and recovery

```
latest tag ──► signature over SHA256SUMS ──► checksum of the archive
                       │                              │
                    refuse                         refuse
                       ▼                              ▼
              snapshot ──► install ──► migrate ──► restart ──► health check
                                                                    │
                                                              failed? roll back
```

`internal/manage/update.go`, with `migrate.go` as the mechanism that forces a
correction onto a server an older version set up — idempotent, no marker file,
run on every update.

---

## Support packages

`internal/utils/network` (88 files) is where the unusual work lives: Noise,
nonce pools, KCP, uTLS, the spoof and pck carriers, ICMP, outbound routing,
endpoints, TLS config.

`internal/tunnel/portmap` parses the forwarded-port syntax all engines share.
`internal/tunnel/limits` and `internal/server/transport/limits.go` are the
per-tunnel caps. `internal/tunnel/mssclamp` is the MSS clamp.
`internal/metrics`, `internal/tunhist`, `internal/alerthist` are the three
stores. `internal/sysstat`, `internal/geo`, `internal/optimize`,
`internal/schedule`, `internal/debugserver` are the small ones.

---

## Where to start reading

- **A tunnel does not come up** → `cmd/cmd.go`, then the transport under
  `internal/server/transport`
- **Something about the panel** → `internal/webui/server.go` for the routes,
  `panel/js/api.js` for every call the page makes
- **Something about setup or editing** → `internal/manage`
- **Layer-3** → `internal/tunnel/l3/doc.go`, then `session.go` and `tunnel.go`

The package comments carry the reasoning — what was tried, what it cost, why the
obvious alternative is wrong. They are the best documentation in the project and
they are worth reading before changing the code they sit on.

---

<div dir="rtl">

## خلاصهٔ فارسی

این صفحه می‌گوید Backpack از چه ساخته شده و کدام بخش به کدام سؤال جواب می‌دهد.
بقیهٔ راهنماها می‌گویند «چطور کاری را بکنی»؛ این یکی می‌گوید «چیزها کجایند».

**یک باینری، هفت حالت.** `main.go` قبل از هر چیز دیگری تصمیم می‌گیرد: `backpack`
منوی مدیریت است؛ `backpack -c <file>` **حالت موتور** است یعنی یک تونل از یک
کانفیگ؛ `--webui` پنل وب؛ `--monitor` نگهبان و ربات تلگرام و هشدارها و تاریخچه؛
`--proxy` پراکسی داخلی SOCKS5/HTTP؛ `--restart-all` و `--telegram-report` کارهای
یک‌باره‌ای که از cron اجرا می‌شوند؛ و `backpack node exec <base64>` تنها عملیاتی
که پنل روی یک سرور مدیریت‌شده از طریق SSH اجرا می‌کند. **هر چیزی جز حالت موتور،
مدیریت است. حالت موتور، خودِ محصول است.** هر تونل یک سرویس systemd جداگانه و یک
پروسهٔ جداگانه دارد.

**سه موتور،** و `cmd/cmd.go runEngine` به همین ترتیب انتخاب می‌کند: اگر جدول
`[l3]` باشد → موتور لایه‌۳؛ وگرنه اگر `[direct]` باشد → تونل مستقیم لایه‌۴؛
وگرنه `[server]`/`[client]` → تونل معکوس. **هیچ کلید کانفیگ و هیچ کدی بین‌شان
مشترک نیست.** فایلی که `[l3]` ندارد اصلاً به موتور لایه‌۳ نمی‌رسد، و فایلی که
دارد هیچ‌وقت به موتور معکوس نمی‌رسد.

**درزِ بین موتورها و هر چیزی که تماشایشان می‌کند** — مهم‌ترین چیزی که قبل از
تغییر دادن هر چیزی باید بدانی: **هیچ‌چیز دستش را داخل یک تونل در حال اجرا
نمی‌کند.** موتور هر سی ثانیه یک snapshot از جنس JSON می‌نویسد — ترافیک، peer،
وضعیت استخر، بالابودن کانال کنترل، و آنچه پروسه نگه داشته (goroutine، descriptor،
heap) — و هر پروسهٔ دیگری همان فایل را می‌خواند: پنل، ربات تلگرام، CLI و نگهبان.
برای همین است که نگهبان از خودِ موتور می‌پرسد نه از کرنل: یک سوکت خیلی بیشتر از
تونلی که به آن تعلق دارد زنده می‌ماند، و هر خرابی‌ای که نگهبان قبلاً از دست
می‌داد در جدول سوکت‌ها سالم به‌نظر می‌رسید.

**از کجا شروع به خواندن کنی:** تونل بالا نمی‌آید → `cmd/cmd.go` و بعد ترنسپورت
زیر `internal/server/transport`. چیزی دربارهٔ پنل → `internal/webui/server.go`
برای مسیرها و `panel/js/api.js` برای هر فراخوانی صفحه. چیزی دربارهٔ راه‌اندازی یا
ویرایش → `internal/manage`. لایه‌۳ → `internal/tunnel/l3/doc.go` و بعد
`session.go` و `tunnel.go`.

**کامنت‌های سر پکیج‌ها استدلال را دارند** — چه چیزی امتحان شده، چه هزینه‌ای داشته،
و چرا جایگزینِ بدیهی غلط است. بهترین مستندات این پروژه همان‌ها هستند و ارزشش را
دارند که قبل از تغییرِ کدی که رویشان نشسته خوانده شوند.

</div>

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v1.8.2.*

# Shipping logs off the server

Backpack logs to stdout, systemd puts that in journald, and on one server
`journalctl -u backpack-<name>` is the whole story. With five servers it is five
journals, and the question "which tunnel dropped at 03:10 last night" needs five
answers stitched together.

This page is the other half: how to turn the logs into something a collector can
read, and what the fields are called.

## Turning it on

Set `log_format = "json"` on the tunnel — in the CLI it is a question during
Fine Tune (*Write logs as JSON*), in the panel it is **Edit → Fine Tune → JSON
logs**, and in the file it sits beside `log_level`:

```toml
[server]
log_level  = "info"
log_format = "json"
```

Each line becomes one JSON object:

```json
{"host":"iran-1","level":"info","msg":"server started successfully, listening on address: 0.0.0.0:8443","role":"server","time":"2026-09-22T03:10:22+03:30","transport":"wss","tunnel":"fr-relay"}
```

The default stays the coloured text format on purpose: most of the time these
logs are read by a person on the server they are already logged into, and JSON
is worse for that.

## The fields

These names are a stable interface. A dashboard, an alert rule or a line in a
runbook is written against them, so they are documented here and pinned by a
test — `internal/utils/logident_test.go` fails if one is renamed.

| Field | What it is |
| --- | --- |
| `time` | RFC 3339, with the server's offset |
| `level` | `trace`, `debug`, `info`, `warn`, `error`, `fatal`, `panic` |
| `msg` | The message. Free text — match on the fields, not on this |
| `tunnel` | The tunnel's name: its configuration file's name without the extension, the same name the CLI, the panel and the metrics snapshot use |
| `role` | `server` or `client` on a reverse tunnel; `iran-edge` or `kharej-edge` on a layer-3 one |
| `transport` | The carrier in use — `wss`, `quic`, `kcp`, …; prefixed `l3-` or `direct-` on the other two engines |
| `host` | The machine's hostname, so two servers running a tunnel of the same name can be told apart |

The last four are what make a shipped line findable, and they are why shipping
is worth doing at all: without them, five journals in one place are one stream
in which no line says where it came from.

A message may carry extra fields of its own. Where one uses a name from the
table above it means it — a line about a transport rotation says which transport
it rotated *to*, and that wins over the process's own.

**All three engines honour `log_format`**: the reverse tunnel, the direct
tunnel, and layer-3.

## Shipping it

Backpack does not ship logs itself, and
[design-decisions](design-decisions.md) says why: journald plus an existing
collector already does it, for anyone who wants it, without a line of code here.
What this product owes is logs worth collecting. Two recipes, for the two most
common collectors — both read journald, so neither needs Backpack to write a
file.

**Promtail** (into Loki):

```yaml
scrape_configs:
  - job_name: backpack
    journal:
      labels: { job: backpack }
    relabel_configs:
      - source_labels: ['__journal__systemd_unit']
        regex: 'backpack-.*'
        action: keep
      - source_labels: ['__journal__hostname']
        target_label: 'host'
    pipeline_stages:
      - json:
          expressions: { tunnel: tunnel, role: role, transport: transport, level: level }
      - labels: { tunnel: '', role: '', transport: '', level: '' }
```

**Vector**:

```toml
[sources.backpack]
type = "journald"
include_units = ["backpack-fr-relay", "backpack-de-relay"]

[transforms.parsed]
type = "remap"
inputs = ["backpack"]
source = '. |= object!(parse_json!(.message))'
```

With either, the queries that were five journals become one:

```
{job="backpack", level="error"}                  every error in the fleet
{job="backpack", tunnel="fr-relay"} |= "restart" one tunnel's restarts
{job="backpack", transport="quic", level="warn"} one carrier, everywhere
```

## What not to expect

- **Backpack does not retry or buffer.** It writes to stdout; journald and the
  collector own everything after that. A collector that is down loses nothing
  as long as the journal still holds the lines — size it with
  `SystemMaxUse` in `journald.conf`.
- **The message text is not an interface.** It is written for a person and it
  changes. Alert on `level`, `tunnel` and `transport`; if you need a specific
  event, say so and it can be given a field of its own.
- **There is no trace context.** Backpack is one hop carrying opaque bytes, so
  there is no span structure to record. See
  [design-decisions](design-decisions.md).

---

<div dir="rtl">

## خلاصهٔ فارسی

Backpack روی stdout لاگ می‌نویسد، systemd آن را در journald می‌گذارد، و روی یک
سرور `journalctl -u backpack-<name>` همهٔ داستان است. با پنج سرور می‌شود پنج
journal، و سؤال «کدام تونل دیشب ساعت ۳:۱۰ افتاد» پنج جواب می‌خواهد که باید به هم
دوخته شوند.

**روشن‌کردنش:** روی تونل `log_format = "json"` بگذار — در CLI یک سؤال داخل Fine
Tune است، در پنل `Edit → Fine Tune → JSON logs`، و در فایل کنار `log_level`
می‌نشیند. هر خط یک شیء JSON می‌شود. پیش‌فرض عمداً همان قالب رنگیِ متنی می‌ماند، چون
بیشتر وقت‌ها این لاگ‌ها را آدمی می‌خواند که روی همان سرور لاگین است و JSON برای آن
بدتر است.

**فیلدها:** `time` (RFC 3339 با آفست سرور)، `level`، `msg`، و چهار تای اصلی:
`tunnel` (نام تونل، یعنی نام فایل کانفیگش بدون پسوند — همان نامی که CLI، پنل و
snapshot متریک استفاده می‌کنند)، `role` (روی تونل معکوس `server` یا `client`؛ روی
لایه‌۳ `iran-edge` یا `kharej-edge`)، `transport` (حاملِ در استفاده، با پیشوند
`l3-` یا `direct-` روی آن دو موتور دیگر) و `host` (نام میزبان، تا دو سروری که
تونلی هم‌نام دارند از هم قابل تشخیص باشند).

**این نام‌ها یک interfaceاند، نه جزئیات پیاده‌سازی.** یک داشبورد، یک قانون هشدار
یا یک خط در runbook کسی روی همین‌ها نوشته می‌شود؛ عوض‌کردن یکی‌شان همهٔ آن‌ها را در
همان آپدیت می‌شکند بدون اینکه جایی خطایی بدهد. برای همین اینجا مستند شده و یک
تست هم آن را قفل کرده. **هر سه موتور** `log_format` را رعایت می‌کنند.

**فرستادنش:** Backpack خودش لاگ نمی‌فرستد و
[design-decisions](design-decisions.md) می‌گوید چرا: journald به‌علاوهٔ یک
collector موجود همین کار را می‌کند، بدون یک خط کد در این مخزن. چیزی که این محصول
بدهکار است، لاگی است که *ارزش* جمع‌کردن داشته باشد. دو نسخه برای promtail و
vector در بالا آمده؛ هر دو journald را می‌خوانند.

**چیزی که انتظارش را نداشته باش:** Backpack بافر یا retry نمی‌کند — بعد از stdout
همه‌چیز دست journald و collector است. **متن پیام interface نیست**؛ برای آدم نوشته
شده و عوض می‌شود، پس هشدارت را روی `level` و `tunnel` و `transport` بگذار.

</div>

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v1.8.3.*

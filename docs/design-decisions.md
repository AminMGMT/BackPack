# Design decisions

What Backpack deliberately does not do, and why.

Most of this file is a list of things that were asked for, considered
seriously, and turned down. That is the useful half of a roadmap: the features
on the list get built and stop being interesting, while the reasons for the
ones that were refused have to be re-derived every time somebody proposes them
again. Each entry below states a reason an engineer can argue with, not a
preference.

It is not a closed list. "Rejected" here means *against the product as it is*;
where a decision would flip on new information, the entry says what that
information would be.

## The three facts every decision below rests on

**One binary, one tunnel, one systemd unit.** The engine process serves a
single tunnel. Fault isolation comes free: a panic, a leak or a bad config
takes down exactly one tunnel and systemd restarts exactly that one.

**The data path carries opaque bytes.** Backpack does not look at what it
carries, and several decisions below are downstream of refusing to start.

**One maintainer, ~104k lines of Go, no runtime dependencies.** A feature is
judged on whether it earns its maintenance, not on whether a competitor has
it. `go build` needs nothing but the Go toolchain, and that is a property
worth defending.

## The licence, which decides what can be sold

Backpack is AGPL-3.0, and `NOTICE` records that part of the data plane derives
from prior AGPL/GPL work — so the copyright is not held outright and a closed
build cannot be shipped or relicensed. AGPL §13 adds the network clause on top.
What remains sellable is a *service*, support, and early access with source.
Anything in this repository that assumes a proprietary edition is a mistake, not
a plan.

## Rejected, with the reason

Seventeen proposals, in identifier order.

### API-05 — Typed client SDK

A published Go/TypeScript client library.

**Rejected for now.** With OpenAPI (API-02) a client is generated in one
command by whoever needs one, in whatever language they use. Publishing and
versioning a hand-maintained SDK is a permanent obligation for a user base that
writes shell scripts. Revisit only if there is evidence of real third-party
integration.

### API-06 — gRPC control API

A binary RPC surface alongside REST.

**Rejected.** The consumers are a browser and shell scripts. gRPC adds protobuf
codegen to a build that currently needs nothing but `go build`, and buys
performance on a control plane that handles a few requests a minute. The node
RPC already has a working answer for machine-to-machine: JSON over SSH, with a
closed op list.

### ARC-05 — One engine binary, many tunnels

Running several tunnels in one process to save resources.

**Rejected.** The current arrangement gives fault isolation for free: a panic,
a leak or a bad config affects exactly one tunnel, and systemd restarts exactly
that one. The saving is a few megabytes of RSS per tunnel on machines that are
not memory-bound. Trading the single best reliability property in the system
for that is a bad deal.

### HAV-02 — Active/active with state synchronisation

Two servers serving the same tunnel simultaneously with shared session state.

**Rejected.** A tunnel session is cryptographic state with a nonce counter and
a replay window; sharing it between two hosts means either a consensus protocol
or a nonce-reuse bug, and nonce reuse in ChaCha20-Poly1305 is catastrophic
rather than degraded. Load balancing across endpoints (HAV-01) already gives
the availability benefit without touching session state. This is the clearest
case on the page of a feature that sounds like maturity and is actually a
footgun.

### HAV-03 — Distributed control plane with quorum

Several panels agreeing on fleet state through consensus.

**Rejected.** The fleet is tens of servers managed by one or two people over
SSH. Raft buys nothing here and introduces a quorum that can be lost — a new
outage mode for a control plane whose current failure mode is 'the panel is
down and the tunnels keep running'. That property is worth more than consensus.
Panel resilience is a backup-and-restore problem (OPS-07), not a
distributed-systems one.

### INT-02 — OpenTelemetry traces

Distributed tracing through the tunnel.

**Rejected.** Tracing answers 'which service in the chain was slow', and
BackPack is one hop carrying opaque bytes — there is no span structure to
record. OTel *metrics* would duplicate the Prometheus endpoint. The cost is a
large dependency tree in a binary that currently has sixteen direct
requirements and installs as a single static file.

### INT-03 — Ansible and Terraform modules

Provisioning BackPack from existing infrastructure-as-code.

**Rejected as first-party.** These are thin wrappers over the API, they belong
in their own repositories with their own release cycles, and the target user
runs `bash <(curl ...)` on a VPS rather than a Terraform plan. Build API-01
well and anyone who wants a module can write forty lines. Reconsider only if
enterprise adoption appears.

### INT-04 — Loki-compatible log export

Shipping logs to a central store.

**Rejected as a built-in.** journald plus promtail already does this for anyone
who wants it, without a line in this repo. What BackPack should do instead is
make its logs worth shipping — that is OBS-04, structured logs with a stable
schema.

*Done, as of v1.8.2.* Every JSON line now carries the tunnel, its role, its
transport and the host, the field names are documented and pinned by a test, and
[shipping logs off the server](log-schema.md) carries a promtail and a vector
recipe. The decision stands: the shipping is still somebody else's collector.

### INT-05 — Docker image

Running BackPack in a container.

**Rejected.** The engine needs `CAP_NET_RAW` and `CAP_NET_ADMIN` for the raw
and packet-socket carriers, creates TUN devices, writes sysctls, manages
iptables rules and installs systemd units. A container that can do all of that
is a container with `--privileged --net=host`, which is a worse deployment of
the same thing with an extra layer to debug.

### INT-06 — Kubernetes operator

Managing tunnels as cluster resources.

**Rejected, firmly.** Every reason from INT-05 applies, plus: the users run
individual VPSes, the fleet transport is SSH, and there is no cluster in the
picture. This would be a second product sharing a name.

### MISC-05 — Per-user quotas and billing

Accounting traffic per end user, with limits and invoices.

**Rejected.** BackPack carries opaque bytes for a proxy that sits behind it; it
has no concept of a user and cannot acquire one without inspecting traffic it
deliberately does not look at. The panels that do this (Marzban, and the like)
sit at the proxy layer where the identity actually exists. Adding it here would
mean either a user database BackPack cannot populate or traffic inspection that
contradicts the product.

### MISC-06 — Built-in DNS or ad-blocking

Resolving or filtering DNS inside the tunnel.

**Rejected.** Out of scope, well served by existing software, and it would put
BackPack in the position of inspecting traffic. The l3 engine carries whatever
the kernel routes; pointing that kernel at a resolver is the operator's
one-line job.

### MISC-07 — Plugin or scripting engine in the data path

Lua or WASM hooks for custom packet handling.

**Rejected.** A scripting runtime on a per-packet path costs latency on every
packet to serve a decision that changes rarely, adds an unbounded failure
surface inside a process that must not crash, and makes every performance
property unpredictable. Any genuine need here is better met by a config key
with a name and a test.

### MISC-08 — Windows or macOS engine

Running the tunnel engine off Linux.

**Rejected.** The value proposition is a pair of Linux VPSes. Half the
transports could not exist elsewhere, and the management layer would need a
second implementation. A client for another platform is a different product.

### MISC-09 — Desktop GUI client

A native application for managing tunnels.

**Rejected.** Three interfaces already exist and the users are server operators
who reach servers over a network. A fourth surface would compete with the panel
for maintenance and lose.

### MISC-10 — Machine-learning traffic classification or evasion

A model deciding how to shape traffic to avoid detection.

**Rejected.** The current knobs are the right design precisely because they are
legible: an operator can reason about what a packet will look like and test it.
A model produces behaviour nobody can explain, cannot be reproduced when it
fails, and would be trained on data that does not exist. DIF-01's aggregate
evidence is the honest version of this idea.

### NET-07 — Domain and application-aware routing

Routing by hostname or by which application produced the traffic.

**Rejected.** Domain routing needs DNS interception or SNI inspection on every
flow; application awareness needs a client agent on the originating host.
BackPack sits at the edge, carrying traffic that arrives already anonymous — it
has neither the DNS view nor the process view. This is Xray/sing-box's job, and
those already sit behind a BackPack tunnel in the normal deployment. Adding it
here duplicates a mature tool badly.

## Decisions recorded elsewhere

Not everything belongs here. Three decisions live next to the code they
constrain, because that is where somebody about to undo them will be looking:

- **Profile-guided optimisation is not used**, and the profile that says why is
  in [performance notes](performance-notes.md): 59% of a loaded tunnel is
  syscalls and 78% of the layer-3 path is hand-written crypto assembly, so
  there is nothing for the compiler to win.
- **The layer-3 handshake gains a freshness timestamp only at wire version 2**,
  because an old responder compares the initiator payload whole and would reject
  a new dialler. The reasoning is in `internal/tunnel/l3/tunnel.go`, at
  `handleInit`.
- **QUIC starts from a 1232-byte packet rather than quic-go's 1280**, because
  the default cannot cross a 1280-byte path at all. The measurement is at
  `network.QUICInitialPacketSize`.

---

<div dir="rtl">

## خلاصهٔ فارسی

این صفحه فهرست کارهایی است که Backpack **عمداً نمی‌کند**، و دلیل هر کدام.

بیشترش فهرست پیشنهادهایی است که جدی بررسی و رد شده‌اند. این همان نیمهٔ مفیدِ یک
roadmap است: چیزهایی که ساخته می‌شوند دیگر جالب نیستند، ولی دلیلِ ردکردنِ بقیه هر
بار که کسی دوباره پیشنهادشان می‌کند باید از نو ساخته شود. «رد شده» یعنی **در
برابر محصولی که هست**؛ جایی که تصمیم با اطلاعات تازه عوض می‌شود، خود همان بند
می‌گوید آن اطلاعات چیست.

**سه واقعیتی که همهٔ تصمیم‌ها رویش ایستاده‌اند:** یک باینری، یک تونل، یک unit
سیستمی — جداسازی خطا مجانی می‌آید، چون panic یا نشتی یا کانفیگ بد دقیقاً یک تونل
را می‌خواباند و systemd دقیقاً همان را برمی‌گرداند؛ مسیر داده **بایت مبهم** حمل
می‌کند و به محتوا نگاه نمی‌کند، و چند تصمیمِ این فهرست نتیجهٔ همین نگاه‌نکردن است؛
و یک نگه‌دارنده با حدود ۱۰۴ هزار خط Go و بدون وابستگی زمان اجرا، پس هر قابلیت با
معیارِ «هزینهٔ نگه‌داریش را درمی‌آورد؟» سنجیده می‌شود نه «رقیب دارد؟».

**مجوز، که تعیین می‌کند چه چیزی قابل فروش است:** Backpack با AGPL-3.0 منتشر شده
و `NOTICE` ثبت می‌کند که بخشی از مسیر داده از کار قبلیِ AGPL/GPL گرفته شده — پس
کپی‌رایت به‌طور کامل در اختیار نیست و نه می‌شود نسخهٔ بسته ساخت و نه مجوز را عوض
کرد. مادهٔ ۱۳ AGPL هم بند شبکه را اضافه می‌کند. آنچه می‌ماند: **سرویس**،
پشتیبانی، و دسترسی زودهنگام همراه با سورس.

</div>

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v1.8.3.*

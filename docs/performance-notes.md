# Performance notes

Measurements that closed a question, kept because the question comes back.

Each of these was an idea that sounded right. Two of them are not shipped, and
the reason they are not is a number rather than an opinion — which is the point
of writing them down: without this file the same idea is proposed again next
year and measured again from scratch.

## Where the time actually goes

Two profiles, taken 2026-09-22.

**A tunnel under sustained load** (the soak: 650,000 round trips through a real
tunnel over loopback):

```
59%  syscalls
 9%  runtime.nanotime
 4%  runtime.futex
```

**The layer-3 data path on its own** (seal, parse, open, at full MTU):

```
62%  chacha20poly1305Seal      (assembly)
16%  chacha20poly1305Open      (assembly)
 3%  session.sealKind          (Go)
 6%  allocation
```

So the product spends its time in the kernel and in hand-written crypto
assembly. That single fact decides most of what follows.

## Profile-guided optimisation: measured, not shipped

Go's PGO works by using a profile to guide inlining and devirtualisation of
**Go** code. Given the two profiles above there is very little Go code to guide:
the tunnel is syscalls, and the data path is assembly in `x/crypto` that the Go
compiler does not touch.

Measured anyway, with the data-path profile as `default.pgo`, three runs each:

| | without | with |
|---|---|---|
| seal, full MTU | 856 ns/op | 883 ns/op |
| full round trip | 1725 ns/op | 1690 ns/op |

Both differences are inside run-to-run variance, and they point in opposite
directions. There is no gain here to ship, and a committed `default.pgo` is a
file that has to be regenerated and re-justified on every release.

**Revisit if** the balance changes — a pure-Go crypto path on an architecture
without assembly, or a workload where the Go relay code dominates rather than
the kernel.

## splice: measured, not turned on by default

`splice` saves a copy between two sockets and the kernel.

| | throughput |
|---|---|
| buffered | 8,419 Mbit/s |
| splice | 10,813 Mbit/s |

+28%, on loopback, for plain `tcp` with no bandwidth limit — and loopback
*understates* it, because the copy splice avoids is cheap there and the syscall
overhead dominates. A gain here is conservative; a loss would have been damning.

It is still off by default, and that is the recorded decision rather than an
absence of one. A synthetic gain shows the implementation works, not that it
should become everybody's default on upgrade. What would justify flipping it is
the same measurement on a real link. Reproduce with:

```
go test ./internal/e2e/ -run TestSpliceThroughput -v -count=1
```

## recvmmsg: measured, shipped

40,000 datagrams of 1200 bytes at a loopback carrier:

| | received | rate |
|---|---|---|
| one at a time | 27,682 of 40,000 | 59 kpps |
| recvmmsg | 40,000 of 40,000 | 214 kpps |

The rate is the smaller half of that result. Reading one at a time, the receive
loop could not keep up with the sender and the socket dropped nearly a third of
the datagrams. A tunnel losing packets inside its own receive loop is
indistinguishable from a lossy path when you are looking at it from outside,
which is the most expensive kind of fault to chase.

Linux only, plain `udp` carrier only. Every other carrier reads as it did
before, including if the batch path refuses at runtime — losing the
optimisation costs throughput, never the tunnel.

## sendmmsg: measured, shipped, and the number is not the point

The transmit half of the same mechanism. 40,000 datagrams of 1200 bytes at a
loopback carrier, three runs each:

| | syscalls | rate (median of 3) |
|---|---|---|
| one at a time | 40,000 | 254 kpps |
| sendmmsg | 5,000 | 260 kpps |

**Eight times fewer syscalls for the same throughput.** Read that as the
expected shape rather than a disappointment: on loopback the syscall is not what
costs, the copy and the sink are, so removing seven eighths of the syscalls buys
nothing measurable here. It costs nothing either — and on a real NIC, where the
syscall and the driver path *are* the cost, the same change is what the receive
side measured as 59 → 214 kpps once that path was genuinely loaded.

It is shipped on those terms: same throughput, far less work, and the direction
of the remaining uncertainty is the one that favours it.

The send path was already reading a *batch* from the TUN — the kernel hands over
however many packets it had ready — and then writing each one with its own
syscall. The gather costs nothing to arrange: the packets are already there,
already sealed, already going to the same peer.

## UDP segmentation offload: measured, shipped, and the number *is* the point

`sendmmsg` removed seven eighths of the syscalls on the layer-3 send path and
bought no measurable throughput, which is the row above. The conclusion drawn
from it was the right one and it was only half the story: the cost is not the
syscall, it is **per datagram inside the kernel** — a copy into the socket
buffer and a walk down the protocol stack, once each, for every datagram.
Batching the calls does nothing about that.

`UDP_SEGMENT` does. One buffer and a segment size go in, the kernel cuts the
buffer up, and the copy and the stack walk happen once for the whole run.

40,000 datagrams of 1,200 bytes to a drained loopback sink, three runs, with an
unconnected socket and an explicit destination — the same shape the carrier has,
because a connected socket measures faster and would not be measuring the thing
that could ship:

| | syscalls | rate | vs sendmmsg |
|---|---|---|---|
| `sendmmsg`, 8 per call | 5,000 | ~255 kpps | — |
| `UDP_SEGMENT`, 8 per call | 5,000 | ~700 kpps | **+123% … +229%** |
| `UDP_SEGMENT`, 48 per call | 834 | ~1,190 kpps | +320% … +398% |

**The middle row is the one that decided it.** Same batch, same number of
syscalls, two to three times the rate — so the gain is the offload itself and
not the batch size, and it is worth having at the batch of eight this carrier
reads from the TUN today. The third row says what a larger read batch would be
worth on top, which is a separate change.

Shipped with two conditions, both in `internal/tunnel/l3/gso.go`:

- **Every segment but the last must be the same size.** That is the mechanism,
  not the implementation. A run of full-sized packets — a transfer through the
  tunnel — qualifies; the ragged traffic in between does not and takes the
  `sendmmsg` path exactly as before. The optimisation therefore applies where
  the tunnel is busiest, which is the right place for it.
- **Support is found out by trying.** It depends on the kernel, the family and
  the route, and there is no reliable way to ask first. The first refusal turns
  it off for the life of the socket and the same batch goes out the old way,
  so nothing is dropped and nothing keeps paying for a probe that has failed.

`TestGSOSendRate` is the measurement and it is kept, so the next person to ask
gets a number from their own machine.

## Batching the reverse relay: there is nothing to gather

The layer-3 carrier gained `recvmmsg` and `sendmmsg`, and the obvious next
question was the reverse relay: it copies connection by connection and nothing
gathers. The answer is that the gather has already happened, in the kernel,
before the relay sees the data.

Measured on a saturated stream — 256 MB through a relay loop the same shape as
`handlers.transferData`, with the same 64 KB buffer:

```
4,501 reads for 256 MB
  full 64 KB buffer   3,942 reads   87.6%
  8–16 KB               428 reads    9.5%
  everything else       131 reads    2.9%
```

Nearly nine reads in ten already come back with the buffer full. There is no
batching to add: one read and one write per 64 KB is the floor for a stream, and
`splice` removes even the copy between them where both ends are plain TCP.

The remaining tenth is the interesting part, and it is the argument against
doing this rather than for it. Those are the reads that returned early because
that is all that had arrived. Gathering them means holding the first chunk until
a second one turns up — buying a syscall with latency, on a path that is already
the slow part of somebody's connection, for traffic that is small precisely
because it is interactive.

What does help at this layer has already been done and is measured above: a
64 KB buffer rather than 16 KB (a quarter of the syscalls per gigabyte), pooling
it rather than allocating per direction per connection, and `splice` where the
operator asks for it.

## Automatic MSS discovery on the reverse tunnel: not built, and why

The layer-3 engine measures its own path (`auto_mtu`). The reverse tunnel does
not, and the obvious symmetry — have every TCP connection read the path MTU the
kernel knows for its route, derive the clamp from it, and apply it — was built
and then thrown away, because it does nothing.

Measured in a network namespace with loopback narrowed to 1280 bytes, which is
the only way to get a genuinely narrow link into a unit test:

```
lo MTU = 1280
IP_MTU (what the kernel knows about the route) = 1280
the clamp that would be derived from it        = 1228
TCP_MAXSEG on the dialled socket, no clamp set = 1228
TCP_MAXSEG on the accepted socket, no clamp set = 1228
```

The clamp derived from the kernel's figure is the figure the kernel had already
chosen. That holds in every case the feature would cover:

- **The local interface is narrow.** The kernel sizes its advertised MSS from
  the outgoing interface and gets it right without help.
- **A remote hop is narrow and answers.** The ICMP "fragmentation needed" reply
  lowers the kernel's path MTU, and `snd_mss` with it. `IP_MTU` reports the
  result rather than causing it.
- **A remote hop is narrow and is silent.** This is the fault the clamp exists
  for, and it is exactly the case where `IP_MTU` still reads the local
  interface's number. A clamp derived from it would be 1448 on a path carrying
  1240 — no better than none.

What actually finds the number on a silent path is the kernel's own TCP MTU
probing, which searches downward when a connection stalls with retransmits and
no ICMP. `internal/optimize/optimize.go` has set `net.ipv4.tcp_mtu_probing = 1`
since before this was asked, so the discovery the feature would have added is
already running, at the only layer that can see the evidence it needs.

The part that is genuinely missing is neither of these: it is that the operator
is never *told* the kernel had to back off. Health Check reads `snd_mss` from
the live sockets and compares it to the path (`internal/manage/diagnose_path.go`
— `fromKernel`), which is the right place for that to appear.

## What is measured on every build

The numbers above are one-offs. These are gates, and they fail the build:

- **Allocation counts** on the packet path and the relay buffer pool. Exact, not
  a maximum: deterministic, unaffected by the machine, and every one of those
  paths is meant to be allocation-free or close to it.
- **A wall-clock ceiling** at roughly 3× what each path measured when the
  ceiling was set. Generous on purpose — a shared CI runner varies by more than
  any tight threshold — and it still catches the failure that actually happens,
  which is a syscall or an allocation appearing in a per-packet loop. That is an
  order of magnitude, not a margin.

Both skip under `-race`, which multiplies wall-clock time by between five and
twenty. The allocation half still runs.

See `internal/tunnel/l3/bench_gate_test.go`.

---

<div dir="rtl">

## خلاصهٔ فارسی

این صفحه برای کسی است که می‌خواهد کارایی را بهتر کند: اندازه‌گیری‌هایی که یک
سؤال را بسته‌اند، نگه‌داشته شده چون آن سؤال برمی‌گردد.

هر کدام ایده‌ای بوده که درست به‌نظر می‌رسیده. چندتایشان **ساخته نشده‌اند** و
دلیلش یک عدد است نه یک نظر — و نکتهٔ نوشتنشان همین است: بدون این فایل همان ایده
سال بعد دوباره پیشنهاد و دوباره از صفر اندازه‌گیری می‌شود.

اعداد اصلی: تونل زیر بار سنگین **۵۹٪ syscall** است و مسیر دادهٔ لایه‌۳ **۷۸٪
اسمبلیِ ChaCha20-Poly1305** — برای همین PGO چیزی برای راهنمایی‌کردن ندارد، و
اندازه‌گیری هم همین را گفت. بافر رله از ۱۶ به ۶۴ کیلوبایت رفت (یک‌چهارمِ syscall
برای هر گیگابایت) و pool شد. `recvmmsg` و `sendmmsg` ساخته و شیپ شدند. `splice`
اندازه گرفته شد ولی پیش‌فرض خاموش ماند، چون سود روی loopback شاهدِ میدانی نیست.
و دو ایده — MSS خودکار روی تونل معکوس، و batching رلهٔ معکوس — ساخته یا اندازه
گرفته شدند و **بی‌اثر** بودند؛ هر دو با عددشان اینجا ثبت شده‌اند.

</div>

---
[← Back to the docs index](README.md)

---

*Last verified against Backpack v1.8.2.*

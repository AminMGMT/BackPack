# Contributing

## Before anything else: the licence

Backpack is **AGPL-3.0**, and `NOTICE` records that part of the data plane
derives from prior AGPL/GPL work. That has three consequences worth knowing
before you write anything:

- The project cannot be relicensed, by anyone, including its maintainer.
- AGPL §13 applies: if you run a modified version as a network service, the
  people using it are entitled to your source.
- `NOTICE` carries two additional terms permitted by AGPL §7. They ask that the
  attribution line be kept, and they decline to license the name. `TRADEMARK.md`
  says exactly what that covers — in short: **fork freely, rename if you
  redistribute**, and nominative use ("compatible with Backpack") is always
  fine.

You keep the copyright on what you write. There is no CLA.

## The bar

CI enforces all of this, so run it before you push — `make check` runs the
first five in one go:

```
gofmt -l .                      # must print nothing
go vet ./...
go test ./...
go test ./... -race             # enforced; the transports are concurrent
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

Coverage is measured with `-coverpkg` and has a floor that only goes up. If it
drops, find out what stopped being exercised before lowering it.

## Building with libpcap (the PCK carrier)

The `pck` carrier is [paqet](https://github.com/hanselime/paqet)'s engine
(`internal/tunnel/paqet`), and paqet captures packets with `gopacket/pcap`,
which is cgo and libpcap. That is the one place Backpack is not CGO-free, so a
build that is meant to *run* PCK needs libpcap's headers and a static
`libpcap.a`. Everything else builds and runs without them: a plain
`CGO_ENABLED=0 go build` still compiles, passes the engine's tests (they use a
fake wire) and says "this build has no libpcap" if a PCK tunnel is started.

No root is needed anywhere. Build libpcap into your home directory once:

```
make libpcap                       # -> ~/.local/libpcap  (LIBPCAP_PREFIX=... to move it)
```

That runs `scripts/build-libpcap.sh`: it downloads libpcap, builds it with
`--with-pcap=linux --disable-shared` and installs `include/pcap.h` and
`lib/libpcap.a`. libpcap's tarball does not ship its generated parser, so it
needs `flex` and `bison`; if they are not installed the script builds `m4`,
`bison` and `flex` from source into the same prefix first. Then point cgo at it
for anything that compiles the module — `go vet`, `go test`, `go build`:

```
export CGO_ENABLED=1
export CGO_CFLAGS="-I$HOME/.local/libpcap/include"
export CGO_LDFLAGS="-L$HOME/.local/libpcap/lib -lpcap"
go vet ./... && go test ./...
```

`make build`, `make check` and the other targets do that for you when the
prefix exists. A local `make build` links libpcap statically but uses the host
libc; it is for development, not for shipping.

**Release binaries** are one static file per architecture, as before, and must
run on a server with no libpcap. `make release-linux` builds each with a libpcap
built for that architecture (`scripts/build-linux.sh <arch>`), using
[Zig](https://ziglang.org)'s C compiler and its bundled musl as the cross
compiler — no containers, no sudo, byte-for-byte reproducible. It needs `zig` on
`PATH` (0.17.0 is what CI uses), plus `gcc-arm-linux-gnueabi` for `armv5`, which
Zig's musl cannot target. `-tags netgo,osusergo` keeps the pure-Go resolver, so
cgo does not change how names are resolved. To check one:

```
scripts/build-linux.sh amd64 /tmp/backpack
ldd /tmp/backpack                  # "not a dynamic executable"
go version -m /tmp/backpack | grep CGO_ENABLED     # CGO_ENABLED=1
```

CI (`.github/actions/libpcap`) builds libpcap once per run (cached) and exports
the three variables above for every job.

## How this codebase is written, and why it will look unusual

**The comments carry the institutional memory.** They say what was tried, what
it cost, and why the obvious alternative is wrong. Several bugs in this project
were found *because* a comment stated an invariant precisely enough to check.

When you change something that a comment explains, change the comment. When you
fix a bug, write down what it did — not "fixed nil check" but what a person
saw: which message, which screen, what they concluded from it. The next person
to read that code is usually trying to work out why it is the way it is.

**Tests are named after the behaviour, not the function.**
`TestAPairingThatIsNeverAnsweredTimesOutAndGivesBackItsSlot`, not `TestRun`. A
test's name should tell you what capability exists.

**A guard that scans source is a legitimate test here.** Several exist —
`TestEveryConfigKeyReachesCodeThatActsOnIt`, `TestEveryPanelAPIFunctionIsCalled`,
`TestEveryPairingLoopTimesOutAndCleansUpOnShutdown`. They catch the class of bug
this codebase actually produces: a feature wired on one side only. Three unwired
entry points were found by hand before the last of those existed.

`TestEveryPanelCallReachesAHandlerThatReadsWhatItSends` is the panel's version:
it runs `panel/js/api.js` under node, records every request, and holds each one
against the route it reaches and the fields that handler reads. A new export in
`api.js` needs a sample call in `internal/webui/paneltest/apicalls.mjs`, with
the arguments its view really passes.

## Things to know before you touch them

- **Both ends must match.** Dozens of settings are paired, and a mismatch
  produces the worst failure this system has: the tunnel comes up, reports
  itself connected, and carries nothing. If you add a setting that both ends
  read, add it to the peer mirror and say so in the docs.
- **The data path is `internal/tunnel/l3`, `internal/server/transport`,
  `internal/client/transport` and `internal/utils/handlers`.** It has allocation
  budgets and benchmark gates. A change there that adds an allocation per packet
  fails the build, deliberately.
- **`config/` is the public API.** Everything else is `internal/` and free to
  move. A new `toml:` key must be read by something, and a test proves it.
- **Never break the wire without a plan.** `internal/e2e/wirecompat_test.go`
  runs the previous release against this one in both directions. Protocol
  version negotiation exists (`internal/tunnel/l3/version.go`); use it.

## Opening a change

- One thing per pull request. A refactor and a fix in one branch is a branch
  nobody can review.
- Say what a user would have seen before your change and what they see after.
- New behaviour comes with a test at a public seam. Not a test of the function
  you wrote — a test of the thing a user can now do.
- Structural changes go in one sub-package or one transport at a time, never as
  one sweep. `internal/manage` was split that way on purpose; see
  `internal/manage/core/doc.go` for the layers and why each seam was cut where
  it was.

## Reporting a security issue

Do not open an issue. See `SECURITY.md`.

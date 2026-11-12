#!/usr/bin/env bash
#
# Build one static Linux release binary, linked with libpcap.
#
#   scripts/build-linux.sh <arch> [output]
#
# <arch> is one of: amd64 arm64 386 s390x armv5 armv6 armv7 — the names the
# release assets use (backpack_linux_<arch>.tar.gz).
#
# Why not just `go build`. The PCK carrier captures with libpcap through cgo, so
# a release binary needs a C compiler for its target and a libpcap built for it
# — and has to stay a single static file that runs on a server with no libpcap
# and nothing else installed, exactly as the CGO-free build did. Zig's C
# compiler (zig cc, with its bundled musl) cross-compiles for every target from
# one x86-64 machine with no containers and no sudo; the one exception is ARMv5,
# which the musl in Zig cannot target (its atomics need ARMv6K), so that one is
# built with the distribution's arm-linux-gnueabi gcc and static glibc.
#
# `-tags netgo,osusergo` keeps the pure-Go resolver and user lookup, so cgo does
# not change how names are resolved: a static binary that called getaddrinfo
# would be a different program on every libc.
#
# Environment:
#   VERSION_LDFLAGS  extra -ldflags (the Makefile passes the ARM stamp)
#   PCAP_ROOT        where per-arch libpcap builds are kept
#                    (default ~/.cache/backpack-libpcap, outside the checkout so a
#                    path with a space in it cannot reach the C build)
#   ZIG              the zig binary (default: zig)
set -euo pipefail

arch="${1:?usage: build-linux.sh <amd64|arm64|386|s390x|armv5|armv6|armv7> [output]}"
out="${2:-dist/backpack-linux-$arch}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
ZIG="${ZIG:-zig}"
PCAP_ROOT="${PCAP_ROOT:-$HOME/.cache/backpack-libpcap}"

goarch="" goarm="" cc="" host="" cflags="" ar="" ranlib=""
zig_target() { # zig-triple host-triple [cflags]
  command -v "$ZIG" >/dev/null || { echo "build-linux.sh: zig is required (https://ziglang.org/download)" >&2; exit 2; }
  cc="$ZIG cc -target $1 $3" ar="$ZIG ar" ranlib="$ZIG ranlib" host="$2" cflags="$3"
}
case "$arch" in
  amd64) goarch=amd64; zig_target x86_64-linux-musl x86_64-linux-musl "" ;;
  arm64) goarch=arm64; zig_target aarch64-linux-musl aarch64-linux-musl "" ;;
  386)   goarch=386;   zig_target x86-linux-musl i386-linux-musl "" ;;
  s390x) goarch=s390x; zig_target s390x-linux-musl s390x-linux-musl "" ;;
  armv6) goarch=arm goarm=6; zig_target arm-linux-musleabihf arm-linux-musleabihf "-mcpu=arm1176jzf_s" ;;
  armv7) goarch=arm goarm=7; zig_target arm-linux-musleabihf arm-linux-musleabihf "-mcpu=generic+v7a" ;;
  armv5)
    goarch=arm goarm=5
    # Debian/Ubuntu: apt install gcc-arm-linux-gnueabi
    cc="${ARMV5_CC:-arm-linux-gnueabi-gcc}" host=arm-linux-gnueabi cflags="-march=armv5te" ar="" ranlib=""
    command -v "${cc%% *}" >/dev/null || { echo "build-linux.sh: armv5 needs arm-linux-gnueabi-gcc (apt install gcc-arm-linux-gnueabi), or set ARMV5_CC" >&2; exit 2; }
    ;;
  *) echo "build-linux.sh: unknown arch $arch" >&2; exit 2 ;;
esac

prefix="$PCAP_ROOT/$arch"
CC="$cc" AR="$ar" RANLIB="$ranlib" HOST="$host" CFLAGS="$cflags" "$root/scripts/build-libpcap.sh" "$prefix"

mkdir -p "$(dirname "$out")"
export CGO_ENABLED=1 GOOS=linux GOARCH="$goarch" CC="$cc"
export CGO_CFLAGS="-I$prefix/include $cflags" CGO_LDFLAGS="-L$prefix/lib -lpcap"
[[ -n "$goarm" ]] && export GOARM="$goarm"
go build -trimpath -tags netgo,osusergo \
  -ldflags "-s -w -buildid= -linkmode external -extldflags '-static' ${VERSION_LDFLAGS:-}" \
  -o "$out" .

# It must be one static file: no interpreter, no shared libraries.
if command -v readelf >/dev/null && readelf -d "$out" 2>/dev/null | grep -q NEEDED; then
  echo "build-linux.sh: $out is dynamically linked:" >&2
  readelf -d "$out" | grep NEEDED >&2
  exit 1
fi
echo "built $out ($arch)"

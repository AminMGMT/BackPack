#!/usr/bin/env bash
#
# Build a static libpcap into a prefix, for one target.
#
# Why this exists: the PCK carrier is paqet's engine, and paqet captures with
# gopacket/pcap, which is cgo and libpcap. Backpack was CGO-free until then, so
# every build that is meant to RUN the PCK carrier needs libpcap's headers and
# a static libpcap.a to link. Nothing here needs root: the default prefix is in
# the home directory, and the build uses whatever C compiler it is told to.
#
# Usage:
#   scripts/build-libpcap.sh [prefix]                       # host compiler (gcc)
#   CC="zig cc -target aarch64-linux-musl" HOST=aarch64-linux-musl \
#       scripts/build-libpcap.sh /opt/libpcap-arm64          # a cross build
#
# Environment:
#   LIBPCAP_VERSION  default 1.10.6
#   CC               the C compiler (default gcc)
#   HOST             configure's --host triple, for a cross build
#   AR, RANLIB       the matching archiver (zig: "zig ar", "zig ranlib")
#   CFLAGS           extra flags, for the target CPU
#
# libpcap's release tarball does not ship its generated parser and scanner, so
# it needs flex and bison. When they are missing (a bare Debian/Ubuntu runner
# has them; a minimal container may not) the script builds m4, bison and flex
# from source into $BUILD_TOOLS (default ~/.cache/backpack-build-tools), again
# without root.
#
# Paths with a space in them (a checkout in "My Projects/") break autotools'
# install step, so everything is built in a temporary directory and the finished
# prefix is copied into place.
#
# The result is <prefix>/include/pcap.h and <prefix>/lib/libpcap.a, built with
# --with-pcap=linux (no libnl, no dbus, no bluetooth, no USB: nothing to link
# beyond libc), which is what makes the final binary run on a host that has no
# libpcap and no libc to speak of.
set -euo pipefail

VERSION="${LIBPCAP_VERSION:-1.10.6}"
PREFIX="${1:-${LIBPCAP_PREFIX:-$HOME/.local/libpcap}}"
CC="${CC:-gcc}"
HOST_FLAG=()
[[ -n "${HOST:-}" ]] && HOST_FLAG=(--host="$HOST")

if [[ -f "$PREFIX/lib/libpcap.a" && -f "$PREFIX/include/pcap.h" && "${FORCE:-0}" != 1 ]]; then
  echo "libpcap $VERSION already in $PREFIX"
  exit 0
fi

# LD_LIBRARY_PATH on some machines points at libraries that break xz and tar.
unset LD_LIBRARY_PATH

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fetch() { # url sha-less download with retries
  local url="$1" out="$2"
  for i in 1 2 3; do
    if command -v curl >/dev/null; then curl -fsSL --max-time 300 -o "$out" "$url" && return 0
    else wget -q -O "$out" "$url" && return 0; fi
    sleep 2
  done
  echo "could not download $url" >&2; return 1
}

need_tools() { command -v flex >/dev/null && command -v bison >/dev/null; }
if ! need_tools; then
  TOOLS="${BUILD_TOOLS:-$HOME/.cache/backpack-build-tools}"
  if [[ ! -x "$TOOLS/bin/flex" || ! -x "$TOOLS/bin/bison" ]]; then
    echo "flex/bison not found: building them into $TOOLS"
    mkdir -p "$TOOLS"
    (
      cd "$WORK"
      export CFLAGS="-std=gnu11 -O2"   # gnulib in these releases predates C23
      unset CC HOST AR RANLIB          # tools run on the build machine
      export PATH="$TOOLS/bin:$PATH"
      build() { # name version url
        fetch "$3" "$1.tar"; mkdir "$1-src"; tar -xf "$1.tar" -C "$1-src" --strip-components=1
        (cd "$1-src" && ./configure --prefix="$TOOLS" >/dev/null && make -j"$(nproc)" >/dev/null && make install >/dev/null)
      }
      command -v m4 >/dev/null || build m4 1.4.19 https://ftp.gnu.org/gnu/m4/m4-1.4.19.tar.xz
      build bison 3.8.2 https://ftp.gnu.org/gnu/bison/bison-3.8.2.tar.xz
      build flex 2.6.4 https://github.com/westes/flex/releases/download/v2.6.4/flex-2.6.4.tar.gz
    )
  fi
  export PATH="$TOOLS/bin:$PATH"
fi

cd "$WORK"
fetch "https://www.tcpdump.org/release/libpcap-$VERSION.tar.gz" libpcap.tar.gz
tar -xzf libpcap.tar.gz
cd "libpcap-$VERSION"
export CC
[[ -n "${AR:-}" ]] && export AR
[[ -n "${RANLIB:-}" ]] && export RANLIB
STAGE="$WORK/stage"
./configure "${HOST_FLAG[@]}" --prefix="$STAGE" \
  --disable-shared --disable-dbus --disable-bluetooth --disable-usb --disable-netmap --disable-rdma \
  --without-libnl --with-pcap=linux >/dev/null
make -j"$(nproc)" >/dev/null
make install >/dev/null
mkdir -p "$PREFIX"
cp -a "$STAGE/." "$PREFIX/"
echo "libpcap $VERSION installed in $PREFIX"

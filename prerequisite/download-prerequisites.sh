#!/usr/bin/env bash
#
# download-prerequisites.sh
#
# Run this ONCE on any machine that HAS internet (ideally your kharej/abroad
# server, since go.dev is blocked from Iran). It fills ./prerequisite so the
# offline VPS needs NOTHING:
#
#   * Downloads the Go toolchain for linux amd64 + arm64  -> prerequisite/go*.tar.gz
#   * If Go is available, ALSO:
#       - vendors all dependencies                        -> ../vendor
#       - cross-compiles a static Linux binary            -> ../dist/backpack-linux-<arch>
#         (a prebuilt binary means the VPS needs no Go and no build at all)
#
# Then copy the whole BackPack folder to the VPS and run:  bash install.sh
#
set -euo pipefail

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
info() { echo -e "${GREEN}[*]${NC} $*"; }
warn() { echo -e "${YELLOW}[!]${NC} $*"; }
err()  { echo -e "${RED}[x]${NC} $*" >&2; }

GO_VERSION="1.23.4"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"   # prerequisite/
ROOT="$(cd "$HERE/.." && pwd)"                          # project root

export GOPROXY="https://goproxy.cn,https://goproxy.io,https://proxy.golang.org,direct"
export GOSUMDB=off
export GOTOOLCHAIN=local

# Download one Go tarball for a given arch, trying multiple mirrors.
fetch_go() {
  local arch="$1"
  local file="go${GO_VERSION}.linux-${arch}.tar.gz"
  local out="$HERE/${file}"
  if [[ -f "$out" ]]; then info "Already have ${file}"; return 0; fi
  local urls=(
    "https://mirrors.aliyun.com/golang/${file}"
    "https://golang.google.cn/dl/${file}"
    "https://go.dev/dl/${file}"
    "https://mirror.ghproxy.com/https://go.dev/dl/${file}"
  )
  for u in "${urls[@]}"; do
    info "Downloading ${file} from ${u}"
    if curl -fL --connect-timeout 15 "$u" -o "$out"; then
      info "Saved -> prerequisite/${file}"
      return 0
    fi
    warn "mirror failed, trying next..."
  done
  err "Could not download ${file} from any mirror."
  return 1
}

info "Fetching Go ${GO_VERSION} toolchains into ./prerequisite ..."
fetch_go amd64 || true
fetch_go arm64 || true

# Find a Go we can run to produce vendor/ + prebuilt binaries.
GO_BIN=""
if command -v go >/dev/null 2>&1; then
  GO_BIN="go"
elif [[ -x /usr/local/go/bin/go ]]; then
  GO_BIN="/usr/local/go/bin/go"
fi

# If no Go is installed but we're on Linux, bootstrap from the toolchain we just
# downloaded — so this works even on a fresh server with nothing installed.
if [[ -z "$GO_BIN" && "$(uname -s)" == "Linux" ]]; then
  host_arch=""
  case "$(uname -m)" in x86_64|amd64) host_arch="amd64";; aarch64|arm64) host_arch="arm64";; esac
  tb="$HERE/go${GO_VERSION}.linux-${host_arch}.tar.gz"
  if [[ -n "$host_arch" && -f "$tb" ]]; then
    info "No system Go — bootstrapping from the bundled toolchain to build offline artifacts."
    rm -rf "$HERE/.goroot" && mkdir -p "$HERE/.goroot"
    tar -C "$HERE/.goroot" -xzf "$tb"
    GO_BIN="$HERE/.goroot/go/bin/go"
  fi
fi

if [[ -n "$GO_BIN" ]]; then
  info "Go found ($($GO_BIN version)) — vendoring deps and cross-compiling."
  cd "$ROOT"
  "$GO_BIN" mod tidy
  "$GO_BIN" mod vendor
  mkdir -p dist
  for arch in amd64 arm64; do
    info "Building static linux/${arch} binary..."
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch "$GO_BIN" build -mod=vendor -trimpath -ldflags "-s -w" \
      -o "dist/backpack-linux-${arch}" .
    # also drop a copy next to the toolchain so install.sh Mode 1 finds it
    cp "dist/backpack-linux-${arch}" "$HERE/backpack-linux-${arch}"
  done
  info "Vendored deps -> ./vendor  and prebuilt binaries -> ./dist and ./prerequisite"
else
  warn "Go is not installed here, so no prebuilt binary/vendor was produced."
  warn "The VPS will use the bundled Go toolchain in ./prerequisite to build."
fi

echo
info "Prerequisites ready. Contents of ./prerequisite:"
ls -lh "$HERE" | sed 's/^/    /'
echo
echo -e "${GREEN}Next:${NC} copy the whole BackPack folder to the VPS and run: ${YELLOW}sudo bash install.sh${NC}"

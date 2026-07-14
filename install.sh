#!/usr/bin/env bash
#
# Backpack installer — one command on the VPS (as root):
#
#   bash <(curl -fsSL https://raw.githubusercontent.com/AminMGMT/BackPack/main/install.sh)
#
# or, if GitHub raw is blocked (Iran):
#
#   bash <(curl -fsSL https://gh-proxy.com/https://raw.githubusercontent.com/AminMGMT/BackPack/main/install.sh)
#
# It downloads the prebuilt release tar.gz for this architecture into
# /root/BackPack — trying GitHub DIRECTLY first, then falling back to public
# mirrors that work from Iran — and installs the binary. If run inside a source
# checkout and the download fails, it builds from source as a last resort.
#
# After install:  sudo backpack
#
set -euo pipefail

RED='\033[0;31m'; WHITE='\033[1;37m'; GRAY='\033[0;90m'; NC='\033[0m'
info() { echo -e "${WHITE}[*]${NC} $*"; }
warn() { echo -e "${GRAY}[!]${NC} $*"; }
err()  { echo -e "${RED}[x]${NC} $*" >&2; }

REPO="AminMGMT/BackPack"
BIN_PATH="/usr/local/bin/backpack"
INSTALL_DIR="/root/BackPack"
GO_VERSION="1.23.4"
GO_MIN_MINOR=23
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-/tmp}")" 2>/dev/null && pwd || echo /tmp)"

# GitHub download mirrors, tried AFTER a direct attempt (prefix form).
GH_MIRRORS=("https://gh-proxy.com/" "https://ghfast.top/" "https://ghproxy.net/")

if [[ $EUID -ne 0 ]]; then err "Please run as root (sudo)."; exit 1; fi

case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) err "Unsupported architecture: $(uname -m)"; exit 1 ;;
esac

ASSET="backpack_linux_${ARCH}.tar.gz"
mkdir -p /etc/backpack "$INSTALL_DIR/backups"

# fetch <url> <out> — direct first, then through each mirror.
fetch() {
  local url="$1" out="$2"
  info "Trying direct: ${url}"
  if curl -fSL --connect-timeout 15 "$url" -o "$out" 2>/dev/null; then return 0; fi
  for m in "${GH_MIRRORS[@]}"; do
    warn "direct failed — trying mirror ${m}"
    if curl -fSL --connect-timeout 15 "${m}${url}" -o "$out" 2>/dev/null; then return 0; fi
  done
  return 1
}

install_release() {
  # 1) A local release asset next to the script (e.g. ./release/ or ./dist/).
  for cand in "$SCRIPT_DIR/release/$ASSET" "$SCRIPT_DIR/dist/$ASSET" "$SCRIPT_DIR/$ASSET"; do
    if [[ -f "$cand" ]]; then
      info "Using local release asset: ${cand}"
      cp "$cand" "$INSTALL_DIR/$ASSET"
      return 0
    fi
  done
  # 2) The latest GitHub release (direct → mirrors).
  fetch "https://github.com/${REPO}/releases/latest/download/${ASSET}" "$INSTALL_DIR/$ASSET"
}

install_binary_from_tar() {
  tar -xzf "$INSTALL_DIR/$ASSET" -C "$INSTALL_DIR" backpack
  install -m 0755 "$INSTALL_DIR/backpack" "$BIN_PATH"
  rm -f "$INSTALL_DIR/backpack"
  echo "$INSTALL_DIR" > /etc/backpack/install_path
}

# ---------------------------------------------------------------------------
# Build-from-source fallback (only used when the release download fails and
# this script sits inside a source checkout).
# ---------------------------------------------------------------------------
download_go() {
  local file="go${GO_VERSION}.linux-${ARCH}.tar.gz" out="$1"
  for u in "https://go.dev/dl/${file}" \
           "https://golang.google.cn/dl/${file}" \
           "https://mirrors.aliyun.com/golang/${file}"; do
    info "Trying ${u}"
    curl -fsSL --connect-timeout 15 "$u" -o "$out" && return 0
    warn "source failed, trying next..."
  done
  return 1
}
go_new_enough() {
  local v; v="$("$1" version 2>/dev/null | grep -oE 'go1\.[0-9]+' | head -1)"; v="${v#go1.}"
  [[ -n "$v" ]] && (( v >= GO_MIN_MINOR ))
}
ensure_go() {
  command -v go >/dev/null 2>&1 && go_new_enough "$(command -v go)" && { info "Go: $(go version)"; return; }
  [[ -x /usr/local/go/bin/go ]] && go_new_enough /usr/local/go/bin/go && { export PATH="/usr/local/go/bin:$PATH"; info "Go: $(go version)"; return; }
  warn "Installing Go ${GO_VERSION}..."; download_go /tmp/go-bp.tgz || { err "Could not obtain Go."; exit 1; }
  rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go-bp.tgz; export PATH="/usr/local/go/bin:$PATH"; info "$(go version)"
}
build_from_source() {
  cd "$SCRIPT_DIR"
  ensure_go; export PATH="/usr/local/go/bin:$PATH"
  # Direct module fetching first, Iran-friendly mirrors as fallback.
  export GOPROXY="https://proxy.golang.org,https://mirror-go.runflare.com,https://goproxy.cn,direct"
  export GOSUMDB=off GOTOOLCHAIN=local
  info "Building from source (proxy order: direct first, then mirrors)."
  CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$BIN_PATH" .
  echo "$INSTALL_DIR" > /etc/backpack/install_path
}

if install_release; then
  install_binary_from_tar
  info "Installed release binary -> ${BIN_PATH}"
elif [[ -f "$SCRIPT_DIR/go.mod" && -f "$SCRIPT_DIR/main.go" ]]; then
  warn "Release download failed — building from source instead."
  build_from_source
  info "Built and installed -> ${BIN_PATH}"
else
  err "Could not download the release (direct or mirrors), and no source found."
  err "Retry later, or clone the repo and run install.sh inside it."
  exit 1
fi

chmod +x "$BIN_PATH"
echo
echo -e "${WHITE}Done!${NC} Open the menu with:  ${RED}sudo backpack${NC}"

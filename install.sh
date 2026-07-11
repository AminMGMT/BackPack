#!/usr/bin/env bash
#
# Backpack installer — clone the repo and run this as root:
#   git clone https://github.com/AminMGMT/BackPack.git
#   cd BackPack && sudo bash install.sh && sudo backpack
#
# It auto-detects:
#   1. A local prebuilt binary (./backpack, dist/, prerequisite/) -> install it
#   2. Source present (go.mod next to this script)                -> build it
#      (installs a suitable Go from the Aliyun mirror if needed; works from Iran)
#
# After install:  sudo backpack
#
set -euo pipefail

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
info() { echo -e "${GREEN}[*]${NC} $*"; }
warn() { echo -e "${YELLOW}[!]${NC} $*"; }
err()  { echo -e "${RED}[x]${NC} $*" >&2; }

BIN_PATH="/usr/local/bin/backpack"
GO_VERSION="1.23.4"
GO_MIN_MINOR=23
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-/tmp}")" 2>/dev/null && pwd || echo /tmp)"

# Iranian Go module proxy first (RunFlare), then China mirrors that also work
# from Iran. GitHub-hosted modules are fetched through these, so no direct
# GitHub access is needed to build.
export GOPROXY="https://mirror-go.runflare.com,https://goproxy.cn,https://goproxy.io,direct"
export GOSUMDB=off
export GOTOOLCHAIN=local

if [[ $EUID -ne 0 ]]; then err "Please run as root (sudo bash install.sh)."; exit 1; fi

case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) ARCH="" ;;
esac

mkdir -p /etc/backpack

# ---------------------------------------------------------------------------
# Build-from-source helpers.
# ---------------------------------------------------------------------------
download_go() {
  local file="go${GO_VERSION}.linux-${ARCH}.tar.gz" out="$1"
  for u in "https://mirrors.aliyun.com/golang/${file}" \
           "https://golang.google.cn/dl/${file}" \
           "https://go.dev/dl/${file}"; do
    info "Trying ${u}"
    curl -fsSL --connect-timeout 15 "$u" -o "$out" && return 0
    warn "mirror failed, trying next..."
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
  local bundled; bundled="$(ls "$SCRIPT_DIR"/prerequisite/go*.linux-"${ARCH}".tar.gz 2>/dev/null | head -1 || true)"
  if [[ -n "$bundled" ]]; then
    info "Using bundled Go: $(basename "$bundled")"; rm -rf /usr/local/go && tar -C /usr/local -xzf "$bundled"
    export PATH="/usr/local/go/bin:$PATH"; return
  fi
  [[ -z "$ARCH" ]] && { err "Unsupported architecture."; exit 1; }
  warn "Installing Go ${GO_VERSION}..."; download_go /tmp/go-bp.tgz || { err "Could not obtain Go."; exit 1; }
  rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go-bp.tgz; export PATH="/usr/local/go/bin:$PATH"; info "$(go version)"
}
build_from_source() {
  cd "$SCRIPT_DIR"
  if [[ -d "$SCRIPT_DIR/go" ]]; then
    warn "Moving stray ./go cache out of the project."; rm -rf "$HOME/backpack-gocache"; mv "$SCRIPT_DIR/go" "$HOME/backpack-gocache"
  fi
  ensure_go; export PATH="/usr/local/go/bin:$PATH"
  if [[ -d "$SCRIPT_DIR/vendor" ]]; then
    info "Building OFFLINE from vendor/."
    GOPROXY=off GOFLAGS=-mod=vendor CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$BIN_PATH" .
  else
    info "Building from source (proxy: ${GOPROXY})."
    go mod download 2>/dev/null || true
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$BIN_PATH" .
  fi
  echo "$SCRIPT_DIR" > /etc/backpack/install_path
}

# ---------------------------------------------------------------------------
# Pick a mode.
# ---------------------------------------------------------------------------
for cand in "$SCRIPT_DIR/backpack" "$SCRIPT_DIR/dist/backpack-linux-${ARCH}" "$SCRIPT_DIR/prerequisite/backpack-linux-${ARCH}"; do
  if [[ -f "$cand" ]]; then
    info "Installing local prebuilt binary: ${cand}"
    install -m 0755 "$cand" "$BIN_PATH"
    echo "$SCRIPT_DIR" > /etc/backpack/install_path
    MODE=done; break
  fi
done

if [[ "${MODE:-}" != "done" ]]; then
  if [[ -f "$SCRIPT_DIR/go.mod" && -f "$SCRIPT_DIR/main.go" ]]; then
    build_from_source
  else
    err "No source found. Clone the repo first:"
    err "   git clone https://github.com/AminMGMT/BackPack.git && cd BackPack && sudo bash install.sh"
    exit 1
  fi
fi

chmod +x "$BIN_PATH"
info "Installed backpack -> ${BIN_PATH}"
echo
echo -e "${GREEN}Done!${NC} Open the menu with:  ${YELLOW}sudo backpack${NC}"

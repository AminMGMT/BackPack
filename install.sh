#!/usr/bin/env bash
#
# fstunnel installer — one command on the VPS (as root)
# Created by @DevHttp - v1.1.1
#
#   bash <(curl -fsSL https://raw.githubusercontent.com/amirfste/fstunnel/main/install.sh)
#
# It downloads the prebuilt release tar.gz for this architecture into
# /root/fstunnel and installs the binary, verifying it against the checksum
# published with the release. If run inside a source checkout and the download
# fails, it builds from source as a last resort.
#
# A server that cannot reach GitHub at all installs offline instead: download
# the archive on a machine that can, copy it over, and follow the offline steps
# in the README. Third-party GitHub proxies are deliberately not used — the
# archive and its checksum would arrive through the same proxy, so verifying
# one against the other would prove nothing.
#
# When it finishes it opens the menu automatically (on an interactive terminal).
# Later, reopen it any time with:  sudo fstunnel
#
set -euo pipefail

RED='\033[0;31m'; WHITE='\033[1;37m'; GRAY='\033[0;90m'; NC='\033[0m'
info() { echo -e "${WHITE}[*]${NC} $*"; }
warn() { echo -e "${GRAY}[!]${NC} $*"; }
err()  { echo -e "${RED}[x]${NC} $*" >&2; }

REPO="amirfste/fstunnel"
BIN_PATH="/usr/local/bin/fstunnel"
INSTALL_DIR="/root/fstunnel"
GO_VERSION="1.24.5"
GO_MIN_MINOR=24
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-/tmp}")" 2>/dev/null && pwd || echo /tmp)"

if [[ $EUID -ne 0 ]]; then err "Please run as root (sudo)."; exit 1; fi

case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) err "Unsupported architecture: $(uname -m)"; exit 1 ;;
esac

ASSET="fstunnel_linux_${ARCH}.tar.gz"
mkdir -p /etc/fstunnel "$INSTALL_DIR/backups"

fetch() {
  local url="$1" out="$2"
  info "Downloading: ${url}"
  curl -fSL --connect-timeout 15 "$url" -o "$out" 2>/dev/null
}

verify_asset() {
  local file="$1" sums="$2"
  local expected actual

  expected="$(grep -E "[[:space:]]\\*?${ASSET}$" "$sums" 2>/dev/null | awk '{print $1}' | head -1)"
  if [[ -z "$expected" ]]; then
    warn "No checksum published for ${ASSET} — cannot verify this download."
    return 1
  fi

  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$file" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$file" | awk '{print $1}')"
  else
    warn "Neither sha256sum nor shasum is available — cannot verify this download."
    return 1
  fi

  if [[ "$expected" != "$actual" ]]; then
    err "CHECKSUM MISMATCH for ${ASSET}"
    err "  expected: ${expected}"
    err "  actual:   ${actual}"
    err "The file does not match what the release publishes. It may have been"
    err "altered in transit. Refusing to install it."
    return 2
  fi
  info "Checksum verified: ${actual:0:16}..."
  return 0
}

install_release() {
  for cand in "$SCRIPT_DIR/release/$ASSET" "$SCRIPT_DIR/dist/$ASSET" "$SCRIPT_DIR/$ASSET"; do
    if [[ -f "$cand" ]]; then
      info "Using local release asset: ${cand}"
      cp "$cand" "$INSTALL_DIR/$ASSET"
      local localsums="$(dirname "$cand")/SHA256SUMS"
      if [[ -f "$localsums" ]]; then
        local rc=0
        verify_asset "$INSTALL_DIR/$ASSET" "$localsums" || rc=$?
        if [[ $rc -eq 2 ]]; then
          rm -f "$INSTALL_DIR/$ASSET"
          exit 1
        fi
      else
        warn "No SHA256SUMS beside the local asset — installing it unverified."
      fi
      return 0
    fi
  done

  fetch "https://github.com/${REPO}/releases/latest/download/${ASSET}" "$INSTALL_DIR/$ASSET" || return 1

  if ! fetch "https://github.com/${REPO}/releases/latest/download/SHA256SUMS" "$INSTALL_DIR/SHA256SUMS"; then
    err "Could not fetch SHA256SUMS, so the download cannot be verified."
    err "Refusing to install it. Install offline instead — see the README."
    rm -f "$INSTALL_DIR/$ASSET"
    exit 1
  fi
  if ! verify_asset "$INSTALL_DIR/$ASSET" "$INSTALL_DIR/SHA256SUMS"; then
    err "Refusing to install an archive that could not be verified."
    err "Install offline instead — see the README."
    rm -f "$INSTALL_DIR/$ASSET"
    exit 1
  fi
  return 0
}

install_binary_from_tar() {
  tar -xzf "$INSTALL_DIR/$ASSET" -C "$INSTALL_DIR" fstunnel
  install -m 0755 "$INSTALL_DIR/fstunnel" "$BIN_PATH"
  rm -f "$INSTALL_DIR/fstunnel"
  echo "$INSTALL_DIR" > /etc/fstunnel/install_path
}

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
  export GOPROXY="https://proxy.golang.org,https://mirror-go.runflare.com,https://goproxy.cn,direct"
  export GOSUMDB=off GOTOOLCHAIN=local
  info "Building from source (proxy order: direct first, then mirrors)."
  CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$BIN_PATH" .
  echo "$INSTALL_DIR" > /etc/fstunnel/install_path
}

if install_release; then
  install_binary_from_tar
  info "Installed release binary -> ${BIN_PATH}"
elif [[ -f "$SCRIPT_DIR/go.mod" && -f "$SCRIPT_DIR/main.go" ]]; then
  warn "Release download failed — building from source instead."
  build_from_source
  info "Built and installed -> ${BIN_PATH}"
else
  err "Could not download the release, and no source checkout was found here."
  err "This server may not be able to reach GitHub. Install offline instead:"
  err "download the archive on a machine that can, copy it over, and follow the"
  err "offline steps in the README. Or clone the repo and run install.sh inside it."
  exit 1
fi

chmod +x "$BIN_PATH"
echo
echo -e "${WHITE}Done!${NC}"

if [ -t 0 ]; then
  echo -e "Starting the menu... ${GRAY}(next time, just run ${NC}${RED}sudo fstunnel${GRAY})${NC}"
  echo
  exec "$BIN_PATH"
else
  echo -e "Open the menu with:  ${RED}sudo fstunnel${NC}"
fi

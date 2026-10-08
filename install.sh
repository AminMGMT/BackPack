#!/usr/bin/env bash
#
# Backpack installer — one command on the VPS (as root):
#
#   bash <(curl -fsSL https://raw.githubusercontent.com/AminMGMT/BackPack/main/install.sh)
#
# It downloads the prebuilt release tar.gz for this architecture into
# /root/BackPack and installs the binary, verifying it against the checksum
# published with the release. If run inside a source checkout and the download
# fails, it builds from source as a last resort.
# BP_BUILD_FROM_SOURCE=1 deliberately installs this checkout's build instead
# of the latest release, for features not present in a published release yet.
#
# A server that cannot reach GitHub at all installs offline instead: download
# the archive on a machine that can, copy it over, and follow the offline steps
# in the README. Third-party GitHub proxies are deliberately not used — the
# archive and its checksum would arrive through the same proxy, so verifying
# one against the other would prove nothing.
#
# When it finishes it opens the menu automatically (on an interactive terminal).
# Later, reopen it any time with:  sudo backpack
#
set -euo pipefail

RED='\033[0;31m'; WHITE='\033[1;37m'; GRAY='\033[0;90m'; NC='\033[0m'
info() { echo -e "${WHITE}[*]${NC} $*"; }
warn() { echo -e "${GRAY}[!]${NC} $*"; }
err()  { echo -e "${RED}[x]${NC} $*" >&2; }

REPO="AminMGMT/BackPack"
BIN_PATH="/usr/local/bin/backpack"
INSTALL_DIR="/root/BackPack"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-/tmp}")" 2>/dev/null && pwd || echo /tmp)"

# Which Go the source build needs, and the oldest toolchain already on the
# machine that is usable for it.
#
# Read from go.mod whenever it is beside this script, because go.mod is what
# actually decides. The build runs with GOTOOLCHAIN=local on purpose — the
# networks this installer targets frequently cannot reach the toolchain
# downloader — and with that set, a local Go older than the `go` line does not
# fall back to anything, it refuses.
#
# Pinning both numbers by hand is what broke it: go.mod moved to 1.26.0 while
# these stayed at 1.24.5, so building from source could not succeed on any
# machine. That is the path taken only when the release download has already
# failed, which is to say on exactly the servers with the worst connectivity —
# the ones least able to do anything else. The values below are the fallback for
# a standalone `curl | bash`, where there is no go.mod to read and no source
# build to do either.
GO_VERSION="1.26.6"
GO_MIN_MINOR=26
if [[ -f "$SCRIPT_DIR/go.mod" ]]; then
  gomod_go="$(grep -m1 -E '^go[[:space:]]+[0-9]+\.[0-9]+' "$SCRIPT_DIR/go.mod" | awk '{print $2}' || true)"
  if [[ "$gomod_go" =~ ^[0-9]+\.([0-9]+)(\.[0-9]+)?$ ]]; then
    # A `go` line may read "1.26" or "1.26.0"; a download URL needs all three.
    if [[ -n "${BASH_REMATCH[2]}" ]]; then GO_VERSION="$gomod_go"; else GO_VERSION="${gomod_go}.0"; fi
    GO_MIN_MINOR="${BASH_REMATCH[1]}"
  fi
  unset gomod_go
fi

if [[ $EUID -ne 0 ]]; then err "Please run as root (sudo)."; exit 1; fi

case "${BP_BUILD_FROM_SOURCE:-0}" in
  0|1) ;;
  *) err 'BP_BUILD_FROM_SOURCE must be 0 or 1'; exit 2 ;;
esac
if [[ "${BP_BUILD_FROM_SOURCE:-0}" == 1 ]] && ! [[ -f "$SCRIPT_DIR/go.mod" && -f "$SCRIPT_DIR/main.go" ]]; then
  err 'BP_BUILD_FROM_SOURCE=1 requires a source checkout beside install.sh'
  exit 1
fi

# One thing may follow the script: a setup link to apply once Backpack is in.
#
#   bash <(curl -fsSL …/install.sh) link apply 'backpack://…'
#
# That is the line the Iran server prints for a kharej that does not run
# Backpack yet: install, then build the tunnel the link describes, with nothing
# asked. A leading "backpack" is accepted too, since the line is often written
# as the command it runs. Anything else is refused, as before.
#
# It used to accept `node --panel <host:port> --key <setup-key>`, which
# installed Backpack and then enrolled the machine with a panel. A panel reaches
# a managed server over its own SSH now, so there is nothing to enrol: the
# operator adds the server from the panel and never touches this machine again.
BP_ARGS=("$@")
if [[ ${#BP_ARGS[@]} -gt 0 && "${BP_ARGS[0]}" == "backpack" ]]; then
  BP_ARGS=("${BP_ARGS[@]:1}")
fi
if [[ ${#BP_ARGS[@]} -gt 0 ]]; then
  if [[ "${BP_ARGS[0]}" != "link" || "${BP_ARGS[1]:-}" != "apply" || ${#BP_ARGS[@]} -lt 3 ]]; then
    err "Unknown argument: ${BP_ARGS[0]}"
    err "This script takes no arguments, or a setup link to apply after installing:"
    err "  bash <(curl -fsSL …/install.sh) link apply 'backpack://…'"
    exit 2
  fi
fi

# Which release asset this machine can run.
#
# The three 32-bit ARM variants are not interchangeable — a v7 binary on a v5
# board is an illegal instruction, not a slow one — so uname alone is not
# enough: it says "armv7l" for the kernel's idea of the CPU, which is usually
# right, and /proc/cpuinfo's architecture line is the fallback when it is not.
# When neither is readable, v6 is the safe choice: it runs on v6 and v7 both.
arm_variant() {
  case "$(uname -m)" in
    armv7*) echo 7; return ;;
    armv6*) echo 6; return ;;
    armv5*|armv4*) echo 5; return ;;
  esac
  case "$(grep -m1 -i '^CPU architecture' /proc/cpuinfo 2>/dev/null)" in
    *7*) echo 7 ;;
    *6*) echo 6 ;;
    *5*) echo 5 ;;
    *)   echo 6 ;;
  esac
}

case "$(uname -m)" in
  x86_64|amd64)   ARCH="amd64" ;;
  aarch64|arm64)  ARCH="arm64" ;;
  i386|i486|i586|i686) ARCH="386" ;;
  s390x)          ARCH="s390x" ;;
  armv*|arm)      ARCH="armv$(arm_variant)" ;;
  *) err "Unsupported architecture: $(uname -m)"; exit 1 ;;
esac

# Optional protocol helpers: pinned, isolated and installed only on request.
helpers_validate() {
  case "${BP_HELPERS:-}" in
    ""|naive|xray|naive,xray) ;;
    *) err 'BP_HELPERS must be empty, naive, xray or naive,xray'; return 2 ;;
  esac
  [[ -z "${BP_HELPERS:-}" ]] && return 0
  [[ "$(uname -s)" == Linux ]] || { err 'Optional helpers require Linux'; return 1; }
  case "$ARCH" in amd64|arm64) ;; *) err "Optional helpers do not support $ARCH"; return 1 ;; esac
  if [[ "$BP_HELPERS" == *naive* ]]; then
    getconf GNU_LIBC_VERSION >/dev/null 2>&1 || { err 'Pinned Naive helpers require glibc; install suitable binaries manually on musl'; return 1; }
    command -v xz >/dev/null || { err 'xz is required for the Naive archive'; return 1; }
  fi
  if [[ "$BP_HELPERS" == *xray* ]]; then
    command -v unzip >/dev/null || { err 'unzip is required for the Xray archive'; return 1; }
  fi
  command -v sha256sum >/dev/null || command -v shasum >/dev/null || { err 'A SHA256 utility is required'; return 1; }
}

# Exact immutable archive pins, obtained from each official release API digest.
helper_spec() {
  helper_tool="$1"; helper_arch="$2"; helper_extra=""
  case "$helper_tool/$helper_arch" in
    naive/amd64) helper_sha=9d765620b90f7c60eb40c7c68b2f82537757cc52a8693dee7a00f8ba8b13dfd0; helper_arch=x64 ;;
    naive/arm64) helper_sha=dfa99dc36dafa1ee7f97f48bde14e9ee96bc4011e5cc10c7881fa7883859a036 ;;
    sing-box/amd64) helper_sha=a684484d7477d1437282ee411f4d131d0340aaad60a7868841ebd5d87dd8a0c6 ;;
    sing-box/arm64) helper_sha=b43a1fb1bda131c6653576741ce527eb2bdeab7c9308ca90ee8b972abb7e4a7f ;;
    xray/amd64) helper_sha=23cd9af937744d97776ee35ecad4972cf4b2109d1e0fe6be9930467608f7c8ae; helper_arch=64 ;;
    xray/arm64) helper_sha=4d30283ae614e3057f730f67cd088a42be6fdf91f8639d82cb69e48cde80413c; helper_arch=arm64-v8a ;;
    *) return 1 ;;
  esac
  case "$helper_tool" in
    naive)
      helper_version=v154.0.8037.49-4; helper_repo=klzgrad/naiveproxy
      helper_stem="naiveproxy-$helper_version-linux-$helper_arch"
      helper_asset="$helper_stem.tar.xz"; helper_member="$helper_stem/naive"; helper_kind=xz ;;
    sing-box)
      helper_version=v1.14.2; helper_repo=SagerNet/sing-box
      helper_stem="sing-box-${helper_version#v}-linux-$helper_arch"
      helper_asset="$helper_stem.tar.gz"; helper_member="$helper_stem/sing-box"; helper_extra="$helper_stem/libcronet.so"; helper_kind=gz ;;
    xray)
      helper_version=v26.3.27; helper_repo=XTLS/Xray-core
      helper_asset="Xray-linux-$helper_arch.zip"; helper_member=xray; helper_kind=zip ;;
  esac
}

# Subshell owns its staging cleanup and never overwrites an existing directory.
install_helper() (
  set -euo pipefail
  helper_spec "$1" "$ARCH" || exit 1
  root="/usr/local/lib/backpack/helpers/$helper_tool"
  final="$root/$helper_version-$ARCH"
  if [[ -e "$final" || -L "$final" ]]; then
    info "Keeping existing helper directory: $final (configure its binary explicitly)"
    exit 0
  fi
  mkdir -p "$root" || exit 1
  stage="$(mktemp -d "$root/.stage.XXXXXX")" || exit 1
  trap 'rm -rf -- "$stage"' EXIT
  archive="$stage/archive"
  fetch "https://github.com/$helper_repo/releases/download/$helper_version/$helper_asset" "$archive" || exit 1
  if command -v sha256sum >/dev/null; then
    actual="$(sha256sum "$archive" | awk '{print $1}')"
  else
    actual="$(shasum -a 256 "$archive" | awk '{print $1}')"
  fi
  [[ "$actual" == "$helper_sha" ]] || { err "CHECKSUM MISMATCH for $helper_asset"; exit 1; }
  mkdir "$stage/payload" || exit 1
  case "$helper_kind" in
    zip) unzip -p "$archive" "$helper_member" > "$stage/payload/$helper_tool" || exit 1 ;;
    xz) tar -xJf "$archive" -C "$stage/payload" --strip-components=1 "$helper_member" || exit 1 ;;
    gz) tar -xzf "$archive" -C "$stage/payload" --strip-components=1 "$helper_member" "$helper_extra" || exit 1 ;;
  esac
  [[ -f "$stage/payload/$helper_tool" && ! -L "$stage/payload/$helper_tool" && -s "$stage/payload/$helper_tool" ]] || exit 1
  chmod 0755 "$stage/payload" "$stage/payload/$helper_tool" || exit 1
  if [[ -f "$stage/payload/libcronet.so" ]]; then chmod 0644 "$stage/payload/libcronet.so" || exit 1; fi
  # GNU mv's no-clobber protects simultaneous installers too. Confirm that the
  # directory really moved: mv -n itself reports success when it skipped it.
  mv -Tn -- "$stage/payload" "$final" || exit 1
  [[ ! -d "$stage/payload" ]] || { err "Helper directory appeared concurrently: $final"; exit 1; }
  info "Verified helper installed: $final/$helper_tool"
)

install_helpers() {
  [[ -n "${BP_HELPERS:-}" ]] || return 0
  if [[ "$BP_HELPERS" == *naive* ]]; then
    install_helper naive || return 1
    install_helper sing-box || return 1
  fi
  [[ "$BP_HELPERS" != *xray* ]] || install_helper xray
}


# End optional protocol helpers.
helpers_validate || exit $?

ASSET="backpack_linux_${ARCH}.tar.gz"
mkdir -p /etc/backpack "$INSTALL_DIR/backups"

# fetch <url> <out> — straight to GitHub, so TLS terminates there.
fetch() {
  local url="$1" out="$2"
  info "Downloading: ${url}"
  curl -fSL --connect-timeout 15 "$url" -o "$out" 2>/dev/null
}

# verify_asset <file> <sumsfile>
# Confirms the downloaded archive matches the checksum published with the
# release. This matters most on restricted networks: the archive usually
# arrives through a third-party mirror, and without this there is nothing
# stopping that mirror from substituting a different binary.
verify_asset() {
  local file="$1" sums="$2"
  local expected actual

  expected="$(grep -E "[[:space:]]\\*?${ASSET}\$" "$sums" 2>/dev/null | awk '{print $1}' | head -1)"
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

# trusted_dir <dir> — true when an arbitrary local account cannot put a file in it.
#
# This gates the two places the installer trusts a file it found rather than one
# it fetched: the local release asset below, and the source-build fallback. Both
# install or compile something that then runs as root, and both take whatever
# happens to be sitting in the script's own directory.
#
# Which directory that is depends on how the script was started. The documented
# forms are all fine — `bash <(curl ...)` resolves to /dev/fd and a plain pipe
# to /, neither of which holds an asset; the documented offline path puts all
# three files in /root; a clone in a home directory is writable only by the
# person running sudo. What is not fine is the natural variation on the offline
# path: scp install.sh to /tmp and run it there.
#
# /tmp is world-writable, and its sticky bit does not help with this. Sticky
# stops one account deleting or replacing another's file, so the scp'd
# install.sh is safe — but it does nothing about CREATING a file that is not
# there yet. An account on the box pre-creates backpack_linux_<arch>.tar.gz and
# waits, and the branch below prefers a local asset over the download.
#
# The test is therefore the other-write bit, not ownership: a directory only the
# invoking operator can write is not a problem, and requiring root ownership
# would refuse an ordinary `git clone` in a home directory.
trusted_dir() {
  local dir="$1" perms
  perms="$(stat -c '%a' "$dir" 2>/dev/null)" || return 1
  perms="${perms: -3}"   # drop setuid/sticky if stat printed four digits
  (( (${perms:2:1} & 2) == 0 ))
}

install_release() {
  # 1) A local release asset next to the script (e.g. ./release/ or ./dist/).
  for cand in "$SCRIPT_DIR/release/$ASSET" "$SCRIPT_DIR/dist/$ASSET" "$SCRIPT_DIR/$ASSET"; do
    if [[ -f "$cand" ]]; then
      local canddir; canddir="$(dirname "$cand")"
      local localsums="$canddir/SHA256SUMS"
      # A world-writable directory is skipped whether or not SHA256SUMS is
      # there, and the SHA256SUMS is why: it would have been picked up from the
      # same directory as the archive, so anyone who could plant one could plant
      # the other and they would agree. That is the argument this script already
      # makes about third-party proxies, and it holds here for the same reason.
      if ! trusted_dir "$canddir"; then
        warn "Ignoring ${cand}: ${canddir} is world-writable, so neither it nor a checksum beside it can be trusted."
        warn "Work from a directory only you can write — /root is what docs/install.md uses."
        continue
      fi
      info "Using local release asset: ${cand}"
      cp "$cand" "$INSTALL_DIR/$ASSET"
      # An offline install can carry SHA256SUMS beside the archive; verify it
      # when it is there, and say plainly when it is not.
      if [[ -f "$localsums" ]]; then
        # `|| rc=$?` rather than a bare call: `set -e` is currently suppressed
        # here because install_release runs inside `if`, so a bare call happens
        # to work — but only for that reason. Moving the call site would make a
        # failed verification kill the script instead of reaching the warning.
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

  # 2) The latest GitHub release.
  fetch "https://github.com/${REPO}/releases/latest/download/${ASSET}" "$INSTALL_DIR/$ASSET" || return 1

  # 3) Verify against the checksums published with the same release. An archive
  #    that cannot be verified is not installed: this binary runs as root, and
  #    the offline install in the README is always available as a way out.
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
  tar -xzf "$INSTALL_DIR/$ASSET" -C "$INSTALL_DIR" backpack
  install -m 0755 "$INSTALL_DIR/backpack" "$BIN_PATH"
  rm -f "$INSTALL_DIR/backpack"
  echo "$INSTALL_DIR" > /etc/backpack/install_path
}

# ---------------------------------------------------------------------------
# Build-from-source fallback (only used when the release download fails and
# this script sits inside a source checkout).
# ---------------------------------------------------------------------------
# The checksums Go publishes for the toolchain this build needs.
#
# Verified before the archive is unpacked, and the reason is the same one the
# header gives for refusing third-party GitHub proxies: two of the three sources
# below are mirrors, and a mirror that hands over a modified toolchain compromises
# everything that toolchain then compiles — with no artefact left to compare
# afterwards. TLS does not help, because the mirror is the party being trusted.
#
# Keyed by Go's own architecture names, which are not the release-asset names
# used elsewhere in this script.
#
# GO_SHA_VERSION is the version these belong to, and it is checked against
# GO_VERSION before anything is downloaded. That check exists because
# GO_VERSION is read from go.mod and moves on its own: bumping the `go` line
# would otherwise leave this table describing a toolchain nobody is fetching,
# and the installer would verify a new archive against an old checksum — or,
# worse, be quietly changed to skip the check. It fails loudly and says exactly
# what to update instead. Values come from
# https://go.dev/dl/?mode=json&include=all.
GO_SHA_VERSION="1.26.6"
GO_SHA256_amd64="708effb774be8237570d0add163225abbdfaf4fca28b2611df167beba4feef89"
GO_SHA256_arm64="d0507e9e9d7fe012aae570108cbd76c15de879e17130ab8cb90d4d7445cb1f2e"
GO_SHA256_386="f09a71029fc5cd2940fbe36b0eb1fb2d8f3407cd6adb6b7b4de3eaf04007f8c4"
GO_SHA256_s390x="958757933d38172dd544085d253c8738cf09793d24c8bc0422e5e1e1fffa4fde"
GO_SHA256_armv6l="e1379a2fe77bd30fa29833074388247e7c65416e09279f746f20de2d5cf4dfea"

# go_arch maps this script's asset architecture onto the one Go names its
# toolchain with.
#
# They are not the same set, and the difference was a plain bug: ARCH is armv5,
# armv6 or armv7 for the three 32-bit ARM release assets, and Go publishes one
# 32-bit ARM toolchain called armv6l. The download URL therefore asked for
# go<version>.linux-armv7.tar.gz, which has never existed — so the
# build-from-source fallback could not work on any ARM machine, which is the
# hardware most likely to need it.
go_arch() {
  case "$1" in
    armv*) echo "armv6l" ;;   # one 32-bit ARM toolchain, usable on v6 and v7
    *)     echo "$1" ;;
  esac
}

# go_sha256 is the expected checksum for an architecture, or "" when this script
# carries none for it.
go_sha256() {
  local var="GO_SHA256_$1"
  echo "${!var-}"
}

download_go() {
  local garch file out want got
  garch="$(go_arch "$ARCH")"
  file="go${GO_VERSION}.linux-${garch}.tar.gz"
  out="$1"
  want="$(go_sha256 "$garch")"

  # No pinned checksum means no download. The alternative — fetching it anyway
  # and trusting whichever mirror answered — is the thing this exists to stop,
  # and a gap in the table above is a gap in this script rather than a reason to
  # lower the bar.
  if [[ "$GO_VERSION" != "$GO_SHA_VERSION" ]]; then
    err "This installer carries Go checksums for ${GO_SHA_VERSION}, but go.mod asks"
    err "for ${GO_VERSION}. The toolchain cannot be verified, so it will not be"
    err "downloaded."
    err "Fix: update GO_SHA_VERSION and the GO_SHA256_* values in install.sh from"
    err "     https://go.dev/dl/?mode=json&include=all"
    err "Or install Go ${GO_VERSION} or newer yourself and run this again."
    return 1
  fi
  if [[ -z "$want" ]]; then
    err "No pinned Go checksum for ${garch} in this installer, so the toolchain"
    err "cannot be verified and will not be downloaded."
    err "Install Go ${GO_VERSION} or newer yourself and run this again, or use"
    err "the offline install — see the README."
    return 1
  fi

  for u in "https://go.dev/dl/${file}" \
           "https://golang.google.cn/dl/${file}" \
           "https://mirrors.aliyun.com/golang/${file}"; do
    info "Trying ${u}"
    curl -fsSL --connect-timeout 15 "$u" -o "$out" || { warn "source failed, trying next..."; continue; }

    if command -v sha256sum >/dev/null 2>&1; then
      got="$(sha256sum "$out" | awk '{print $1}')"
    elif command -v shasum >/dev/null 2>&1; then
      got="$(shasum -a 256 "$out" | awk '{print $1}')"
    else
      err "Neither sha256sum nor shasum is available, so the Go toolchain cannot"
      err "be verified. Refusing to unpack it."
      rm -f "$out"
      return 1
    fi

    if [[ "$got" == "$want" ]]; then
      info "Go toolchain checksum verified: ${got:0:16}..."
      return 0
    fi

    err "CHECKSUM MISMATCH for ${file} from ${u}"
    err "  expected: ${want}"
    err "  actual:   ${got}"
    err "That source served something other than the published toolchain."
    rm -f "$out"
    warn "trying next source..."
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

# Select the engine build independently of optional helper installation.
if [[ "${BP_BUILD_FROM_SOURCE:-0}" == 1 ]]; then
  trusted_dir "$SCRIPT_DIR" || { err 'The requested source directory is not trusted'; exit 1; }
  build_from_source
  info "Built requested source checkout -> ${BIN_PATH}"
elif install_release; then
  install_binary_from_tar
  info "Installed release binary -> ${BIN_PATH}"
elif [[ -f "$SCRIPT_DIR/go.mod" && -f "$SCRIPT_DIR/main.go" ]] && trusted_dir "$SCRIPT_DIR"; then
  warn "Release download failed — building from source instead."
  build_from_source
  info "Built and installed -> ${BIN_PATH}"
else
  err "Could not download the release, and no usable source checkout was found here."
  err "(A checkout in a world-writable directory is not built from: it would"
  err " compile whatever is there into a binary that then runs as root.)"
  err "This server may not be able to reach GitHub. Install offline instead:"
  err "download the archive on a machine that can, copy it over, and follow the"
  err "offline steps in the README. Or clone the repo and run install.sh inside it."
  exit 1
fi
# End engine build selection.

install_helpers || { err "Optional helper installation failed; no helper was started"; exit 1; }

chmod +x "$BIN_PATH"
echo
echo -e "${WHITE}Done!${NC}"

# Open the menu straight away — people miss the "now run sudo backpack" step.
# Only when there is an interactive terminal to read from: a piped install
# (curl ... | bash) has no tty on stdin, so it just prints the instruction. The
# script already runs as root, so the binary is launched directly. `exec`
# replaces this shell so the menu owns the terminal cleanly.
# A setup link given on the command line is applied now, instead of opening the
# menu: the binary builds the tunnel, starts it, and says whether it connected.
if [[ ${#BP_ARGS[@]} -gt 0 ]]; then
  echo
  info "Setting up the tunnel from the setup link..."
  exec "$BIN_PATH" "${BP_ARGS[@]}"
fi

if [ -t 0 ]; then
  echo -e "Starting the menu... ${GRAY}(next time, just run ${NC}${RED}sudo backpack${GRAY})${NC}"
  echo
  exec "$BIN_PATH"
else
  echo -e "Open the menu with:  ${RED}sudo backpack${NC}"
fi

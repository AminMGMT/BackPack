#!/bin/sh
# Every reverse transport, across two real network namespaces.
#
# Three passes, because each asks a different question:
#
#   1. a real MTU with nothing else wrong — does it work off loopback at all;
#   2. a small MTU — does it survive a path that cannot carry full-sized
#      packets, which is the most common real failure and the one loopback
#      can never produce;
#   3. loss and latency — does it survive a path that drops things, which is
#      what the mux transports and KCP's ARQ exist for.
#
# See transporttest.sh for the setup this needs.
set -e
cd "$(dirname "$0")/../.."
go build -o /tmp/bp-transport .

TRANSPORTS="tcp tcpmux stealth ws wss wsmux wssmux kcp quic"
fail=0

run() {
  desc="$1"; shift
  echo
  echo "=== $desc ==="
  for t in $TRANSPORTS; do
    if ! timeout 180 unshare --map-auto --map-root-user --net --mount --fork -- \
        tools/transporttest/transporttest.sh "$t" "$@"; then
      fail=1
    fi
  done
}

run "a real MTU, clean path"        1400 0 0
run "a small MTU (1280)"            1280 0 0
run "loss and latency"              1400 2 20

exit $fail

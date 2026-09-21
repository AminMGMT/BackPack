#!/bin/sh
# Every layer-3 carrier, one after another. See carriertest.sh for the setup
# this needs and why.
set -e
cd "$(dirname "$0")/../.."
go build -o /tmp/bp-carrier .
fail=0
for c in udp quic pck sni xdi spoof; do
  if ! timeout 120 unshare --map-auto --map-root-user --net --mount --fork -- \
      tools/carriertest/carriertest.sh "$c"; then
    fail=1
  fi
done
exit $fail

#!/bin/sh
# carriertest — run one layer-3 carrier for real, end to end.
#
# The four obfuscated carriers (pck, sni, xdi, spoof) need CAP_NET_RAW and a
# packet socket. A `go test` process has neither, so internal/e2e cannot reach
# them: the transport matrix there covers the ten reverse transports and stops.
# These four are the ones most likely to be chosen when a path is hostile, which
# makes "untested because it is awkward" the wrong place to leave them.
#
# This builds the real thing instead. Two network namespaces joined by a veth
# pair, one BackPack l3 tunnel between them over the carrier being tested, then
# a ping to prove the tunnel is up and two megabytes through nc to prove it
# carries data byte for byte.
#
# Usage:
#
#   go build -o /tmp/bp-carrier .
#   unshare --map-auto --map-root-user --net --mount --fork -- \
#       tools/carriertest/carriertest.sh pck
#
# Carriers: udp quic pck sni xdi spoof
#
# On Ubuntu the unprivileged namespace is confined by AppArmor and the
# capabilities are stripped inside it, so raw sockets fail even as root there.
# Clear the gate first, once per boot:
#
#   sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
#
# Two settings below are load-bearing rather than tidy, and both were learned
# from a live run: rp_filter must be 0 on conf.all AND on the receiving
# interface (the effective value is the maximum of the two, so setting one is
# setting neither), and auto_mtu must be off or the tunnel rewrites its own
# config and the reload watcher flaps it.
set -e
BP=/tmp/bp-carrier
CARRIER="$1"
WORK=$(mktemp -d)

mount -t tmpfs none /run/netns 2>/dev/null || true
ip netns add iran
ip netns add kharej
ip link add v-i type veth peer name v-k
ip link set v-i netns iran
ip link set v-k netns kharej

ip netns exec iran   ip addr add 10.99.0.1/24 dev v-i
ip netns exec iran   ip link set v-i up
ip netns exec iran   ip link set lo up
ip netns exec kharej ip addr add 10.99.0.2/24 dev v-k
ip netns exec kharej ip link set v-k up
ip netns exec kharej ip link set lo up

# Load-bearing, not advisory: with rp_filter at 1 or 2 the kernel drops every
# forged-source packet before the tunnel sees it. Effective value is the max of
# conf.all and the receiving interface, so both are set.
for ns in iran kharej; do
  ip netns exec $ns sysctl -qw net.ipv4.conf.all.rp_filter=0
  ip netns exec $ns sysctl -qw net.ipv4.conf.default.rp_filter=0
  ip netns exec $ns sysctl -qw net.ipv4.conf.v-i.rp_filter=0 2>/dev/null || true
  ip netns exec $ns sysctl -qw net.ipv4.conf.v-k.rp_filter=0 2>/dev/null || true
done

SPOOF=""
if [ "$CARRIER" = "spoof" ]; then
  SPOOF='spoof_profile = "udp"
spoof_src_ip = "8.8.4.4"
spoof_peer_ip = "10.99.0.2"'
fi
SPOOF_K=""
if [ "$CARRIER" = "spoof" ]; then
  SPOOF_K='spoof_profile = "udp"
spoof_src_ip = "1.1.1.1"
spoof_peer_ip = "10.99.0.1"'
fi

# auto_mtu off, or the tunnel rewrites its own config and the reload watcher
# flaps it.
cat > "$WORK/iran.toml" <<EOF
[l3]
mode = "listen"
addr = "10.99.0.1:9000"
token = "a-netns-carrier-token-0123456789"
carrier = "$CARRIER"
local_ip = "10.10.0.1/30"
peer_ip = "10.10.0.2"
iface = "bp0"
mtu = 1300
auto_mtu = false
$SPOOF
EOF

cat > "$WORK/kharej.toml" <<EOF
[l3]
mode = "dial"
addr = "10.99.0.1:9000"
token = "a-netns-carrier-token-0123456789"
carrier = "$CARRIER"
local_ip = "10.10.0.2/30"
peer_ip = "10.10.0.1"
iface = "bp0"
mtu = 1300
auto_mtu = false
$SPOOF_K
EOF

ip netns exec iran   "$BP" -c "$WORK/iran.toml"   > "$WORK/iran.log"   2>&1 &
sleep 2
ip netns exec kharej "$BP" -c "$WORK/kharej.toml" > "$WORK/kharej.log" 2>&1 &
sleep 4

RC=1
if ip netns exec kharej ping -c 3 -W 2 -q 10.10.0.1 > "$WORK/ping.log" 2>&1; then
  # Ping proves the tunnel is up; a real payload proves it carries data.
  ip netns exec iran sh -c 'nc -l -p 7777 > /tmp/got.bin' &
  sleep 1
  head -c 2000000 /dev/urandom > "$WORK/send.bin"
  if ip netns exec kharej sh -c "nc -w 5 10.10.0.1 7777 < $WORK/send.bin"; then
    sleep 2
    if [ -f /tmp/got.bin ] && cmp -s "$WORK/send.bin" /tmp/got.bin; then
      echo "RESULT $CARRIER: OK  ping + 2MB byte-identical"
      RC=0
    else
      echo "RESULT $CARRIER: DATA MISMATCH ($(stat -c %s /tmp/got.bin 2>/dev/null || echo 0) of $(stat -c %s "$WORK/send.bin") bytes)"
    fi
  else
    echo "RESULT $CARRIER: ping worked, payload transfer failed"
  fi
else
  echo "RESULT $CARRIER: FAILED — no ping across the tunnel"
  tail -5 "$WORK/iran.log" | sed 's/^/  iran: /'
  tail -5 "$WORK/kharej.log" | sed 's/^/  kharej: /'
fi
rm -f /tmp/got.bin
exit $RC

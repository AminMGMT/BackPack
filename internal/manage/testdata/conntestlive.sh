#!/bin/bash
# The Connection Test across two network namespaces, one playing the Iran
# server and one the kharej, on a 70 ms path. Run under
#   unshare --map-auto --map-root-user --net --mount --fork
# by TestTheConnectionTestAcrossARealPath.
#
# Env: BIN (the engine), TESTBIN (the test binary, re-run as the Iran side),
# W (work dir), SOAK (seconds), FILTER (packets a flow may carry before the
# path drops the rest of it — what an Iran route was measured doing; unset for
# a clean path), PATHMTU (the veth MTU, to test the path-MTU measurement).
set -u
mount -t tmpfs none /run 2>/dev/null; mkdir -p /run/netns
ip netns add iran; ip netns add kharej
ip link add vi type veth peer name vk
ip link set vi netns iran; ip link set vk netns kharej
ip -n iran addr add 10.99.0.1/24 dev vi; ip -n kharej addr add 10.99.0.2/24 dev vk
for n in iran kharej; do ip -n $n link set lo up; done
ip -n iran link set vi up; ip -n kharej link set vk up
if [ -n "${PATHMTU:-}" ]; then
  ip -n iran link set vi mtu "$PATHMTU"; ip -n kharej link set vk mtu "$PATHMTU"
fi
ip netns exec iran tc qdisc add dev vi root netem delay 35ms
ip netns exec kharej tc qdisc add dev vk root netem delay 35ms
# A strict rp_filter drops a forged source before the spoof carrier sees it,
# and a loose one does too here, where no default route covers it; off, as
# l3live.sh has it. A real server with a default route needs only loose (2).
for n in iran kharej; do
  ip netns exec $n sysctl -qw net.ipv4.conf.all.rp_filter=0 net.ipv4.conf.default.rp_filter=0
done
ip netns exec iran sysctl -qw net.ipv4.conf.vi.rp_filter=0
ip netns exec kharej sysctl -qw net.ipv4.conf.vk.rp_filter=0

if [ -n "${FILTER:-}" ]; then
  # Every flow crossing the path is cut after FILTER packets, both ways.
  for dir in "INPUT -i vi" "OUTPUT -o vi"; do
    ip netns exec iran iptables -A $dir -m connbytes --connbytes "$FILTER": \
      --connbytes-dir both --connbytes-mode packets -j DROP
  done
fi

mkdir -p "$W"
rm -f "$W/link"
ip netns exec iran env CT_ROLE=iran CT_HOST=10.99.0.1 CT_W="$W" CT_SOAK="$SOAK" CT_PRESET="${PRESET:-}" CT_ONLY="${ONLY:-}" BIN="$BIN" \
  "$TESTBIN" -test.run '^TestConnTestIranSide$' -test.v -test.timeout 10m > "$W/iran.out" 2>&1 &
IP=$!
for i in $(seq 60); do [ -s "$W/link" ] && break; sleep 1; done
if [ ! -s "$W/link" ]; then echo "RESULT FAIL the Iran side printed no link"; cat "$W/iran.out"; exit 1; fi

ip netns exec kharej "$BIN" link apply "$(cat "$W/link")" > "$W/kharej.out" 2>&1
KC=$?
wait $IP
IC=$?
echo "--- iran"; cat "$W/iran.out"
echo "--- kharej (exit $KC)"; cat "$W/kharej.out"
[ $IC -eq 0 ] && echo "RESULT DONE" || echo "RESULT FAIL iran side exit $IC"

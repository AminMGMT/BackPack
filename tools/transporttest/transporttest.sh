#!/bin/sh
# transporttest — run one reverse transport across two real network namespaces.
#
# internal/e2e runs the whole ten-transport matrix, and it runs it on loopback.
# Loopback has a 65536-byte MTU, loses nothing, reorders nothing and costs
# nothing — which is the opposite of the path this product exists for. Every
# transport passes there, and the failures that matter in the field are
# precisely the ones loopback cannot produce:
#
#   - a path that will not carry a full-sized packet, which is what an MSS
#     clamp is for and what "connects and then stalls on the first real
#     transfer" looks like;
#   - loss and reordering, which is what the mux transports and KCP's ARQ are
#     for;
#   - a real 1500-byte-class MTU, which is where fragmentation shows up.
#
# So this runs the same claim — the tunnel comes up and carries a payload byte
# for byte — over a veth pair with a real MTU, and optionally with loss and
# latency applied by netem.
#
# Usage:
#
#   go build -o /tmp/bp-transport .
#   unshare --map-auto --map-root-user --net --mount --fork -- \
#       tools/transporttest/transporttest.sh tcp [mtu] [loss%] [delay_ms]
#
# Transports: tcp tcpmux stealth ws wss wsmux wssmux kcp quic udp
#
# See tools/carriertest/carriertest.sh for the AppArmor gate this shares.
set -e
BP=/tmp/bp-transport
TR="$1"
MTU="${2:-1400}"
LOSS="${3:-0}"
DELAY="${4:-0}"
WORK=$(mktemp -d)

# Everything this script starts, stopped — however it ends.
#
# It did not, and the cost was not theoretical: a full matrix run leaves two
# engine processes and an `nc` per transport behind, the namespace they were in
# disappears from under them, and they keep running as orphans for as long as
# the machine is up. A day of running this left 324 of them holding six and a
# half gigabytes, which is also enough to make every measurement taken
# afterwards quietly wrong.
#
# Worse, they inherit this script's stdout. A caller that waits for the pipe to
# close — which is what a shell, a CI step and a task runner all do — waits for
# the orphans rather than for the test, so a run that finished in forty seconds
# looks like it is still going hours later.
#
# The trap covers every exit including the interrupt, because the interrupt is
# the case that leaks most: somebody stops a matrix half way through.
cleanup() {
  status=$?
  trap - EXIT INT TERM HUP
  for pid in $PIDS; do
    kill -TERM "$pid" 2>/dev/null || true
  done
  sleep 0.3
  for pid in $PIDS; do
    kill -KILL "$pid" 2>/dev/null || true
  done
  rm -rf "$WORK"
  exit $status
}
PIDS=""
trap cleanup EXIT INT TERM HUP

mount -t tmpfs none /run/netns 2>/dev/null || true
ip netns add iran
ip netns add kharej
ip link add v-i type veth peer name v-k
ip link set v-i netns iran
ip link set v-k netns kharej

ip netns exec iran   ip addr add 10.99.0.1/24 dev v-i
ip netns exec iran   ip link set v-i mtu "$MTU" up
ip netns exec iran   ip link set lo up
ip netns exec kharej ip addr add 10.99.0.2/24 dev v-k
ip netns exec kharej ip link set v-k mtu "$MTU" up
ip netns exec kharej ip link set lo up

# Loss and latency, when asked for. Applied on both sides so it is a property
# of the path rather than of one direction — a tunnel that only survives loss
# in the direction nobody tested is a tunnel that has not been tested.
if [ "$LOSS" != "0" ] || [ "$DELAY" != "0" ]; then
  for ns in iran kharej; do
    dev=v-i; [ "$ns" = "kharej" ] && dev=v-k
    ip netns exec $ns tc qdisc add dev $dev root netem \
        loss "${LOSS}%" delay "${DELAY}ms" 2>/dev/null || true
  done
fi

TOKEN="a-netns-transport-token-01234567"

cat > "$WORK/iran.toml" <<EOF
[server]
bind_addr = "10.99.0.1:9000"
transport = "$TR"
token = "$TOKEN"
ports = ["7777=10.99.0.1:7778"]
channel_size = 512
heartbeat = 20
keepalive_period = 20
mux_con = 8
mux_version = 2
mux_framesize = 32768
mux_recievebuffer = 4194304
mux_streambuffer = 65536
log_level = "info"
skip_optz = true
EOF

cat > "$WORK/kharej.toml" <<EOF
[client]
remote_addr = "10.99.0.1:9000"
transport = "$TR"
token = "$TOKEN"
connection_pool = 4
retry_interval = 1
dial_timeout = 5
keepalive_period = 20
mux_session = 8
mux_version = 2
mux_framesize = 32768
mux_recievebuffer = 4194304
mux_streambuffer = 65536
log_level = "info"
skip_optz = true
EOF

# The backend the tunnel forwards to lives on the Iran side here, because the
# reverse tunnel exposes its ports there. A byte-for-byte echo is what proves
# the payload survived.
# Inside this run's own directory, not a fixed path in /tmp.
#
# A shared /tmp/got.bin is a file two runs can hold at once: the netns isolates
# the network and not the filesystem, and a previous run's nc lives a moment
# longer than the script that started it. The symptom was a transport reporting
# the right number of bytes and the wrong content — the previous run's payload.
GOT="$WORK/got.bin"
ip netns exec iran sh -c "nc -l -p 7778 > $GOT" &
PIDS="$PIDS $!"
sleep 1

ip netns exec iran   "$BP" -c "$WORK/iran.toml"   > "$WORK/iran.log"   2>&1 &
PIDS="$PIDS $!"
sleep 2
ip netns exec kharej "$BP" -c "$WORK/kharej.toml" > "$WORK/kharej.log" 2>&1 &
PIDS="$PIDS $!"
sleep 5

LABEL="$TR mtu=$MTU"
[ "$LOSS" != "0" ] && LABEL="$LABEL loss=${LOSS}%"
[ "$DELAY" != "0" ] && LABEL="$LABEL delay=${DELAY}ms"

RC=1
# Big enough to span many packets: a transport that works for one small write
# and falls over on a stream is exactly what a full MTU path catches.
head -c 2000000 /dev/urandom > "$WORK/send.bin"
if ip netns exec kharej sh -c "nc -w 20 10.99.0.1 7777 < $WORK/send.bin"; then
  sleep 3
  GOTN=$(stat -c %s "$GOT" 2>/dev/null || echo 0)
  WANT=$(stat -c %s "$WORK/send.bin")
  if [ -f "$GOT" ] && cmp -s "$WORK/send.bin" "$GOT"; then
    echo "RESULT $LABEL: OK  2MB byte-identical"
    RC=0
  else
    echo "RESULT $LABEL: DATA MISMATCH ($GOTN of $WANT bytes)"
    tail -5 "$WORK/kharej.log" | sed 's/^/  kharej: /'
  fi
else
  echo "RESULT $LABEL: the tunnel never carried anything"
  tail -6 "$WORK/iran.log"   | sed 's/^/  iran: /'
  tail -6 "$WORK/kharej.log" | sed 's/^/  kharej: /'
fi
exit $RC

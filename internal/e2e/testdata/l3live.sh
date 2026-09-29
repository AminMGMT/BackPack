#!/bin/bash
# l3live.sh <carrier> <paths> — one [l3] tunnel between two network namespaces,
# run for real: a TUN device on each side, the engine binary in $BIN, and a
# veth between them standing in for the path.
#
# TestL3CarriersOverARealTUN runs it inside
#   unshare --map-auto --map-root-user --net --mount --fork
# so nothing here touches the host's network. The last line it prints is
# "RESULT OK <detail>" or "RESULT FAIL <detail>".
#
# Environment: BIN (required), W (a working directory, required),
# IRAN_CONF / KHAREJ_CONF (configs to run instead of the generated ones),
# BIN_LISTEN (a different build for the listening end, for a cross-version run),
# NETEM (tc netem arguments applied to both ends of the veth, optional),
# RESTART=1 (SIGKILL the listening end once the tunnel works, start it again,
# and require the tunnel to carry traffic again),
# BLOCKFLOW=1 (pck/sni: once the tunnel works, drop the dialling end's current
# flow on the path — its source port, as a middlebox that stopped passing it
# would — and require the tunnel to come back on its own, from other ports).
set -u
CARRIER="$1"; PATHS="$2"
mkdir -p "$W"

mount -t tmpfs none /run 2>/dev/null; mkdir -p /run/netns
ip netns add iran; ip netns add kharej
ip link add name vi type veth peer name vk
ip link set vi netns iran; ip link set vk netns kharej
ip -n iran addr add 10.99.0.1/24 dev vi; ip -n kharej addr add 10.99.0.2/24 dev vk
for n in iran kharej; do
  ip -n $n link set lo up
  # A forged or unexpected source is dropped by a strict rp_filter before the
  # carrier sees it; production relaxes it the same way (see rpfilter.go).
  ip netns exec $n sysctl -qw net.ipv4.conf.all.rp_filter=0 net.ipv4.conf.default.rp_filter=0
done
ip -n iran link set vi up; ip -n kharej link set vk up
ip netns exec iran sysctl -qw net.ipv4.conf.vi.rp_filter=0
ip netns exec kharej sysctl -qw net.ipv4.conf.vk.rp_filter=0
if [ -n "${NETEM:-}" ]; then
  ip netns exec iran tc qdisc add dev vi root netem $NETEM
  ip netns exec kharej tc qdisc add dev vk root netem $NETEM
fi

TOKEN="l3-live-token-0123456789abcdef"
extra_k=""; extra_i=""
case "$CARRIER" in
  pck|sni) extra_k='pck_interface = "vk"'; extra_i='pck_interface = "vi"' ;;
  spoof) extra_k='spoof_peer_ip = "10.99.0.1"
spoof_src_pool = ["198.51.100.77"]'
         extra_i='spoof_src_pool = ["198.51.100.66"]' ;;
esac
# auto_mtu=false: with it on, the tunnel rewrites its own file after probing,
# and the reload watcher would restart it in the middle of the measurement.
conf() { # mode iface local peer extra
  cat <<EOF
[l3]
mode = "$1"
addr = "10.99.0.2:9000"
token = "$TOKEN"
carrier = "$CARRIER"
iface = "$2"
local_ip = "$3"
peer_ip = "$4"
auto_mtu = false
mtu = 1300
paths = $PATHS
$5
EOF
}
conf listen bpk0 10.200.0.2/30 10.200.0.1 "$extra_k" > "$W/kharej.toml"
conf dial   bpi0 10.200.0.1/30 10.200.0.2 "$extra_i" > "$W/iran.toml"
# Or the two configs a real setup made — the Iran wizard's, and the kharej's
# built from its setup link — in place of the ones above. They must use this
# script's addresses: the kharej listens on 10.99.0.2:9000, and the tunnel is
# 10.200.0.1 (Iran) to 10.200.0.2 (kharej).
if [ -n "${IRAN_CONF:-}" ]; then cp "$IRAN_CONF" "$W/iran.toml"; fi
if [ -n "${KHAREJ_CONF:-}" ]; then cp "$KHAREJ_CONF" "$W/kharej.toml"; fi

BINK="${BIN_LISTEN:-$BIN}"
ip netns exec kharej "$BINK" -c "$W/kharej.toml" > "$W/kharej.log" 2>&1 &
KP=$!
sleep 0.5
ip netns exec iran "$BIN" -c "$W/iran.toml" > "$W/iran.log" 2>&1 &
IP=$!

pings() { # tries — until one ping crosses the tunnel
  for _ in $(seq 1 "$1"); do
    ip netns exec iran ping -c1 -W1 10.200.0.2 >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  return 1
}
copy() { # port — 5 MB across the tunnel, compared byte for byte
  ip netns exec kharej sh -c "nc -l -p $1 > '$W/got' 2>/dev/null" &
  local np=$!
  sleep 0.3
  local s e
  s=$(date +%s.%N)
  ip netns exec iran sh -c "nc -N -w 5 10.200.0.2 $1 < '$W/payload'" 2>/dev/null
  e=$(date +%s.%N)
  wait $np 2>/dev/null
  cmp -s "$W/payload" "$W/got" || return 1
  echo "$e - $s" | bc
}

result=FAIL; detail="the tunnel never carried a ping"
head -c 5242880 /dev/urandom > "$W/payload"
if pings 80; then
  loss=$(ip netns exec iran ping -q -c 20 -i 0.05 10.200.0.2 2>/dev/null | grep -oE '[0-9.]+% packet loss')
  if secs=$(copy 5001); then
    result=OK; detail="ping $loss; 5 MB byte-identical in ${secs:0:5}s"
  else
    detail="ping $loss; the 5 MB copy arrived different ($(stat -c %s "$W/got" 2>/dev/null) bytes)"
  fi
fi

if [ "$result" = OK ] && [ -n "${RESTART:-}" ]; then
  kill -KILL $KP; wait $KP 2>/dev/null
  t0=$(date +%s.%N)
  ip netns exec kharej "$BINK" -c "$W/kharej.toml" >> "$W/kharej.log" 2>&1 &
  KP=$!
  if pings 160; then
    back=$(echo "$(date +%s.%N) - $t0" | bc)
    if copy 5002 >/dev/null; then
      detail="$detail; listener killed and restarted, traffic back in ${back:0:5}s and 5 MB identical again"
    else
      result=FAIL; detail="$detail; after the listener restarted the copy arrived different"
    fi
  else
    result=FAIL; detail="$detail; after the listener restarted the tunnel never came back"
  fi
fi

if [ "$result" = OK ] && [ -n "${BLOCKFLOW:-}" ]; then
  flowport() { # the dialling end's current source port, from its own startup line
    local p
    p=$(grep -oE 'src=10\.99\.0\.1:[0-9]+' "$W/iran.log" | tail -1 | cut -d: -f2)
    if [ -z "$p" ]; then
      # v1.8.4 and earlier did not log it; they derived it from the token, and
      # a single-path tunnel took the first port of that range.
      local h
      h=$(printf 'backpack-pck-v1:%s' "$TOKEN" | sha256sum | cut -c1-4)
      p=$(( 32768 + 16#$h % (28000 - 128) ))
    fi
    echo "$p"
  }
  sport=$(flowport)
  if [ -z "$sport" ]; then
    result=FAIL; detail="$detail; could not read the dialling end's source port off the wire"
  else
    # On the dialling end's egress, where a middlebox would sit. Not iptables:
    # the carrier reads and writes through a packet socket, which sees a frame
    # before netfilter does, so an iptables drop blocks nothing. The qdisc is
    # not bypassed.
    ip netns exec iran tc qdisc add dev vi root handle 1: prio
    ip netns exec iran tc filter add dev vi parent 1: protocol ip prio 1 u32 \
      match ip protocol 6 0xff match ip sport "$sport" 0xffff action drop
    t0=$(date +%s.%N)
    if pings 800; then
      back=$(echo "$(date +%s.%N) - $t0" | bc)
      now=$(flowport)
      if copy 5003 >/dev/null; then
        detail="$detail; flow from port $sport blocked on the path, back in ${back:0:5}s from port $now, 5 MB identical"
      else
        result=FAIL; detail="$detail; after the blocked flow the copy arrived different"
      fi
    else
      result=FAIL; detail="$detail; flow from port $sport blocked on the path and the tunnel never came back"
    fi
  fi
fi

kill -INT $IP $KP 2>/dev/null; sleep 1; kill -KILL $IP $KP 2>/dev/null; wait 2>/dev/null
if [ "$result" != OK ]; then
  echo "--- dialling end"; tail -8 "$W/iran.log"
  echo "--- listening end"; tail -8 "$W/kharej.log"
fi
echo "RESULT $result $detail"

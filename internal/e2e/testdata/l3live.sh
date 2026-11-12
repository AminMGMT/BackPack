#!/bin/bash
# l3live.sh <carrier> — one [l3] tunnel between two network namespaces,
# run for real: a TUN device on each side, the engine binary in $BIN, and a
# veth between them standing in for the path. For gre and ip6gre it is a kernel
# GRE link on each side instead, over the veth's IPv4 or IPv6 addresses.
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
# would — and require the tunnel to come back on its own, from other ports),
# PRESET (pck: the preset label written to both files, default turbo),
# PCKCONNS (pck: pck_conns for the dialling end; BLOCKFLOW uses 1, so that the
# one flow it blocks is the whole tunnel),
# EXPECT_OLD_PEER=1 (pck, with the previous release at one end: the tunnel is
# NOT expected to come up. The new end has to say, in its log, that the other
# server runs the old PCK).
#
# pck is paqet's engine and has no TUN: there is no ping and no 10.200.0.x
# address. The dialling end listens on forwarded ports (TCP 5000-5003, UDP 5010)
# and the listening end dials 127.0.0.1 for them, so the checks go through
# those ports instead. The files still carry every TUN key an old pck file had
# (iface, local_ip, peer_ip, mtu, ...), on purpose: they are what a migrated
# tunnel looks like, and the engine has to ignore them.
set -u
CARRIER="$1"
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
# IPv6 on the path too, for ip6gre. nodad, so the addresses are usable at once
# rather than after duplicate detection.
ip -n iran addr add fd99::1/64 dev vi nodad; ip -n kharej addr add fd99::2/64 dev vk nodad
ip netns exec iran sysctl -qw net.ipv4.conf.vi.rp_filter=0
ip netns exec kharej sysctl -qw net.ipv4.conf.vk.rp_filter=0
if [ -n "${NETEM:-}" ]; then
  ip netns exec iran tc qdisc add dev vi root netem $NETEM
  ip netns exec kharej tc qdisc add dev vk root netem $NETEM
fi

TOKEN="l3-live-token-0123456789abcdef"
PRESET="${PRESET:-turbo}"
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
$5
EOF
  if [ "$CARRIER" = pck ]; then
    echo "preset = \"$PRESET\""
    if [ "$1" = dial ]; then
      echo 'ports = ["5000", "5001", "5002", "5003", "5004", "5010"]'
      echo 'accept_udp = true'
      [ -n "${PCKCONNS:-}" ] && echo "pck_conns = $PCKCONNS"
    fi
  fi
}
# A kernel GRE link names the other server on both sides and has no port and
# no token. The MTU is left to the engine — the veth's 1500 less GRE's own —
# and so is measuring it: nothing is rewritten after a GRE probe.
greconf() { # mode iface local peer remote
  cat <<EOF
[l3]
mode = "$1"
addr = "$5"
carrier = "$CARRIER"
gre_key = 77
iface = "$2"
local_ip = "$3"
peer_ip = "$4"
EOF
}
case "$CARRIER" in
  gre)
    greconf listen bpk0 10.200.0.2/30 10.200.0.1 10.99.0.1 > "$W/kharej.toml"
    greconf dial   bpi0 10.200.0.1/30 10.200.0.2 10.99.0.2 > "$W/iran.toml" ;;
  ip6gre)
    greconf listen bpk0 10.200.0.2/30 10.200.0.1 fd99::1 > "$W/kharej.toml"
    greconf dial   bpi0 10.200.0.1/30 10.200.0.2 fd99::2 > "$W/iran.toml" ;;
  *)
    conf listen bpk0 10.200.0.2/30 10.200.0.1 "$extra_k" > "$W/kharej.toml"
    conf dial   bpi0 10.200.0.1/30 10.200.0.2 "$extra_i" > "$W/iran.toml" ;;
esac
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

# pck: a TCP echo and a UDP echo on the listening end, reached through the
# dialling end's forwarded ports.
restart_echo() {
  ip netns exec kharej python3 -I - <<'PY' &
import socket, threading
def tcp():
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("127.0.0.1", 5000)); s.listen(64)
    def one(c):
        try:
            while True:
                d = c.recv(65536)
                if not d: break
                c.sendall(d)
        finally: c.close()
    while True:
        c, _ = s.accept(); threading.Thread(target=one, args=(c,), daemon=True).start()
def udp():
    u = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); u.bind(("127.0.0.1", 5010))
    while True:
        d, a = u.recvfrom(65535); u.sendto(d, a)
def halfclose():
    # Replies only once the client has finished sending: what a protocol that
    # shuts down its write side and waits for the answer does.
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("127.0.0.1", 5004)); s.listen(64)
    def one(c):
        try:
            n = 0
            while True:
                d = c.recv(65536)
                if not d: break
                n += len(d)
            c.sendall(("got %d bytes" % n).encode())
        finally: c.close()
    while True:
        c, _ = s.accept(); threading.Thread(target=one, args=(c,), daemon=True).start()
threading.Thread(target=udp, daemon=True).start()
threading.Thread(target=halfclose, daemon=True).start()
tcp()
PY
  ECHOP=$!
}
if [ "$CARRIER" = pck ]; then restart_echo; fi
pings() { # tries — until one ping (pck: one echo through a forwarded port) crosses the tunnel
  for _ in $(seq 1 "$1"); do
    if [ "$CARRIER" = pck ]; then
      [ "$(ip netns exec iran python3 -I -c '
import socket
s = socket.create_connection(("127.0.0.1", 5000), 2); s.settimeout(2)
s.sendall(b"ping"); print(s.recv(4).decode())' 2>/dev/null)" = ping ] && return 0
    else
      ip netns exec iran ping -c1 -W1 10.200.0.2 >/dev/null 2>&1 && return 0
    fi
    sleep 0.25
  done
  return 1
}
udpecho() { # datagrams of several sizes through the forwarded UDP port, each back unchanged
  ip netns exec iran python3 -I -c '
import socket, os, sys
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.settimeout(3)
for n in (1, 200, 1200, 1472, 8000, 30000):
    d = os.urandom(n)
    for attempt in range(5):
        s.sendto(d, ("127.0.0.1", 5010))
        try:
            r, _ = s.recvfrom(65535)
        except socket.timeout:
            continue
        if r == d: break
        sys.exit("datagram of %d came back different" % n)
    else:
        sys.exit("datagram of %d never came back" % n)
' 2>&1
}
halfclose() { # a client that sends, shuts down its write side, and then reads the reply
  ip netns exec iran python3 -I -c '
import socket, os, sys
for size in (5, 3000000):
    s = socket.create_connection(("127.0.0.1", 5004), 5); s.settimeout(20)
    s.sendall(os.urandom(size)); s.shutdown(socket.SHUT_WR)
    r = b""
    while True:
        d = s.recv(4096)
        if not d: break
        r += d
    want = ("got %d bytes" % size).encode()
    if r != want: sys.exit("after sending %d bytes and shutting down, read %r, want %r" % (size, r, want))
    s.close()
' 2>&1
}
copy() { # port — 5 MB across the tunnel, compared byte for byte
  ip netns exec kharej sh -c "nc -l -p $1 > '$W/got' 2>/dev/null" &
  local np=$!
  sleep 0.3
  local s e
  s=$(date +%s.%N)
  if [ "$CARRIER" = pck ]; then
    ip netns exec iran sh -c "nc -N -w 5 127.0.0.1 $1 < '$W/payload'" 2>/dev/null
  else
    ip netns exec iran sh -c "nc -N -w 5 10.200.0.2 $1 < '$W/payload'" 2>/dev/null
  fi
  e=$(date +%s.%N)
  wait $np 2>/dev/null
  cmp -s "$W/payload" "$W/got" || return 1
  echo "$e - $s" | bc
}

result=FAIL; detail="the tunnel never carried a ping"
head -c 5242880 /dev/urandom > "$W/payload"
if [ -n "${EXPECT_OLD_PEER:-}" ]; then
  # One end is the previous release, whose PCK cannot talk to this one. What is
  # being checked is that nothing connects, and that the log says why.
  sleep 14
  if pings 8; then
    detail="the tunnel came up between the old PCK and the new one, which cannot be"
  elif grep -qE "previous PCK|PCK wire changed" "$W/iran.log" "$W/kharej.log"; then
    result=OK; detail="no tunnel, and the log names the old PCK: $(grep -hE 'previous PCK|PCK wire changed' "$W/iran.log" "$W/kharej.log" | head -1 | cut -c1-160)"
  else
    detail="no tunnel, but nothing in either log says the other end runs the old PCK"
  fi
elif pings 80; then
  if [ "$CARRIER" = pck ]; then loss="(forwarded port)"; else
    loss=$(ip netns exec iran ping -q -c 20 -i 0.05 10.200.0.2 2>/dev/null | grep -oE '[0-9.]+% packet loss'); fi
  if secs=$(copy 5001); then
    result=OK; detail="ping $loss; 5 MB byte-identical in ${secs:0:5}s"
    if [ "$CARRIER" = pck ]; then
      if udp=$(udpecho); then detail="$detail; UDP datagrams 1..30000 B echoed intact"
      else result=FAIL; detail="$detail; UDP: $udp"; fi
      # A half-closed connection still gets its reply (the old TUN path kept
      # this; the engine frames the stream so that it survives).
      if [ "$result" = OK ]; then
        if hc=$(halfclose); then detail="$detail; half-close: the reply arrives after the client's shutdown"
        else result=FAIL; detail="$detail; half-close: $hc"; fi
      fi
    fi
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
      # the paqet engine: "session 1 to 10.99.0.2:9000 is up (source port N, ..."
      p=$(grep -oE 'is up \(source port [0-9]+' "$W/iran.log" | tail -1 | grep -oE '[0-9]+$')
    fi
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

# A GRE tunnel says when the other end answers, and takes its link down on a
# clean stop; anything left behind would block the next start's name.
if [ "$result" = OK ] && { [ "$CARRIER" = gre ] || [ "$CARRIER" = ip6gre ]; }; then
  for _ in $(seq 1 40); do grep -q "other end answers" "$W/iran.log" && break; sleep 0.25; done
  if ! grep -q "other end answers" "$W/iran.log"; then
    result=FAIL; detail="$detail; the engine never reported the other end answering"
  fi
  mtu=$(ip netns exec iran cat /sys/class/net/bpi0/mtu 2>/dev/null)
  detail="$detail; $CARRIER link mtu $mtu"
  # The link keeps the kernel's noqueue: a qdisc on a link that never queues
  # is only a lock every packet takes (see openGRELink).
  qdisc=$(ip netns exec iran tc qdisc show dev bpi0 2>/dev/null | awk '{print $2; exit}')
  if [ "$qdisc" != noqueue ]; then
    result=FAIL; detail="$detail; the GRE link has qdisc ${qdisc:-?}, not the kernel's noqueue"
  fi
  kill -INT $IP; wait $IP 2>/dev/null
  if ip -n iran link show bpi0 >/dev/null 2>&1; then
    result=FAIL; detail="$detail; the GRE link outlived a clean stop"
  fi
fi

kill -INT $IP $KP 2>/dev/null; sleep 1; kill -KILL $IP $KP ${ECHOP:-} 2>/dev/null; wait 2>/dev/null
if [ "$result" != OK ]; then
  echo "--- dialling end"; tail -8 "$W/iran.log"
  echo "--- listening end"; tail -8 "$W/kharej.log"
fi
echo "RESULT $result $detail"

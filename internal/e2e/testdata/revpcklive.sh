#!/bin/bash
# revpcklive.sh — a reverse engine over transport = "pck", run for real between
# two network namespaces: Iran is the [server] (listens, forwards ports), the
# kharej is the [client] (dials back, serves the targets), a veth between them
# stands in for the path.
#
# TestReversePckOverARealPath runs it inside
#   unshare --map-auto --map-root-user --net --mount --fork
# so nothing here touches the host's network. The last line it prints is
# "RESULT OK <detail>" or "RESULT FAIL <detail>".
#
# Environment: BIN (required), W (a working directory, required),
# RECOVER (seconds the tunnel gets to carry traffic again after a link comes
# back up, default 20).
#
# What it checks, in order: a TCP echo and a 2 MB copy through a forwarded
# port, forwarded UDP datagrams of several sizes, then the interface of each end
# taken down for a few seconds and brought back up, one end at a time — the tunnel
# has to carry TCP and UDP again by itself, with no process restarted.
set -u
mkdir -p "$W"
RECOVER="${RECOVER:-20}"

mount -t tmpfs none /run 2>/dev/null; mkdir -p /run/netns
ip netns add iran; ip netns add kharej
ip link add name vi type veth peer name vk
ip link set vi netns iran; ip link set vk netns kharej
ip -n iran addr add 10.99.0.1/24 dev vi; ip -n kharej addr add 10.99.0.2/24 dev vk
for n in iran kharej; do
  ip -n $n link set lo up
  ip netns exec $n sysctl -qw net.ipv4.conf.all.rp_filter=0 net.ipv4.conf.default.rp_filter=0
done
ip -n iran link set vi up; ip -n kharej link set vk up

TOKEN="rpck-live-token-0123456789abcdef"
cat > "$W/iran.toml" <<EOT
[server]
bind_addr = "10.99.0.1:9000"
transport = "pck"
preset = "turbo"
token = "$TOKEN"
log_level = "info"
pck_interface = "vi"
accept_udp = true
ports = ["6001", "6002"]
EOT
cat > "$W/kharej.toml" <<EOT
[client]
remote_addr = "10.99.0.1:9000"
transport = "pck"
preset = "turbo"
token = "$TOKEN"
log_level = "info"
pck_interface = "vk"
connection_pool = 4
EOT

# Targets on the kharej: a TCP echo on 6001 and a UDP echo on 6002 (the Iran
# end forwards both protocols of each listed port).
ip netns exec kharej python3 -I - <<'PY' &
import socket, threading
def tcp():
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("127.0.0.1", 6001)); s.listen(64)
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
    u = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); u.bind(("127.0.0.1", 6002))
    while True:
        d, a = u.recvfrom(65535); u.sendto(d, a)
threading.Thread(target=udp, daemon=True).start()
tcp()
PY
ECHOP=$!

ip netns exec kharej "$BIN" -c "$W/kharej.toml" > "$W/kharej.log" 2>&1 &
KP=$!
sleep 0.5
ip netns exec iran "$BIN" -c "$W/iran.toml" > "$W/iran.log" 2>&1 &
IP=$!

tcpecho() { # size — a TCP round trip through Iran's forwarded port, compared byte for byte
  ip netns exec iran python3 -I -c '
import socket, os, sys, threading
size = int(sys.argv[1])
s = socket.create_connection(("127.0.0.1", 6001), 3); s.settimeout(10)
d = os.urandom(size)
threading.Thread(target=lambda: s.sendall(d), daemon=True).start()
r = b""
while len(r) < size:
    c = s.recv(65536)
    if not c: sys.exit("closed after %d of %d bytes" % (len(r), size))
    r += c
if r != d: sys.exit("the %d bytes came back different" % size)' "$1" 2>&1
}
udpecho() { # datagrams of several sizes through the forwarded UDP port, each back unchanged
  ip netns exec iran python3 -I -c '
import socket, os, sys
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.settimeout(2)
for n in (1, 200, 1200, 1472, 8000):
    d = os.urandom(n)
    for attempt in range(5):
        s.sendto(d, ("127.0.0.1", 6002))
        try:
            r, _ = s.recvfrom(65535)
        except socket.timeout:
            continue
        if r == d: break
        sys.exit("datagram of %d came back different" % n)
    else:
        sys.exit("datagram of %d never came back" % n)' 2>&1
}
waitfor() { # seconds — until both a TCP round trip and a UDP datagram cross; prints the seconds it took
  local t0 now
  t0=$(date +%s.%N)
  while :; do
    if tcpecho 1000 >/dev/null && udpecho >/dev/null; then
      echo "$(date +%s.%N) - $t0" | bc; return 0
    fi
    now=$(date +%s.%N)
    if [ "$(echo "$now - $t0 > $1" | bc)" = 1 ]; then return 1; fi
    sleep 0.25
  done
}
flap() { # ns dev seconds address — the interface goes down and comes back up, as a NIC that was reset does
  ip -n "$1" link set "$2" down
  sleep "$3"
  ip -n "$1" link set "$2" up
  ip -n "$1" addr replace "$4" dev "$2" 2>/dev/null
}

result=FAIL; detail="the tunnel never carried a TCP echo and a UDP datagram"
if up=$(waitfor 60); then
  if out=$(tcpecho 2000000); then
    if out=$(udpecho); then
      result=OK; detail="up in ${up:0:5}s; TCP 2 MB identical; UDP 1..8000 B echoed"
    else detail="UDP: $out"; fi
  else detail="TCP copy: $out"; fi
fi

# Each end's link down for 6 s and back up: the tunnel must come back by itself.
for end in "iran vi 10.99.0.1/24" "kharej vk 10.99.0.2/24"; do
  [ "$result" = OK ] || break
  set -- $end
  flap "$1" "$2" 6 "$3"
  if back=$(waitfor "$RECOVER"); then
    if out=$(tcpecho 1000000) && out=$(udpecho); then
      detail="$detail; $1's link down 6s: TCP and UDP back ${back:0:5}s after it came up"
    else result=FAIL; detail="$detail; after $1's link flap: $out"; fi
  else
    result=FAIL; detail="$detail; $1's link went down for 6s and the tunnel did not carry traffic again within ${RECOVER}s"
  fi
done
if [ "$result" = OK ] && { ! kill -0 $IP 2>/dev/null || ! kill -0 $KP 2>/dev/null; }; then
  result=FAIL; detail="$detail; an engine process died"
fi

kill -INT $IP $KP 2>/dev/null; sleep 1; kill -KILL $IP $KP $ECHOP 2>/dev/null; wait 2>/dev/null
if [ "$result" != OK ]; then
  echo "--- iran (server)"; tail -8 "$W/iran.log"
  echo "--- kharej (client)"; tail -8 "$W/kharej.log"
fi
echo "RESULT $result $detail"

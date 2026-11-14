//go:build linux

package network

import (
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/bpf"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

// A net.PacketConn that carries the tunnel's KCP datagrams inside TCP segments
// this process builds and reads itself — the "pck" transport.
//
// Every other TCP-family transport opens a socket and lets the kernel's stack
// do the work. That stack is also where a filtering box's easiest levers are:
// connection tracking has an opinion about the flow, netfilter can drop it
// before any socket sees it, and the handshake it performs is the thing most
// often matched on. This carrier steps around all of it. Receive is an
// AF_PACKET socket, which taps the device driver — upstream of conntrack, of
// every netfilter chain, and of reverse-path filtering, so a rule that drops the
// tunnel's packets does not stop them arriving here. Send builds the frame and
// hands it to the same driver.
//
// What it is NOT is an IP-spoofing transport. The source address on every packet
// is this machine's real one, so the peer can answer it and the reply is routed
// normally. The only thing forged is the impression that a TCP connection exists:
// there is no handshake, no kernel state, and no stack on either side — just
// segments that look exactly like an established connection's, with KCP inside
// providing the reliability a real stack would have.
//
// Two consequences follow, and both are handled here:
//
//   - The kernel is not listening on the port these segments are addressed to,
//     so it answers each one with a RST. That would tear the flow down at any
//     stateful middlebox in between. A narrow iptables rule drops those.
//   - Conntrack would create an entry for every one of these pseudo-flows, for
//     no benefit, and on a busy server that fills the table. They are marked
//     NOTRACK.
//
// Linux only, and needs root or CAP_NET_RAW.

// PcapCarrier is the pck carrier's tuning, carried in KCPSettings the same way
// the ICMP and spoof carriers are.
type PcapCarrier struct {
	// Port is the TCP port the tunnel's segments are addressed to on the
	// server. It is the tunnel port the operator configured, so what shows up
	// in a capture is the port they expect.
	Port uint16
	// Interface and GatewayMAC override the automatic egress lookup. Both empty
	// — the normal case — means the route to the peer decides. See
	// discoverEgress.
	Interface  string
	GatewayMAC string
	// Flags is the cycle of TCP flag combinations stamped on outgoing segments,
	// one per packet. Empty means PSH+ACK on every segment.
	Flags []TCPFlags
	// PeerIP is the server's address, known to the client before it sends
	// anything. The server leaves it empty and learns each client from the
	// packets that arrive, exactly as a UDP socket would.
	PeerIP string
	// Token is the tunnel's shared secret, used here only to derive the client's
	// source port. See pckClientPort.
	Token string
}

// pckLink is everything a carrier holds that belongs to one instance of its
// network interface: the route it found, and the sockets bound to that
// interface's index. When the interface is deleted and comes back — a veth or
// ppp/wg device recreated, a NIC hot-replugged — it has a new index and every
// one of these is dead for good, so the carrier opens a new pckLink and swaps it
// in whole (see relink). Readers and writers load it once per call.
type pckLink struct {
	egress *pckEgress

	rx     *os.File // AF_PACKET, wrapped so the runtime poller drives reads
	rxConn syscall.RawConn

	txRaw   *ipv4.RawConn
	txRawPC net.PacketConn
	txFile  *os.File // AF_PACKET send, when the link has an L2 header we can build

	// The send path, resolved once instead of per packet.
	//
	// sendFrame used to call txFile.SyscallConn() and build a fresh
	// SockaddrLinklayer on every datagram. Both are constant for the life of
	// the connection — the descriptor does not change and neither does the next
	// hop — and both allocate. At a few thousand packets a second that is two
	// allocations per packet handed to the garbage collector for no reason,
	// which shows up as CPU that climbs with the packet rate and has nothing to
	// do with the work being done.
	txConn syscall.RawConn
	txAddr *unix.SockaddrLinklayer
}

// close closes every socket of the link.
func (l *pckLink) close() {
	if l.rx != nil {
		_ = l.rx.Close()
	}
	if l.txFile != nil {
		_ = l.txFile.Close()
	}
	if l.txRaw != nil {
		_ = l.txRaw.Close()
	}
	if l.txRawPC != nil {
		_ = l.txRawPC.Close()
	}
}

// pckConn is one tunnel's carrier.
type pckConn struct {
	link    atomic.Pointer[pckLink]
	rxBatch pckRecvBatch // recvmmsg's arrays, reused; see pckbatch_linux.go
	txBatch pckSendBatch // sendmmsg's, likewise

	// What the egress was resolved from, so relink can resolve it again.
	toward     net.IP
	ifaceName  string // as configured; "" means the route decides
	gatewayMAC string
	// localIP is the address the port reservation and the guard rules name; a
	// link that comes back with another one cannot be swapped in.
	localIP net.IP

	relinkMu  sync.Mutex
	relinking atomic.Bool
	relinks   atomic.Uint64
	rdeadline atomic.Pointer[time.Time] // the last read deadline, for a new rx
	logf      atomic.Pointer[func(string, ...any)]

	port   uint16 // the port our segments are addressed TO on the peer
	local  uint16 // the port our segments come FROM, and that we accept
	server bool

	flags   []TCPFlags
	flagRot atomic.Uint32
	ipID    atomic.Uint32
	tsBase  uint32
	tsStart time.Time

	guard       *pckGuard
	reservation *os.File // bound TCP port; never listens or connects
	// tunnelID is pckTunnelID of the token: which tunnel's source range the
	// port came from, so closing the last carrier gives the range up.
	tunnelID string

	// holdsPort is set on the client side, where the source port is claimed out
	// of the tunnel's range and has to be handed back on Close. The server's
	// port is its listen port and is not allocated, so nothing is held.
	holdsPort bool

	// Per-peer sequence state. A real connection's numbers advance with the
	// bytes it has sent and acknowledge the bytes it has received; anything
	// tracking the flow checks exactly that, so it is tracked exactly that way.
	mu    sync.Mutex
	peers map[pckPeerKey]*pckPeer

	closed atomic.Bool

	// queueDrops counts segments the local transmit queue had no room for;
	// see writeFailed.
	queueDrops atomic.Uint64
	// queueSent counts the segments it took; the drops are a share of the two.
	queueSent atomic.Uint64
	// linkDrops counts what a downed link cost; see writeFailed and rxLinkDown.
	linkDrops atomic.Uint64
}

// The frame buffer pool both ReadFrom and WriteTo draw from lives in
// pckframe.go, alongside the framing it serves and free of a build tag, so the
// send path can be benchmarked and asserted on anywhere.

// pckPeer is the numbering of one conversation.
type pckPeer struct {
	used   int64 // unix nanoseconds of the last segment to or from it, for eviction
	sent   bool  // this carrier has sent to it: a peer the tunnel accepted
	addr   *net.UDPAddr
	seq    uint32 // our next sequence number
	ack    uint32 // the next byte we expect from them
	lastTS uint32 // their most recent timestamp, echoed back in ours
}

// pckPeerKey identifies a peer without building a string for every packet.
//
// The map used to be keyed on addr.String(), and every entry carried a
// "touched" time set on every packet in both directions — a time nothing ever
// read. Together they were a sixth of the CPU of a loaded pck tunnel: a string
// allocation and a clock read per packet, measured with pprof (time.Now alone
// was 15%, on a VM whose clock is not the cheap vDSO kind).
type pckPeerKey struct {
	ip   [4]byte
	port uint16
}

// PckOverhead is what the pck framing costs inside the path MTU, exported so
// the KCP layer can size its datagrams under it.
func PckOverhead() int { return pckOverhead }

// newPckConn opens the carrier. server decides only which end learns its peer
// from the wire and which is told; the framing is identical in both directions.
func newPckConn(server bool, listenPort uint16, carrier PcapCarrier) (net.PacketConn, error) {
	peer := net.ParseIP(carrier.PeerIP).To4()
	if !server && peer == nil {
		return nil, fmt.Errorf("pck: the server's IPv4 address is required on the client")
	}

	// The egress lookup needs somewhere to route toward. On the server, which
	// has no single peer, any public address resolves the default route, which
	// is the one its replies will take.
	toward := peer
	if toward == nil {
		toward = net.IPv4(1, 1, 1, 1).To4()
	}
	egress, err := discoverEgress(toward, carrier.Interface, carrier.GatewayMAC)
	if err != nil && server && carrier.Interface == "" {
		// A server refusing to start because it has no route to 1.1.1.1.
		//
		// That address is not a peer and nothing is ever sent to it; it is a
		// stand-in used to find the default route. So a box with no default
		// route — policy routing in another table, an IPv6-only default, a
		// private network, a namespace — could not run a pck listener at all,
		// and the reason it gave named an address the operator never
		// configured.
		//
		// The client keeps the strict behaviour, because its target is a real
		// peer: no route to that is a fault worth stopping for. The server's
		// replies go back to whoever reached it, so any interface that could
		// carry them will do, and an interface chosen wrongly is recoverable
		// with pck_interface where refusing to start is not.
		if name := firstUsableIface(); name != "" {
			egress, err = discoverEgress(toward, name, carrier.GatewayMAC)
		}
	}
	if err != nil {
		return nil, err
	}

	flags := carrier.Flags
	if len(flags) == 0 {
		flags = []TCPFlags{FlagPSH | FlagACK}
	}

	c := &pckConn{
		toward:     toward,
		ifaceName:  carrier.Interface,
		gatewayMAC: carrier.GatewayMAC,
		localIP:    egress.LocalIP,
		port:       carrier.Port,
		server:     server,
		flags:      flags,
		tsBase:     rand.Uint32(),
		tsStart:    time.Now(),
		peers:      make(map[pckPeerKey]*pckPeer),

		tunnelID: pckTunnelID(carrier.Token),
	}
	// The server is addressed on the tunnel port and answers from it. A client
	// sends from an ephemeral port of its own, as any connecting host would —
	// one sending from the same port it sends to would stand out on that alone.
	//
	// Each of the client's carriers takes a DIFFERENT port out of the tunnel's
	// range, and that is not cosmetic. KCP's listener demultiplexes purely on the
	// sender's address: `l.sessions[addr.String()]`. Every carrier sharing one
	// port meant every session in the pool arrived at the server looking like the
	// same peer, and kcp-go's rule for a packet that claims a new conversation on
	// an existing entry is to close the old one. So each pool connection killed
	// the session before it — the control channel included — and the tunnel spent
	// its life reconnecting: control channel up, pool dials, control channel dead,
	// timeout, restart. Distinct ports make each session a distinct peer, which is
	// what it always was. It also keeps the TCP sequence numbers of one flow
	// consistent, instead of N carriers each advancing their own on one 4-tuple.
	guardLo, guardHi := listenPort, listenPort
	// The rule sets v1.8.4 and earlier wrote for this tunnel, untagged, so an
	// upgrade clears whatever such a build left behind.
	legacyBase := legacyPckClientPortBase(carrier.Token)
	legacy := [][][]string{pckRules("", listenPort, listenPort)}
	if !server {
		legacy = [][][]string{pckRules("", legacyBase, legacyBase+pckPortSpan-1)}
	}
	if server {
		c.local = listenPort
		c.reservation, err = reservePckPort(egress.LocalIP, c.local)
	} else {
		base := pckClientPortBase(carrier.Token)
		c.local, c.reservation, err = reservePckClientPort(c.tunnelID, base, egress.LocalIP)
		c.holdsPort = err == nil
		guardLo, guardHi = base, base+pckPortSpan-1
	}
	if err != nil {
		return nil, err
	}

	// From here the port is claimed, so every path out has to give it back or
	// the range leaks one on each failed dial and eventually fills.
	release := func() {
		if c.reservation != nil {
			c.reservation.Close()
		}
		if c.holdsPort {
			releasePckClientPort(c.tunnelID, c.local)
			c.holdsPort = false
		}
	}

	link, err := c.openLink(egress)
	if err != nil {
		release()
		return nil, err
	}
	c.link.Store(link)
	// One rule set covers the whole range and is shared by every carrier in this
	// process, so a pool of sixteen does not install sixteen sets of rules.
	peerScope := ""
	if peer != nil {
		peerScope = peer.String()
	}
	rules := scopedPckRules(c.tunnelID, guardLo, guardHi, egress.Iface.Name, egress.LocalIP.String(), peerScope, carrier.Port)
	c.guard, err = installPckGuard(c.tunnelID, legacy, rules)
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Binding without listen keeps this port out of the kernel's TCP allocator,
// while leaving the raw carrier and the kernel's RST behaviour unchanged.
func reservePckPort(ip net.IP, port uint16) (*os.File, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, unix.IPPROTO_TCP)
	if err != nil {
		return nil, err
	}
	addr := &unix.SockaddrInet4{Port: int(port)}
	copy(addr.Addr[:], ip.To4())
	if err := unix.Bind(fd, addr); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("pck: cannot reserve TCP source port %d: %w", port, err)
	}
	return os.NewFile(uintptr(fd), "pck-port"), nil
}

func reservePckClientPort(id string, base uint16, ip net.IP) (uint16, *os.File, error) {
	// Keep occupied candidates claimed until the search finishes, so the
	// allocator advances instead of retrying the same kernel-owned port.
	var rejected []uint16
	defer func() {
		for _, port := range rejected {
			releasePckClientPort(id, port)
		}
	}()
	for i := 0; i < pckPortSpan; i++ {
		port, err := nextPckClientPort(base)
		if err != nil {
			return 0, nil, err
		}
		f, err := reservePckPort(ip, port)
		if err == nil {
			return port, f, nil
		}
		rejected = append(rejected, port)
		if !errors.Is(err, unix.EADDRINUSE) {
			return 0, nil, err
		}
	}
	return 0, nil, fmt.Errorf("pck: no available TCP source port in range %d-%d", base, base+pckPortSpan-1)
}

// resolveEgress finds the egress again, the way newPckConn found it first.
func (c *pckConn) resolveEgress() (*pckEgress, error) {
	e, err := discoverEgress(c.toward, c.ifaceName, c.gatewayMAC)
	if err != nil && c.server && c.ifaceName == "" {
		if name := firstUsableIface(); name != "" {
			e, err = discoverEgress(c.toward, name, c.gatewayMAC)
		}
	}
	return e, err
}

// openLink opens the receive and send sockets on egress.
func (c *pckConn) openLink(e *pckEgress) (*pckLink, error) {
	l := &pckLink{egress: e}
	if err := c.openRx(l); err != nil {
		return nil, err
	}
	if err := c.openTx(l); err != nil {
		l.rx.Close()
		return nil, err
	}
	return l, nil
}

// openRx opens the AF_PACKET receive socket and filters it down to this
// tunnel's segments in the kernel.
func (c *pckConn) openRx(l *pckLink) error {
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_IP)))
	if err != nil {
		if errors.Is(err, unix.EPERM) {
			return fmt.Errorf("pck: a packet socket needs root or CAP_NET_RAW: %w", err)
		}
		return fmt.Errorf("pck: could not open the packet socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{
		Protocol: htons(unix.ETH_P_IP),
		Ifindex:  l.egress.Iface.Index,
	}); err != nil {
		unix.Close(fd)
		return fmt.Errorf("pck: could not bind the packet socket to %s: %w", l.egress.Iface.Name, err)
	}
	// Filtering in the kernel rather than in the read loop is the difference
	// between waking on this tunnel's packets and waking on every packet the
	// machine receives. Best effort: parsePckFrame checks the port again, so a
	// kernel that refuses the filter costs CPU rather than correctness.
	if prog, err := pckBPFProgram(c.local, l.egress.LinkLen); err == nil {
		_ = attachSockFilter(fd, prog)
	}
	// A burst that arrives while the reader is busy is held here rather than
	// dropped. The default is small enough that a fast tunnel loses packets in
	// the kernel and blames the network.
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 8*1024*1024)

	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return fmt.Errorf("pck: could not set the packet socket non-blocking: %w", err)
	}
	// Handing the descriptor to os.File puts it under the runtime's poller, so
	// reads block in the scheduler rather than in a syscall and deadlines and
	// Close work without a race over the descriptor.
	l.rx = os.NewFile(uintptr(fd), "pck-rx")
	// For recvmmsg. Without it ReadBatch declines and reads go one at a time.
	if raw, err := l.rx.SyscallConn(); err == nil {
		l.rxConn = raw
	}
	return nil
}

// openTx opens the send path: a second packet socket where the link has an
// Ethernet header this process can build, and a raw IP socket where it does not.
func (c *pckConn) openTx(l *pckLink) error {
	if l.egress.Ethernet && l.egress.NextHop != nil {
		fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_IP)))
		if err == nil {
			// The send socket had no buffer set while the receive one had 8 MB.
			// This carrier writes one packet per syscall — kcp-go only reaches for
			// the batching path on a real UDP socket — so a burst from a full
			// window arrives here as a burst of individual sends, and the default
			// buffer is small enough that the tail of one is refused with EAGAIN.
			// That costs a poller round trip per dropped packet at exactly the
			// moment the tunnel is busiest.
			_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_SNDBUF, 8*1024*1024)
			if err = unix.SetNonblock(fd, true); err == nil {
				l.txFile = os.NewFile(uintptr(fd), "pck-tx")
				// Resolved once. Both are constant for the life of the socket,
				// and building them per packet was two allocations on the
				// hottest path in the carrier.
				if raw, rerr := l.txFile.SyscallConn(); rerr == nil {
					l.txConn = raw
					sll := &unix.SockaddrLinklayer{
						Protocol: htons(unix.ETH_P_IP),
						Ifindex:  l.egress.Iface.Index,
						Halen:    6,
					}
					copy(sll.Addr[:6], l.egress.NextHop)
					l.txAddr = sll
					return nil
				}
				// Without a usable RawConn there is no link-layer send path;
				// fall through to the raw IP socket below.
				_ = l.txFile.Close()
				l.txFile = nil
				return nil
			}
			unix.Close(fd)
		}
		// Fall through to the raw IP socket rather than fail: a machine that
		// cannot open a second packet socket can still send perfectly well
		// through the kernel's routing.
	}

	pc, err := net.ListenPacket("ip4:tcp", "0.0.0.0")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("pck: the raw send socket needs root or CAP_NET_RAW: %w", err)
		}
		return fmt.Errorf("pck: could not open the raw send socket: %w", err)
	}
	if l.egress.Iface != nil {
		_ = bindPacketConnToInterface(pc, l.egress.Iface.Name)
	}
	raw, err := ipv4.NewRawConn(pc)
	if err != nil {
		pc.Close()
		return fmt.Errorf("pck: could not take control of the IP header: %w", err)
	}
	l.txRawPC, l.txRaw = pc, raw
	return nil
}

// L2 reports whether frames are being injected at the link layer, which is the
// path that bypasses the host's routing and output chains entirely. The carrier
// says so once at startup, because the difference matters and is invisible.
func (c *pckConn) L2() bool { return c.link.Load().txFile != nil }

// GuardInstalled reports whether the kernel's interfering replies are being
// suppressed. Without it the tunnel usually still works and occasionally dies
// for reasons nothing explains, which is worth a warning.
func (c *pckConn) GuardInstalled() bool { return c.guard.Installed() }

// PckDiag returns a one-line summary of what the carrier discovered and
// installed, for the startup log. When a pck tunnel never connects this is the
// first thing to read: a wrong egress interface, an unresolved next hop, or an
// absent RST-guard each shows here, and each is otherwise invisible. It is what
// makes L2() and GuardInstalled() reach a human.
func (c *pckConn) PckDiag() string {
	role := "client"
	if c.server {
		role = "server"
	}
	l := c.link.Load()
	send := "kernel-routed raw IP (no L2 injection)"
	nextHop := "kernel"
	if l.txFile != nil {
		send = "L2 frame injection"
		if l.egress.NextHop != nil {
			nextHop = l.egress.NextHop.String()
		}
	}
	guard := "installed (kernel RSTs dropped, flow untracked)"
	if !c.GuardInstalled() {
		guard = "MISSING — kernel RSTs are NOT suppressed; the flow will drop at any stateful hop. Install iptables."
	}
	return fmt.Sprintf("pck %s ready: iface=%s src=%s:%d peer-tcp-port=%d send=%s next-hop=%s rst-guard=%s",
		role, l.egress.Iface.Name, l.egress.LocalIP, c.local, c.port, send, nextHop, guard)
}

// timestamp is our TCP timestamp clock: a random base advancing at the 1 kHz a
// Linux host uses, so the value moves the way a real one does.
func (c *pckConn) timestamp() uint32 {
	return c.tsBase + uint32(time.Since(c.tsStart)/time.Millisecond)
}

// peerFor returns the numbering for one peer, starting a fresh conversation the
// first time it is seen.
func (c *pckConn) peerFor(addr *net.UDPAddr, sending bool) *pckPeer {
	var key pckPeerKey
	copy(key.ip[:], addr.IP.To4())
	key.port = uint16(addr.Port)
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.peers[key]
	if p == nil {
		// The table is filled from segments nobody has authenticated —
		// anything with a payload aimed at this port — and forged sources are
		// free, so it is bounded. Peers this carrier has sent to are the ones
		// the tunnel above has accepted, so they are kept: at the cap, a batch
		// of the others makes room in one pass, and only when there are none
		// of those does the oldest peer go. A batch rather than one entry, so
		// a flood of new sources costs one scan per batch, not one per packet,
		// under the lock real segments take too.
		if len(c.peers) >= pckMaxPeers {
			c.evictLocked()
		}
		p = &pckPeer{
			addr: &net.UDPAddr{IP: append(net.IP(nil), addr.IP...), Port: addr.Port},
			// A connection's first sequence number is unpredictable, and a
			// stack that started every one at the same place would be obvious.
			seq: rand.Uint32(),
		}
		c.peers[key] = p
	}
	p.used = time.Now().UnixNano()
	if sending {
		p.sent = true
	}
	return p
}

// evictLocked frees room in the peer table: up to pckEvictBatch peers never
// sent to (map order, which is random), or failing that the one idle longest.
func (c *pckConn) evictLocked() {
	freed := 0
	var oldest pckPeerKey
	var oldestUsed int64
	haveOldest := false
	for k, v := range c.peers {
		if !v.sent {
			delete(c.peers, k)
			if freed++; freed >= pckEvictBatch {
				return
			}
			continue
		}
		if !haveOldest || v.used < oldestUsed {
			oldest, oldestUsed, haveOldest = k, v.used, true
		}
	}
	if freed == 0 && haveOldest {
		delete(c.peers, oldest)
	}
}

// pckMaxPeers bounds how many sources the carrier keeps sequence state for. A
// tunnel has one peer; a reverse server has one per pool session of one client.
const pckMaxPeers = 1024

// pckEvictBatch is how many never-sent-to peers one eviction pass removes.
const pckEvictBatch = pckMaxPeers / 8

// WriteTo builds one segment carrying p and puts it on the wire.
func (c *pckConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	dst, ok := toUDPAddr(addr)
	if !ok {
		return 0, net.InvalidAddrError("pck: unusable destination address")
	}
	peer := c.peerFor(dst, true)
	l := c.link.Load()

	c.mu.Lock()
	seq, ack, tsEcr := peer.seq, peer.ack, peer.lastTS
	// The sequence number advances by the bytes sent, exactly as a real
	// connection's does, so a middlebox following the stream sees it stay
	// consistent instead of jumping.
	peer.seq += uint32(len(p))
	c.mu.Unlock()

	f := c.flags[int(c.flagRot.Add(1)-1)%len(c.flags)]
	id := uint16(c.ipID.Add(1))

	// The frame is assembled into a pooled buffer rather than built a layer at a
	// time into three fresh ones. This is the hot path — one call, and one
	// syscall, per packet the tunnel sends — so the three allocations and three
	// copies of the payload the layered version cost were being paid for every
	// datagram. See assemblePckFrame.
	bp := pckFrameBuffers.Get().(*[]byte)
	defer pckFrameBuffers.Put(bp)
	buf := *bp
	if n := pckFrameLen(len(p)); n > len(buf) {
		// Larger than the pooled buffer: fall back to a one-off rather than
		// silently truncating. KCP is sized well under this, so it does not
		// happen in practice.
		buf = make([]byte, n)
	}

	frame := assemblePckFrame(buf, pckFrameParams{
		SrcMAC: l.egress.SrcMAC, DstMAC: l.egress.NextHop,
		SrcIP: l.egress.LocalIP, DstIP: dst.IP.To4(),
		SrcPort: c.local, DstPort: uint16(dst.Port),
		Seq: seq, Ack: ack, Flags: f,
		TSVal: c.timestamp(), TSEcr: tsEcr,
		ID: id, Payload: p,
	})

	if l.txFile != nil {
		if err := c.sendFrame(l, frame); err != nil {
			return c.writeFailed(l, len(p), err)
		}
		c.queueSent.Add(1)
		return len(p), nil
	}

	// No link-layer injection available: hand the IP payload to the raw socket
	// and let the kernel do the L2 work. The TCP segment is the same bytes the
	// frame already holds, so nothing is rebuilt.
	tcp := frame[pckTCPOffset:]
	h := &ipv4.Header{
		Version:  ipv4.Version,
		Len:      ipv4.HeaderLen,
		TOS:      46 << 2, // DSCP 46 (EF), as the L2 path sets in its own header
		TotalLen: ipv4.HeaderLen + len(tcp),
		ID:       int(id),
		Flags:    ipv4.DontFragment,
		TTL:      64,
		Protocol: 6,
		Src:      l.egress.LocalIP,
		Dst:      dst.IP.To4(),
	}
	if err := l.txRaw.WriteTo(h, tcp, nil); err != nil {
		return c.writeFailed(l, len(p), err)
	}
	c.queueSent.Add(1)
	return len(p), nil
}

// writeFailed decides what a failed send means to the tunnel above.
//
// A full transmit queue is a lost packet, not a broken socket. A packet or raw
// socket reports it — ENOBUFS when the qdisc had no room — where a UDP socket
// says nothing and drops the datagram, and KCP, which sits on this, treats any
// error from WriteTo as the end of the session: it closes it for good and
// discards the rest of what it was sending. So a pck tunnel pushed hard enough
// to fill its own queue once stopped carrying anything and never came back,
// while the same tunnel over UDP recovered on its own. Found by the Connection
// Test: echoes passed, a 1 MB transfer both ways stalled at two thirds, and
// the kharej had logged "no buffer space available".
//
// So it is reported the way UDP would have it: sent. KCP retransmits the
// segment as it does any lost on the path. Every other error is returned, bar
// the one below.
//
// A link that is down is the same: the segment is lost, and the tunnel is told
// nothing, so KCP keeps its session and retransmits once the link is back. The
// send used to fail with ENETDOWN (a packet socket) or ENETUNREACH (the raw
// socket, once the kernel dropped the connected route with the link), KCP read
// that as the end of the session, and the reverse tunnel's control channel never
// came back after the interface went down and up.
//
// An interface that is gone altogether — deleted, with nothing yet in its
// place — answers ENXIO or ENODEV, as the index the socket names no longer
// exists. That segment is lost too, and the carrier starts reopening itself on
// whatever has the interface's name once it is back (relink). A send that
// fails because relink closed the old socket under it is the same loss.
func (c *pckConn) writeFailed(l *pckLink, n int, err error) (int, error) {
	if errors.Is(err, unix.ENOBUFS) || errors.Is(err, unix.EAGAIN) {
		c.queueDrops.Add(1)
		return n, nil
	}
	if pckLinkDownErr(err) {
		c.linkDrops.Add(1)
		return n, nil
	}
	if pckLinkGoneErr(err) {
		c.linkDrops.Add(1)
		c.relinkAsync(l)
		return n, nil
	}
	if c.link.Load() != l && !c.closed.Load() {
		c.linkDrops.Add(1)
		return n, nil
	}
	return 0, err
}

// pckLinkGoneErr reports an error that means the socket's interface does not
// exist any more.
func pckLinkGoneErr(err error) bool {
	return errors.Is(err, unix.ENXIO) || errors.Is(err, unix.ENODEV)
}

// pckLinkDownErr reports an error that only means the path is down right now:
// the interface is, or it has no route to the peer while it is.
func pckLinkDownErr(err error) bool {
	return errors.Is(err, unix.ENETDOWN) || errors.Is(err, unix.ENETUNREACH) || errors.Is(err, unix.EHOSTUNREACH)
}

// Bounds of the wait rxLinkDown puts between receive errors that repeat.
const (
	pckLinkWaitMin = 10 * time.Millisecond
	pckLinkWaitMax = time.Second
)

// rxLinkDown decides whether a receive error is only the interface going down.
//
// An AF_PACKET socket bound to an interface reports ENETDOWN once, when that
// interface goes down, and the kernel re-arms it by itself when the interface
// comes back up. But kcp-go's listener ends its read loop on the first error
// from the carrier, for good, so one NIC down/up left a reverse pck server deaf
// to every client from then on, with its process still up. Such an error is
// absorbed here, and the read goes on. The wait before the next read grows
// with each repeat in a row (streak, the caller's), up to pckLinkWaitMax, so a
// link that stays down costs next to no CPU. Anything else is the caller's.
//
// The kernel reports the same ENETDOWN when the interface is deleted, and then
// never re-arms the socket: it is unbound from an index that no longer exists,
// and an interface that comes back under the same name has a new one. So the
// interface is looked up; when it is gone, the carrier starts reopening on its
// replacement in the background (relink) and the read goes on on the old
// socket, which hears nothing — its deadline still ends it, and relink closing
// it once the new link is in moves the read over.
func (c *pckConn) rxLinkDown(l *pckLink, err error, streak *int) bool {
	if !errors.Is(err, unix.ENETDOWN) || c.closed.Load() {
		return false
	}
	if pckLinkGone(l) {
		c.linkDrops.Add(1)
		c.relinkAsync(l)
		return true
	}
	w := pckLinkWaitMin
	for i := 0; i < *streak && w < pckLinkWaitMax; i++ {
		w *= 2
	}
	if w > pckLinkWaitMax {
		w = pckLinkWaitMax
	}
	*streak++
	c.linkDrops.Add(1)
	time.Sleep(w)
	return true
}

// pckLinkGone reports whether the interface l was opened on has been deleted:
// no interface has its index any more, or one with another name does.
func pckLinkGone(l *pckLink) bool {
	in, err := net.InterfaceByIndex(l.egress.Iface.Index)
	return err != nil || in.Name != l.egress.Iface.Name
}

// Bounds of the wait between attempts to reopen on a deleted interface.
const (
	pckRelinkWaitMin = 100 * time.Millisecond
	pckRelinkWaitMax = 2 * time.Second
)

// relinkAsync starts relink in the background unless one is running already.
// Neither a send nor a read waits for an interface to come back.
func (c *pckConn) relinkAsync(l *pckLink) {
	if c.closed.Load() || !c.relinking.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer c.relinking.Store(false)
		c.relink(l)
	}()
}

// relink replaces the link old, whose interface was deleted, with one opened on
// the interface that now has its name (or, for a carrier that follows the
// route, on whatever the route now names). It waits for one to appear, with a
// growing pause, and returns true once the new link is in place — or at once,
// when old was replaced already — and false when the carrier is closed first.
// KCP above never notices beyond the packets lost in between, so its sessions
// carry on as they do after a link that only went down.
//
// The new link must have the address the old one had: the TCP port reservation
// and the RST guard name it. One that came back with another address is waited
// past, in case the address follows, and said so in the log.
func (c *pckConn) relink(old *pckLink) bool {
	said := ""
	wait := pckRelinkWaitMin
	for !c.closed.Load() {
		if c.link.Load() != old {
			return true
		}
		e, err := c.resolveEgress()
		if err == nil && !e.LocalIP.Equal(c.localIP) {
			err = fmt.Errorf("it came back with the address %s, and this tunnel runs on %s", e.LocalIP, c.localIP)
		}
		var l *pckLink
		if err == nil {
			l, err = c.openLink(e)
		}
		if err == nil {
			// The swap, under relinkMu, so Close cannot run between the check
			// and the store and leave the new link open.
			c.relinkMu.Lock()
			if c.closed.Load() || c.link.Load() != old {
				c.relinkMu.Unlock()
				l.close()
				return !c.closed.Load()
			}
			c.link.Store(l)
			if d := c.rdeadline.Load(); d != nil {
				_ = l.rx.SetReadDeadline(*d)
			}
			c.relinkMu.Unlock()
			old.close()
			c.relinks.Add(1)
			if pckRelinkNote(e.Iface.Name + " back") {
				c.log("pck: the interface %s is back (index %d, was %d); the carrier reopened on it",
					e.Iface.Name, e.Iface.Index, old.egress.Iface.Index)
			}
			return true
		}
		if msg := err.Error(); msg != said {
			said = msg
			if pckRelinkNote(old.egress.Iface.Name + " gone " + msg) {
				c.log("pck: the interface %s is gone; waiting for it to come back: %v", old.egress.Iface.Name, err)
			}
		}
		time.Sleep(wait)
		if wait *= 2; wait > pckRelinkWaitMax {
			wait = pckRelinkWaitMax
		}
	}
	return false
}

// pckRelinkNotes keeps a pool of carriers on one interface — a reverse
// tunnel's eight, say — from each logging the same outage: a note is said once
// per pckRelinkQuiet, process-wide.
var pckRelinkNotes = struct {
	sync.Mutex
	said map[string]time.Time
}{said: map[string]time.Time{}}

const pckRelinkQuiet = 10 * time.Second

func pckRelinkNote(key string) bool {
	pckRelinkNotes.Lock()
	defer pckRelinkNotes.Unlock()
	now := time.Now()
	if t, ok := pckRelinkNotes.said[key]; ok && now.Sub(t) < pckRelinkQuiet {
		return false
	}
	for k, t := range pckRelinkNotes.said {
		if now.Sub(t) >= pckRelinkQuiet {
			delete(pckRelinkNotes.said, k)
		}
	}
	pckRelinkNotes.said[key] = now
	return true
}

// Relinks is how many times the carrier reopened on a recreated interface.
func (c *pckConn) Relinks() uint64 { return c.relinks.Load() }

// SetLogf gives the carrier somewhere to say what it does on its own, such as
// reopening on a recreated interface. Without one it says nothing.
func (c *pckConn) SetLogf(f func(string, ...any)) { c.logf.Store(&f) }

func (c *pckConn) log(format string, args ...any) {
	if f := c.logf.Load(); f != nil && *f != nil {
		(*f)(format, args...)
	}
}

// LinkDrops is how many segments, sent or received, a downed link cost: sends
// that failed because the path was down, and receive errors absorbed.
func (c *pckConn) LinkDrops() uint64 { return c.linkDrops.Load() }

// QueueDrops is how many segments were dropped because the local transmit
// queue was full.
func (c *pckConn) QueueDrops() uint64 { return c.queueDrops.Load() }

// TxStats is how many segments the local transmit queue refused and how many it
// took, over the life of the connection. The path control reads the share the
// first are of the two (see PathControl).
func (c *pckConn) TxStats() (refused, sent uint64) { return c.queueDrops.Load(), c.queueSent.Load() }

// sendFrame writes a finished Ethernet frame to the packet socket, taking the
// descriptor from the runtime so it cannot be closed underneath the syscall.
//
// The RawConn and the destination are resolved once at open; see the fields on
// pckConn for why they used to be rebuilt per packet and why that mattered.
func (c *pckConn) sendFrame(l *pckLink, frame []byte) error {
	sc, sll := l.txConn, l.txAddr
	if sc == nil {
		// Only reachable if the socket was never opened for link-layer
		// injection, which the caller has already checked.
		return net.ErrClosed
	}

	var sendErr error
	err := sc.Write(func(fd uintptr) bool {
		sendErr = unix.Sendto(int(fd), frame, 0, sll)
		// EAGAIN means the device queue is full; let the poller wait and retry.
		return !errors.Is(sendErr, unix.EAGAIN)
	})
	if err != nil {
		return err
	}
	return sendErr
}

// ReadFrom returns the next datagram addressed to this tunnel, with the peer it
// came from. Anything else on the wire is skipped rather than reported: a
// packet socket sees a great deal that is not ours.
func (c *pckConn) ReadFrom(p []byte) (int, net.Addr, error) {
	// The frame buffer comes from a pool rather than being allocated here. KCP
	// calls ReadFrom once per packet, so a fresh 64 KiB slice per call meant a
	// 64 KiB allocation for every packet the tunnel received — at a few thousand
	// packets a second that is the garbage collector, not the network, deciding
	// how fast the tunnel runs. Nothing in the buffer outlives the call: the
	// payload is copied into p before returning, so it is safe to hand back.
	bp := pckFrameBuffers.Get().(*[]byte)
	defer pckFrameBuffers.Put(bp)
	buf := *bp
	down := 0
	for {
		l := c.link.Load()
		n, err := l.rx.Read(buf)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return 0, nil, os.ErrDeadlineExceeded
			}
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if c.link.Load() != l && !c.closed.Load() {
				continue // relink closed the old socket under this read
			}
			if c.rxLinkDown(l, err, &down) {
				continue
			}
			return 0, nil, err
		}
		payload, addr, ok := c.accept(l, buf[:n])
		if !ok {
			continue
		}
		return copy(p, payload), addr, nil
	}
}

// accept checks one captured frame and, when it is this tunnel's, records what
// it acknowledges and returns its payload and sender. The payload points into
// frame.
func (c *pckConn) accept(l *pckLink, frame []byte) ([]byte, *net.UDPAddr, bool) {
	seg, ok := parsePckFrame(frame, l.egress.LinkLen, c.local)
	if !ok || len(seg.Payload) == 0 {
		return nil, nil, false
	}
	// Our own outgoing frames come back on the packet socket. They are
	// addressed FROM our port, so the destination-port filter usually
	// excludes them, but a client that happened to pick the server's port
	// would see its own traffic; drop anything sourced from us.
	if seg.SrcIP.Equal(l.egress.LocalIP) && seg.SrcPort == c.local {
		return nil, nil, false
	}
	addr := &net.UDPAddr{IP: seg.SrcIP, Port: int(seg.SrcPort)}
	peer := c.peerFor(addr, false)

	c.mu.Lock()
	// Acknowledge what we have actually received, as a real receiver does.
	if next := seg.Seq + uint32(len(seg.Payload)); peer.ack == 0 || int32(next-peer.ack) > 0 {
		peer.ack = next
	}
	if seg.TSVal != 0 {
		peer.lastTS = seg.TSVal
	}
	c.mu.Unlock()
	return seg.Payload, addr, true
}

func (c *pckConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	c.guard.remove()
	// Under relinkMu, so a relink in progress cannot swap in a link after this
	// one is closed: it sees closed and gives up.
	c.relinkMu.Lock()
	if l := c.link.Load(); l != nil {
		l.close()
	}
	c.relinkMu.Unlock()
	if c.reservation != nil {
		_ = c.reservation.Close()
	}
	if c.holdsPort {
		releasePckClientPort(c.tunnelID, c.local)
	}
	return nil
}

// LocalAddr reports the address our segments come from, which is what a caller
// asking "where am I" wants to know.
func (c *pckConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: c.localIP, Port: int(c.local)}
}

func (c *pckConn) SetDeadline(t time.Time) error { return c.SetReadDeadline(t) }

// SetReadDeadline is remembered as well as set, so a link swapped in later by
// relink carries it too.
func (c *pckConn) SetReadDeadline(t time.Time) error {
	c.rdeadline.Store(&t)
	return c.link.Load().rx.SetReadDeadline(t)
}
func (c *pckConn) SetWriteDeadline(t time.Time) error { return nil }

// toUDPAddr accepts the address shapes KCP hands a PacketConn.
func toUDPAddr(a net.Addr) (*net.UDPAddr, bool) {
	switch v := a.(type) {
	case *net.UDPAddr:
		if v.IP.To4() != nil {
			return v, true
		}
	case *net.IPAddr:
		if v.IP.To4() != nil {
			return &net.UDPAddr{IP: v.IP}, true
		}
	}
	return nil, false
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// The client's source-port range and the firewall rules written against it are
// pure arithmetic, so they live in pckport.go where a test can reach them on any
// platform.

// attachSockFilter installs an assembled classic BPF program on a raw file
// descriptor.
func attachSockFilter(fd int, raw []bpf.RawInstruction) error {
	filters := make([]unix.SockFilter, len(raw))
	for i, ins := range raw {
		filters[i] = unix.SockFilter{Code: ins.Op, Jt: ins.Jt, Jf: ins.Jf, K: ins.K}
	}
	fprog := &unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	return unix.SetsockoptSockFprog(fd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, fprog)
}

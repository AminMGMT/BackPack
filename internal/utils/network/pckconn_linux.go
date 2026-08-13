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

// pckConn is one tunnel's carrier.
type pckConn struct {
	rx      *os.File // AF_PACKET, wrapped so the runtime poller drives reads
	txRaw   *ipv4.RawConn
	txRawPC net.PacketConn
	txFile  *os.File // AF_PACKET send, when the link has an L2 header we can build

	egress *pckEgress
	port   uint16 // the port our segments are addressed TO on the peer
	local  uint16 // the port our segments come FROM, and that we accept
	server bool

	flags   []TCPFlags
	flagRot atomic.Uint32
	ipID    atomic.Uint32
	tsBase  uint32
	tsStart time.Time

	guard *pckGuard

	// Per-peer sequence state. A real connection's numbers advance with the
	// bytes it has sent and acknowledge the bytes it has received; anything
	// tracking the flow checks exactly that, so it is tracked exactly that way.
	mu    sync.Mutex
	peers map[string]*pckPeer

	closed atomic.Bool
}

// The frame buffer pool both ReadFrom and WriteTo draw from lives in
// pckframe.go, alongside the framing it serves and free of a build tag, so the
// send path can be benchmarked and asserted on anywhere.

// pckPeer is the numbering of one conversation.
type pckPeer struct {
	addr    *net.UDPAddr
	seq     uint32 // our next sequence number
	ack     uint32 // the next byte we expect from them
	lastTS  uint32 // their most recent timestamp, echoed back in ours
	touched time.Time
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
	if err != nil {
		return nil, err
	}

	flags := carrier.Flags
	if len(flags) == 0 {
		flags = []TCPFlags{FlagPSH | FlagACK}
	}

	c := &pckConn{
		egress:  egress,
		port:    carrier.Port,
		server:  server,
		flags:   flags,
		tsBase:  rand.Uint32(),
		tsStart: time.Now(),
		peers:   make(map[string]*pckPeer),
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
	if server {
		c.local = listenPort
	} else {
		base := pckClientPortBase(carrier.Token)
		c.local = nextPckClientPort(base)
		guardLo, guardHi = base, base+pckPortSpan-1
	}

	if err := c.openRx(); err != nil {
		return nil, err
	}
	if err := c.openTx(); err != nil {
		c.rx.Close()
		return nil, err
	}
	// One rule set covers the whole range and is shared by every carrier in this
	// process, so a pool of sixteen does not install sixteen sets of rules.
	c.guard = installPckGuard(guardLo, guardHi)
	return c, nil
}

// openRx opens the AF_PACKET receive socket and filters it down to this
// tunnel's segments in the kernel.
func (c *pckConn) openRx() error {
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_IP)))
	if err != nil {
		if errors.Is(err, unix.EPERM) {
			return fmt.Errorf("pck: a packet socket needs root or CAP_NET_RAW: %w", err)
		}
		return fmt.Errorf("pck: could not open the packet socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{
		Protocol: htons(unix.ETH_P_IP),
		Ifindex:  c.egress.Iface.Index,
	}); err != nil {
		unix.Close(fd)
		return fmt.Errorf("pck: could not bind the packet socket to %s: %w", c.egress.Iface.Name, err)
	}
	// Filtering in the kernel rather than in the read loop is the difference
	// between waking on this tunnel's packets and waking on every packet the
	// machine receives. Best effort: parsePckFrame checks the port again, so a
	// kernel that refuses the filter costs CPU rather than correctness.
	if prog, err := pckBPFProgram(c.local, c.egress.LinkLen); err == nil {
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
	c.rx = os.NewFile(uintptr(fd), "pck-rx")
	return nil
}

// openTx opens the send path: a second packet socket where the link has an
// Ethernet header this process can build, and a raw IP socket where it does not.
func (c *pckConn) openTx() error {
	if c.egress.Ethernet && c.egress.NextHop != nil {
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
				c.txFile = os.NewFile(uintptr(fd), "pck-tx")
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
	if c.egress.Iface != nil {
		_ = bindPacketConnToInterface(pc, c.egress.Iface.Name)
	}
	raw, err := ipv4.NewRawConn(pc)
	if err != nil {
		pc.Close()
		return fmt.Errorf("pck: could not take control of the IP header: %w", err)
	}
	c.txRawPC, c.txRaw = pc, raw
	return nil
}

// L2 reports whether frames are being injected at the link layer, which is the
// path that bypasses the host's routing and output chains entirely. The carrier
// says so once at startup, because the difference matters and is invisible.
func (c *pckConn) L2() bool { return c.txFile != nil }

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
	send := "kernel-routed raw IP (no L2 injection)"
	nextHop := "kernel"
	if c.L2() {
		send = "L2 frame injection"
		if c.egress.NextHop != nil {
			nextHop = c.egress.NextHop.String()
		}
	}
	guard := "installed (kernel RSTs dropped, flow untracked)"
	if !c.GuardInstalled() {
		guard = "MISSING — kernel RSTs are NOT suppressed; the flow will drop at any stateful hop. Install iptables."
	}
	return fmt.Sprintf("pck %s ready: iface=%s src=%s:%d peer-tcp-port=%d send=%s next-hop=%s rst-guard=%s",
		role, c.egress.Iface.Name, c.egress.LocalIP, c.local, c.port, send, nextHop, guard)
}

// timestamp is our TCP timestamp clock: a random base advancing at the 1 kHz a
// Linux host uses, so the value moves the way a real one does.
func (c *pckConn) timestamp() uint32 {
	return c.tsBase + uint32(time.Since(c.tsStart)/time.Millisecond)
}

// peerFor returns the numbering for one peer, starting a fresh conversation the
// first time it is seen.
func (c *pckConn) peerFor(addr *net.UDPAddr) *pckPeer {
	key := addr.String()
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.peers[key]
	if p == nil {
		p = &pckPeer{
			addr: &net.UDPAddr{IP: append(net.IP(nil), addr.IP...), Port: addr.Port},
			// A connection's first sequence number is unpredictable, and a
			// stack that started every one at the same place would be obvious.
			seq: rand.Uint32(),
		}
		c.peers[key] = p
	}
	p.touched = time.Now()
	return p
}

// WriteTo builds one segment carrying p and puts it on the wire.
func (c *pckConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	dst, ok := toUDPAddr(addr)
	if !ok {
		return 0, net.InvalidAddrError("pck: unusable destination address")
	}
	peer := c.peerFor(dst)

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
		SrcMAC: c.egress.SrcMAC, DstMAC: c.egress.NextHop,
		SrcIP: c.egress.LocalIP, DstIP: dst.IP.To4(),
		SrcPort: c.local, DstPort: uint16(dst.Port),
		Seq: seq, Ack: ack, Flags: f,
		TSVal: c.timestamp(), TSEcr: tsEcr,
		ID: id, Payload: p,
	})

	if c.txFile != nil {
		if err := c.sendFrame(frame); err != nil {
			return 0, err
		}
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
		Src:      c.egress.LocalIP,
		Dst:      dst.IP.To4(),
	}
	if err := c.txRaw.WriteTo(h, tcp, nil); err != nil {
		return 0, err
	}
	return len(p), nil
}

// sendFrame writes a finished Ethernet frame to the packet socket, taking the
// descriptor from the runtime so it cannot be closed underneath the syscall.
func (c *pckConn) sendFrame(frame []byte) error {
	sc, err := c.txFile.SyscallConn()
	if err != nil {
		return err
	}
	sll := &unix.SockaddrLinklayer{
		Protocol: htons(unix.ETH_P_IP),
		Ifindex:  c.egress.Iface.Index,
		Halen:    6,
	}
	copy(sll.Addr[:6], c.egress.NextHop)

	var sendErr error
	err = sc.Write(func(fd uintptr) bool {
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
	for {
		n, err := c.rx.Read(buf)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return 0, nil, os.ErrDeadlineExceeded
			}
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return 0, nil, err
		}
		seg, ok := parsePckFrame(buf[:n], c.egress.LinkLen, c.local)
		if !ok || len(seg.Payload) == 0 {
			continue
		}
		// Our own outgoing frames come back on the packet socket. They are
		// addressed FROM our port, so the destination-port filter usually
		// excludes them, but a client that happened to pick the server's port
		// would see its own traffic; drop anything sourced from us.
		if seg.SrcIP.Equal(c.egress.LocalIP) && seg.SrcPort == c.local {
			continue
		}
		addr := &net.UDPAddr{IP: seg.SrcIP, Port: int(seg.SrcPort)}
		peer := c.peerFor(addr)

		c.mu.Lock()
		// Acknowledge what we have actually received, as a real receiver does.
		if next := seg.Seq + uint32(len(seg.Payload)); peer.ack == 0 || int32(next-peer.ack) > 0 {
			peer.ack = next
		}
		if seg.TSVal != 0 {
			peer.lastTS = seg.TSVal
		}
		c.mu.Unlock()

		return copy(p, seg.Payload), addr, nil
	}
}

func (c *pckConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	c.guard.remove()
	if c.rx != nil {
		_ = c.rx.Close()
	}
	if c.txFile != nil {
		_ = c.txFile.Close()
	}
	if c.txRaw != nil {
		_ = c.txRaw.Close()
	}
	if c.txRawPC != nil {
		_ = c.txRawPC.Close()
	}
	return nil
}

// LocalAddr reports the address our segments come from, which is what a caller
// asking "where am I" wants to know.
func (c *pckConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: c.egress.LocalIP, Port: int(c.local)}
}

func (c *pckConn) SetDeadline(t time.Time) error      { return c.rx.SetDeadline(t) }
func (c *pckConn) SetReadDeadline(t time.Time) error  { return c.rx.SetReadDeadline(t) }
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

//go:build linux

package network

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xtaci/kcp-go/v5"
)

// The xdi carrier, end to end, with the raw socket replaced by an in-memory
// one — everything above it, kcp-go included, is the real thing.
//
// A raw ICMP socket needs privilege, so until the socket was made injectable
// none of this could be exercised at all, and the transport's central fault
// lived behind that: every session of a tunnel looked identical on the wire, so
// kcp-go's listener — which keys sessions on the address the carrier reports —
// collapsed them onto one entry and closed each session as the next arrived.
// The control channel came up, the first pool connection killed it, and the
// tunnel flapped forever while calling itself connected.

// icmpWire is a host's-eye view of ICMP: a packet sent to a host is delivered
// to EVERY socket open on it, because that is what a raw socket receives. It is
// the property that makes the demultiplexing this carrier does necessary.
type icmpWire struct {
	mu    sync.Mutex
	hosts map[string][]*fakeICMPSocket
}

type icmpPkt struct {
	data []byte
	src  net.IP
}

func newICMPWire() *icmpWire { return &icmpWire{hosts: map[string][]*fakeICMPSocket{}} }

func (w *icmpWire) socket(host net.IP) *fakeICMPSocket {
	s := &fakeICMPSocket{wire: w, ip: host, in: make(chan icmpPkt, 1024), closed: make(chan struct{})}
	w.mu.Lock()
	w.hosts[host.String()] = append(w.hosts[host.String()], s)
	w.mu.Unlock()
	return s
}

type fakeICMPSocket struct {
	wire   *icmpWire
	ip     net.IP
	in     chan icmpPkt
	closed chan struct{}

	mu       sync.Mutex
	deadline time.Time
}

// ReadFrom mirrors what x/net/icmp hands up on Linux: the ICMP message with the
// IP header already stripped, and the sender as an *net.IPAddr.
func (s *fakeICMPSocket) ReadFrom(b []byte) (int, net.Addr, error) {
	s.mu.Lock()
	dl := s.deadline
	s.mu.Unlock()

	var timeout <-chan time.Time
	if !dl.IsZero() {
		t := time.NewTimer(time.Until(dl))
		defer t.Stop()
		timeout = t.C
	}
	select {
	case pkt := <-s.in:
		return copy(b, pkt.data), &net.IPAddr{IP: pkt.src}, nil
	case <-s.closed:
		return 0, nil, net.ErrClosed
	case <-timeout:
		return 0, nil, timeoutError{}
	}
}

func (s *fakeICMPSocket) WriteTo(b []byte, dst net.Addr) (int, error) {
	ip := addrIP(dst)
	if ip == nil {
		return 0, fmt.Errorf("no address in %v", dst)
	}
	pkt := icmpPkt{data: append([]byte(nil), b...), src: s.ip}
	s.wire.mu.Lock()
	socks := append([]*fakeICMPSocket(nil), s.wire.hosts[ip.String()]...)
	s.wire.mu.Unlock()
	for _, peer := range socks {
		select {
		case peer.in <- pkt:
		default: // a full queue is a dropped packet, as on a real link
		}
	}
	return len(b), nil
}

func (s *fakeICMPSocket) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

func (s *fakeICMPSocket) LocalAddr() net.Addr { return &net.IPAddr{IP: s.ip} }

func (s *fakeICMPSocket) SetDeadline(t time.Time) error {
	s.mu.Lock()
	s.deadline = t
	s.mu.Unlock()
	return nil
}
func (s *fakeICMPSocket) SetReadDeadline(t time.Time) error  { return s.SetDeadline(t) }
func (s *fakeICMPSocket) SetWriteDeadline(t time.Time) error { return nil }

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// A tunnel is a control channel plus a pool of data connections, and each is
// its own KCP session. All of them have to work at once, over one server
// socket, from one client address, with no ports anywhere.
func TestXdiCarriesSeveralSessionsAtOnce(t *testing.T) {
	const (
		token   = "a-real-looking-tunnel-token-0123456789"
		clientN = 4
	)
	serverIP, clientIP := net.IPv4(10, 0, 0, 2), net.IPv4(10, 0, 0, 1)

	wire := newICMPWire()
	block, err := kcpCrypt(token)
	if err != nil {
		t.Fatal(err)
	}

	srvConn := newICMPServerConnWith(wire.socket(serverIP), token)
	listener, err := kcp.ServeConn(block, 10, 3, srvConn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { listener.Close(); srvConn.Close() }()

	// The server echoes each session's bytes back with its own label, so a
	// reply proves the whole path — this session's packets reached this
	// session, and its answers came back to the socket that asked.
	go func() {
		for {
			s, err := listener.AcceptKCP()
			if err != nil {
				return
			}
			go func(s *kcp.UDPSession) {
				buf := make([]byte, 256)
				for {
					_ = s.SetReadDeadline(time.Now().Add(10 * time.Second))
					n, err := s.Read(buf)
					if err != nil {
						return
					}
					if _, err := s.Write(append([]byte("echo:"), buf[:n]...)); err != nil {
						return
					}
				}
			}(s)
		}
	}()

	type client struct {
		name    string
		session *kcp.UDPSession
	}
	clients := make([]client, 0, clientN)
	for i := 0; i < clientN; i++ {
		conn := newICMPClientConnWith(wire.socket(clientIP), token)
		session, err := ownedKCPSession(&net.IPAddr{IP: serverIP}, block, 10, 3, conn)
		if err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
		defer session.Close()
		ApplyKCPSettings(session, KCPSettings{
			MTU: 1350, Interval: 20, Resend: 2, NoDelay: 1, NoCongestion: 1,
			SndWnd: 128, RcvWnd: 128, AckNoDelay: true,
			DataShards: 10, ParityShards: 3, UseICMP: true,
		})
		clients = append(clients, client{name: fmt.Sprintf("session-%d", i), session: session})
	}

	// Every session opens, in the order a tunnel opens them: the control
	// channel first, then the pool. Under the fault, each of these killed the
	// one before it and only the last could ever answer.
	for _, c := range clients {
		if _, err := c.session.Write([]byte(c.name)); err != nil {
			t.Fatalf("%s: write: %v", c.name, err)
		}
		buf := make([]byte, 256)
		_ = c.session.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := c.session.Read(buf)
		if err != nil {
			t.Fatalf("%s got no answer — the server has no session for it: %v", c.name, err)
		}
		if got, want := string(buf[:n]), "echo:"+c.name; got != want {
			t.Fatalf("%s read %q, want %q — its packets reached another session", c.name, got, want)
		}
	}

	// And they are all still alive afterwards, which is the half the fault
	// actually broke: opening was never the problem, staying open was.
	for _, c := range clients {
		msg := c.name + "-again"
		if _, err := c.session.Write([]byte(msg)); err != nil {
			t.Fatalf("%s: second write: %v", c.name, err)
		}
		buf := make([]byte, 256)
		_ = c.session.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := c.session.Read(buf)
		if err != nil {
			t.Fatalf("%s died once the later sessions opened: %v", c.name, err)
		}
		if got, want := string(buf[:n]), "echo:"+msg; got != want {
			t.Fatalf("%s read %q, want %q", c.name, got, want)
		}
	}
}

// A client socket must ignore the packets of the tunnel's other sessions. They
// all arrive at it — one host, raw sockets, no ports — and they all decrypt,
// because every session of a tunnel shares the token's key. What keeps them
// apart is the echo identifier, and if it did not, each session's FEC decoder
// would be fed the shards of every other.
func TestAnXdiClientReadsOnlyItsOwnSession(t *testing.T) {
	const token = "another-tunnel-token"
	serverIP, clientIP := net.IPv4(10, 0, 0, 2), net.IPv4(10, 0, 0, 1)

	wire := newICMPWire()
	srv := newICMPServerConnWith(wire.socket(serverIP), token)
	defer srv.Close()

	a := newICMPClientConnWith(wire.socket(clientIP), token)
	defer a.Close()
	b := newICMPClientConnWith(wire.socket(clientIP), token)
	defer b.Close()

	// Each client says hello, so the server learns both addresses.
	if _, err := a.WriteTo([]byte("from-a"), &net.IPAddr{IP: serverIP}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteTo([]byte("from-b"), &net.IPAddr{IP: serverIP}); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 512)
	peers := map[string]net.Addr{}
	for i := 0; i < 2; i++ {
		_ = srv.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, peer, err := srv.ReadFrom(buf)
		if err != nil {
			t.Fatalf("the server did not receive both hellos: %v", err)
		}
		peers[string(buf[:n])] = peer
	}
	if len(peers) != 2 {
		t.Fatalf("the server saw %d senders, want 2", len(peers))
	}
	// The two must be distinguishable, which is the whole point: kcp-go's
	// listener keys its sessions on exactly this string.
	if peers["from-a"].String() == peers["from-b"].String() {
		t.Fatalf("both sessions reported as %q — the listener would collapse them into one",
			peers["from-a"])
	}

	// Answer only a. b must not see it.
	if _, err := srv.WriteTo([]byte("for-a"), peers["from-a"]); err != nil {
		t.Fatal(err)
	}
	_ = a.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := a.ReadFrom(buf)
	if err != nil {
		t.Fatalf("the answer never reached the session it was addressed to: %v", err)
	}
	if got := string(buf[:n]); got != "for-a" {
		t.Fatalf("a read %q, want %q", got, "for-a")
	}

	_ = b.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, _, err := b.ReadFrom(buf); err == nil {
		t.Fatalf("b accepted a packet meant for a (%q) — every session would decode "+
			"every other's, and the FEC decoders would be fed each other's shards", buf[:n])
	}
}

// The address the server reports for one peer must not move.
//
// The layer-3 tunnel uses this carrier too, and it follows a peer that changes
// address — it re-keys its send path whenever the address a packet arrived from
// differs from the one it had (see l3's notePeer). An address that varied
// packet to packet would look exactly like a peer roaming, on every packet, for
// the life of the tunnel.
func TestTheXdiPeerAddressIsStableForOneSession(t *testing.T) {
	const token = "an-l3-tunnel-token"
	serverIP, clientIP := net.IPv4(10, 0, 0, 2), net.IPv4(10, 0, 0, 1)

	wire := newICMPWire()
	srv := newICMPServerConnWith(wire.socket(serverIP), token)
	defer srv.Close()
	cli := newICMPClientConnWith(wire.socket(clientIP), token)
	defer cli.Close()

	buf := make([]byte, 512)
	var first string
	for i := 0; i < 20; i++ {
		if _, err := cli.WriteTo([]byte(fmt.Sprintf("packet-%d", i)), &net.IPAddr{IP: serverIP}); err != nil {
			t.Fatal(err)
		}
		_ = srv.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, peer, err := srv.ReadFrom(buf)
		if err != nil {
			t.Fatalf("packet %d never arrived: %v", i, err)
		}
		if i == 0 {
			first = peer.String()
			continue
		}
		if got := peer.String(); got != first {
			t.Fatalf("packet %d reported the peer as %q, packet 0 as %q — "+
				"a layer-3 tunnel would re-key on every packet", i, got, first)
		}
	}

	// And the peer has to carry the sender's real IP, because that is what the
	// reply is routed to.
	if ip := addrIP(&net.UDPAddr{IP: clientIP, Port: 1}); !ip.Equal(clientIP) {
		t.Fatalf("the reported address lost the peer's IP: %v", ip)
	}
	if !strings.HasPrefix(first, clientIP.String()+":") {
		t.Fatalf("the peer was reported as %q, which does not name %s", first, clientIP)
	}
}

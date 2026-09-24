package l3

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/backpack/backpack/internal/utils/network"
)

// The QUIC carrier.
//
// The other carriers make the traffic look like something else. This one is
// something else: a real QUIC connection, with a real TLS 1.3 handshake and a
// real HTTP/3 ALPN, carrying the tunnel's datagrams in QUIC DATAGRAM frames
// (RFC 9221). To anything watching the path it is a browser talking to a web
// server over UDP — the single most common thing on a modern network, and the
// traffic a filter can least afford to drop wholesale.
//
// Why datagrams and not a stream. A layer-3 tunnel carries IP packets, which
// belong to flows that already handle their own loss. Putting them on a
// stream — anything that retransmits — makes throughput collapse rather than
// degrade under loss, which is the reason tcp, ws and kcp are not carriers and
// never will be. QUIC's DATAGRAM frames are unreliable and unordered, exactly
// like UDP, so this is a carrier and not a transport.
//
// What the TLS is for. It is for the shape, not for the secrecy: the tunnel's
// own payload is already sealed by Noise before it reaches any carrier, and
// this end authenticates its peer with the token, not with a certificate. So
// the listener presents a certificate it generates for itself at startup and
// the dialler does not verify it. That is not a weakened TLS — it is TLS used
// as camouflage over a channel that is already authenticated end to end, and
// saying so plainly here is better than a reader assuming otherwise.

// quicALPN is what the handshake advertises. "h3" is HTTP/3: the protocol this
// is trying to be indistinguishable from.
const quicALPN = "h3"

// quicOverhead is what a datagram costs on the wire, over and above the payload.
//
//	20  IPv4 header
//	 8  UDP header
//	 1  QUIC short-header flags
//	 8  destination connection id (quic-go's default is shorter; this is the
//	    ceiling, and an overestimate only costs MTU while an underestimate
//	    silently fragments)
//	 4  packet number
//	 3  DATAGRAM frame type and length
//	16  AEAD tag
const quicOverhead = 20 + 8 + 1 + 8 + 4 + 3 + 16

// quicHandshakeTimeout bounds the wait for a connection in either direction. A
// path that takes longer than this is a path the tunnel should be retrying on,
// not blocking in.
const quicHandshakeTimeout = 12 * time.Second

// openQuic builds the QUIC carrier for either side.
func openQuic(cfg Config) (DatagramCarrier, net.Addr, error) {
	if cfg.Mode == ModeListen {
		return listenQuic(cfg)
	}
	return dialQuic(cfg)
}

// How quickly a dead QUIC connection is given up on.
//
// The listener used to hold its one connection for a 60-second idle timeout
// with a 15-second keepalive, and the dialler the same. A peer that crashed
// therefore cost a minute before either end even noticed, and the tunnel's own
// liveness check (peerSilentAfter) could not help: it handshakes again, but over
// the same dead connection. A PING every five seconds is a few dozen bytes a
// minute, and twenty seconds of silence on a path that is being pinged that
// often is a peer that is gone.
const (
	quicKeepAlive   = 5 * time.Second
	quicIdleTimeout = 20 * time.Second
)

func quicConfig() *quic.Config {
	return &quic.Config{
		// Required: the carrier is DATAGRAM frames or nothing.
		EnableDatagrams: true,
		MaxIdleTimeout:  quicIdleTimeout,
		KeepAlivePeriod: quicKeepAlive,
		// Start small enough to cross a 1280-byte path; discovery grows it.
		InitialPacketSize: network.QUICInitialPacketSize,
	}
}

// quicResetKey is the listener's stateless-reset key, derived from the token so
// that it is the same across restarts.
//
// A listener that restarts has lost every connection it had, and the dialler's
// next data packet arrives for a connection nothing knows. With a key that
// survives the restart, the new listener answers it with a stateless reset,
// which the dialler takes as the end of the connection at once and redials.
// With a random key, or none, it answers nothing and the dialler waits out its
// idle timeout. (quic-go resets only packets over 42 bytes, against
// amplification, so a dialler with nothing to send still waits the idle
// timeout: twenty seconds.)
//
// Derived rather than stored: the token is already the shared secret both ends
// hold, and a key on disk would be one more file to protect. Anybody who holds
// the token can reset a connection, which is far less than the token already
// lets them do.
func quicResetKey(token string) *quic.StatelessResetKey {
	sum := sha256.Sum256([]byte("backpack l3 quic stateless reset v1\x00" + token))
	k := quic.StatelessResetKey(sum)
	return &k
}

func dialQuic(cfg Config) (DatagramCarrier, net.Addr, error) {
	ctx, cancel := context.WithTimeout(context.Background(), quicHandshakeTimeout)
	defer cancel()

	conn, err := quic.DialAddr(ctx, cfg.Addr, &tls.Config{
		// See the note above: the certificate proves nothing here and is not
		// meant to. The token in the Noise handshake is what authenticates.
		InsecureSkipVerify: true,
		NextProtos:         []string{quicALPN},
		MinVersion:         tls.VersionTLS13,
	}, quicConfig())
	if err != nil {
		return nil, nil, fmt.Errorf("l3: quic: dialling %s: %w", cfg.Addr, err)
	}
	if err := datagramsAgreed(conn); err != nil {
		conn.CloseWithError(0, "no datagrams")
		return nil, nil, err
	}
	c := newQuicCarrier()
	c.conn = conn
	return c, conn.RemoteAddr(), nil
}

func listenQuic(cfg Config) (DatagramCarrier, net.Addr, error) {
	tlsCfg, err := quicSelfSigned()
	if err != nil {
		return nil, nil, err
	}
	addr, err := net.ResolveUDPAddr("udp", cfg.Addr)
	if err != nil {
		return nil, nil, fmt.Errorf("l3: quic: listening on %s: %w", cfg.Addr, err)
	}
	udp, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("l3: quic: listening on %s: %w", cfg.Addr, err)
	}
	tr := &quic.Transport{Conn: udp, StatelessResetKey: quicResetKey(cfg.Token)}
	ln, err := tr.Listen(tlsCfg, quicConfig())
	if err != nil {
		udp.Close()
		return nil, nil, fmt.Errorf("l3: quic: listening on %s: %w", cfg.Addr, err)
	}
	c := newQuicCarrier()
	c.ln, c.tr, c.udp = ln, tr, udp
	c.peers = map[string]*quicPeer{}
	c.in = make(chan quicDatagram, quicInbox)
	go c.acceptLoop()
	return c, nil, nil
}

// datagramsAgreed refuses a peer that will not carry datagrams.
//
// Without this the tunnel would come up, send its first sealed packet, and get
// an error per packet forever — the failure mode this package exists to avoid.
func datagramsAgreed(conn *quic.Conn) error {
	if sd := conn.ConnectionState().SupportsDatagrams; !sd.Local || !sd.Remote {
		return errors.New("l3: quic: the peer did not agree to carry datagrams")
	}
	return nil
}

// errNoDialler is a write on the listening side to an address no connection
// has.
var errNoDialler = errors.New("l3: quic: no connection from that address")

// quicCarrier presents QUIC connections as a net.PacketConn.
//
// The dialling side has one connection and reads it directly. The listening
// side behaves like an unconnected UDP socket: it keeps every connection it
// accepts, reads datagrams from all of them tagged with the address each came
// from, and writes to whichever connection the address names. Which of them is
// the tunnel's peer is not decided here — it is decided where it already was,
// in the tunnel, which moves its peer only on a packet that authenticated.
//
// It accepted exactly one connection, lazily, and kept it for the life of the
// process, so a dialler that crashed and came back was never accepted again
// (measured: no recovery inside 200 seconds). The first fix took the newest
// connection instead, which let anyone who could reach the port — no token
// needed — take the slot from the real dialler and close its connection, over
// and over. Keeping them all, and letting authentication pick, is both.
type quicCarrier struct {
	ln  *quic.Listener
	tr  *quic.Transport
	udp *net.UDPConn

	// conn is the dialling side's one connection.
	conn *quic.Conn

	// The listening side's connections, by remote address, and the datagrams
	// read from all of them.
	mu     sync.Mutex
	peers  map[string]*quicPeer
	in     chan quicDatagram
	closed bool
	done   chan struct{}

	deadlineMu sync.Mutex
	readAt     time.Time
}

// quicPeer is one accepted connection.
type quicPeer struct {
	conn *quic.Conn
	// wrote is when this end last sent on it, as unix nanoseconds: the
	// tunnel only writes to a peer that authenticated (and to a handshake it
	// is answering), so the connection written to most recently is the one
	// the tunnel trusts, and the last to be evicted.
	wrote atomic.Int64
}

type quicDatagram struct {
	data []byte
	from net.Addr
}

// quicMaxPeers bounds how many connections the listener holds. A stranger can
// open connections without the token; past this the least recently written-to
// goes, which is never the one the tunnel is talking to.
const quicMaxPeers = 16

// quicInbox is how many read datagrams may wait for the tunnel.
const quicInbox = 512

func newQuicCarrier() *quicCarrier {
	return &quicCarrier{done: make(chan struct{})}
}

func (c *quicCarrier) CarrierName() string { return CarrierQuic }
func (c *quicCarrier) Overhead() int       { return quicOverhead }

// acceptLoop takes every connection the listener is offered.
func (c *quicCarrier) acceptLoop() {
	for {
		conn, err := c.ln.Accept(context.Background())
		if err != nil {
			return
		}
		if err := datagramsAgreed(conn); err != nil {
			conn.CloseWithError(0, "no datagrams")
			continue
		}
		if !c.addPeer(conn) {
			conn.CloseWithError(0, "")
			return
		}
		go c.readPeer(conn)
	}
}

// addPeer registers conn, evicting the least recently written-to connection
// when the listener is full. A second connection from the same address
// replaces the first.
func (c *quicCarrier) addPeer(conn *quic.Conn) bool {
	key := conn.RemoteAddr().String()
	p := &quicPeer{conn: conn}
	p.wrote.Store(time.Now().UnixNano())

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	var evict []*quic.Conn
	if old, ok := c.peers[key]; ok {
		evict = append(evict, old.conn)
	} else if len(c.peers) >= quicMaxPeers {
		var oldestKey string
		var oldest int64
		for k, q := range c.peers {
			if w := q.wrote.Load(); oldestKey == "" || w < oldest {
				oldestKey, oldest = k, w
			}
		}
		evict = append(evict, c.peers[oldestKey].conn)
		delete(c.peers, oldestKey)
	}
	c.peers[key] = p
	c.mu.Unlock()

	for _, e := range evict {
		e.CloseWithError(0, "replaced")
	}
	return true
}

// readPeer feeds one connection's datagrams to ReadFrom until it ends.
func (c *quicCarrier) readPeer(conn *quic.Conn) {
	from := conn.RemoteAddr()
	for {
		msg, err := conn.ReceiveDatagram(context.Background())
		if err != nil {
			c.mu.Lock()
			if p, ok := c.peers[from.String()]; ok && p.conn == conn {
				delete(c.peers, from.String())
			}
			c.mu.Unlock()
			return
		}
		select {
		case c.in <- quicDatagram{data: msg, from: from}:
		case <-c.done:
			return
		}
	}
}

func (c *quicCarrier) ReadFrom(p []byte) (int, net.Addr, error) {
	c.deadlineMu.Lock()
	at := c.readAt
	c.deadlineMu.Unlock()

	if c.ln == nil {
		// Dialling: the one connection, read directly. A context only when a
		// deadline asks for one, so the ordinary read allocates nothing.
		ctx, cancel := context.Background(), context.CancelFunc(func() {})
		if !at.IsZero() {
			ctx, cancel = context.WithDeadline(ctx, at)
		}
		msg, err := c.conn.ReceiveDatagram(ctx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return 0, nil, os.ErrDeadlineExceeded
			}
			return 0, nil, err
		}
		return copy(p, msg), c.conn.RemoteAddr(), nil
	}

	var deadline <-chan time.Time
	if !at.IsZero() {
		t := time.NewTimer(time.Until(at))
		defer t.Stop()
		deadline = t.C
	}
	select {
	case d := <-c.in:
		return copy(p, d.data), d.from, nil
	case <-deadline:
		return 0, nil, os.ErrDeadlineExceeded
	case <-c.done:
		return 0, nil, net.ErrClosed
	}
}

// WriteTo sends to the connection addr names. The dialling side has only one,
// and ignores addr.
func (c *quicCarrier) WriteTo(p []byte, addr net.Addr) (int, error) {
	conn := c.conn
	if c.ln != nil {
		if addr == nil {
			return 0, errNoDialler
		}
		c.mu.Lock()
		peer, ok := c.peers[addr.String()]
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return 0, net.ErrClosed
		}
		if !ok {
			return 0, errNoDialler
		}
		peer.wrote.Store(time.Now().UnixNano())
		conn = peer.conn
	}
	// Errors go back as they are, including a datagram larger than the
	// connection can carry right now; the tunnel counts it dropped and a
	// failed write never ends a generation.
	if err := conn.SendDatagram(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *quicCarrier) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.done)
	var conns []*quic.Conn
	for _, p := range c.peers {
		conns = append(conns, p.conn)
	}
	c.peers = nil
	c.mu.Unlock()

	if c.conn != nil {
		_ = c.conn.CloseWithError(0, "")
	}
	for _, conn := range conns {
		_ = conn.CloseWithError(0, "")
	}
	if c.ln != nil {
		c.ln.Close()
		c.tr.Close()
		return c.udp.Close()
	}
	return nil
}

func (c *quicCarrier) LocalAddr() net.Addr {
	if c.ln != nil {
		return c.ln.Addr()
	}
	return c.conn.LocalAddr()
}

// SetDeadline and SetReadDeadline bound the next ReceiveDatagram.
func (c *quicCarrier) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}

func (c *quicCarrier) SetReadDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	c.readAt = t
	c.deadlineMu.Unlock()
	return nil
}

// SetWriteDeadline is accepted and ignored: SendDatagram does not block, so
// there is nothing for a deadline to bound.
func (c *quicCarrier) SetWriteDeadline(time.Time) error { return nil }

// quicSelfSigned makes the certificate the listener presents. It is generated
// per process and never stored: nothing verifies it, and a certificate on disk
// would only be one more thing to explain.
func quicSelfSigned() (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("l3: quic: certificate: %w", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("l3: quic: certificate: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		NextProtos:   []string{quicALPN},
		MinVersion:   tls.VersionTLS13,
	}, nil
}

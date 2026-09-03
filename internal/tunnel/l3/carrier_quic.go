package l3

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
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

func quicConfig() *quic.Config {
	return &quic.Config{
		EnableDatagrams: true,
		// Long enough that an idle tunnel is not torn down between keepalives,
		// short enough that a dead path is noticed and rebuilt.
		MaxIdleTimeout:  60 * time.Second,
		KeepAlivePeriod: 15 * time.Second,
	}
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
	return &quicCarrier{conn: conn, peer: conn.RemoteAddr()}, conn.RemoteAddr(), nil
}

func listenQuic(cfg Config) (DatagramCarrier, net.Addr, error) {
	tlsCfg, err := quicSelfSigned()
	if err != nil {
		return nil, nil, err
	}
	ln, err := quic.ListenAddr(cfg.Addr, tlsCfg, quicConfig())
	if err != nil {
		return nil, nil, fmt.Errorf("l3: quic: listening on %s: %w", cfg.Addr, err)
	}
	// The peer is not known until it arrives, so the carrier accepts on first
	// use. Returning a nil address is how every listening carrier says "learn
	// it from the packets", and the tunnel already understands that.
	return &quicCarrier{ln: ln}, nil, nil
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

// quicCarrier presents one QUIC connection as a net.PacketConn.
//
// A tunnel is point to point, so there is exactly one peer and one connection.
// The listening side accepts it lazily, on the first read, because that is when
// the tunnel starts waiting for something to arrive.
type quicCarrier struct {
	ln   *quic.Listener
	conn *quic.Conn
	peer net.Addr

	mu       sync.Mutex
	accepted bool

	deadlineMu sync.Mutex
	readAt     time.Time
}

func (c *quicCarrier) CarrierName() string { return CarrierQuic }
func (c *quicCarrier) Overhead() int       { return quicOverhead }

// session returns the connection, accepting one first on the listening side.
func (c *quicCarrier) session() (*quic.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn, nil
	}
	if c.ln == nil {
		return nil, net.ErrClosed
	}
	conn, err := c.ln.Accept(context.Background())
	if err != nil {
		return nil, err
	}
	if err := datagramsAgreed(conn); err != nil {
		conn.CloseWithError(0, "no datagrams")
		return nil, err
	}
	c.conn, c.peer, c.accepted = conn, conn.RemoteAddr(), true
	return conn, nil
}

func (c *quicCarrier) ReadFrom(p []byte) (int, net.Addr, error) {
	conn, err := c.session()
	if err != nil {
		return 0, nil, err
	}
	ctx := context.Background()
	c.deadlineMu.Lock()
	at := c.readAt
	c.deadlineMu.Unlock()
	if !at.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, at)
		defer cancel()
	}
	msg, err := conn.ReceiveDatagram(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, nil, os.ErrDeadlineExceeded
		}
		return 0, nil, err
	}
	n := copy(p, msg)
	return n, conn.RemoteAddr(), nil
}

// WriteTo ignores the address: a QUIC connection has exactly one peer, and the
// tunnel only ever writes back to the one it read from.
func (c *quicCarrier) WriteTo(p []byte, _ net.Addr) (int, error) {
	conn, err := c.session()
	if err != nil {
		return 0, err
	}
	if err := conn.SendDatagram(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *quicCarrier) Close() error {
	c.mu.Lock()
	conn, ln := c.conn, c.ln
	c.conn, c.ln = nil, nil
	c.mu.Unlock()
	if conn != nil {
		_ = conn.CloseWithError(0, "")
	}
	if ln != nil {
		return ln.Close()
	}
	return nil
}

func (c *quicCarrier) LocalAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ln != nil {
		return c.ln.Addr()
	}
	if c.conn != nil {
		return c.conn.LocalAddr()
	}
	return nil
}

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

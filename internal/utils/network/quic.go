package network

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/quic-go/quic-go"
)

// quicALPN is the application protocol both ends advertise on the TLS handshake.
// It has to match, or the handshake fails before a single tunnel byte flows —
// which doubles as a cheap first filter against anything that is not us.
const quicALPN = "backpack-quic"

// QUICInitialPacketSize is the UDP payload size QUIC starts from, before Path
// MTU Discovery has measured the route.
//
// quic-go's own default is 1280 bytes of payload, which is 1308 bytes on the
// wire over IPv4 and 1328 over IPv6. Any path whose MTU is 1280 — the standard
// for an IPv6 tunnel, for most mobile carriers, and for anything already riding
// inside another tunnel — drops every one of those packets, and because the
// Initial packet is the first thing sent, the handshake never completes and the
// transport reports nothing at all rather than a slow link. A netns run at MTU
// 1280 reproduced it: nine transports carried 2 MB byte-identical and quic
// carried zero bytes.
//
// 1232 is 1280 minus a 40-byte IPv6 header and an 8-byte UDP header, so it fits
// a 1280-byte path in either address family. Starting low costs nothing on a
// fat path: Path MTU Discovery is left enabled and climbs to the real figure
// within the first few round trips, so only the handshake itself is smaller.
const QUICInitialPacketSize = 1232

// QUICSettings carries the tuning of a QUIC endpoint from the config down to the
// socket. QUIC brings its own TLS 1.3, congestion control and loss recovery, so
// unlike KCP there is nothing to hand-tune for the link itself — only the idle
// timeout, the keepalive that holds a NAT mapping open, and the datagram socket
// buffers.
type QUICSettings struct {
	// KeepAlivePeriod sends a PING often enough to keep a NAT/firewall mapping
	// alive on an otherwise idle tunnel. Zero disables it.
	KeepAlivePeriod time.Duration
	// MaxIdleTimeout tears the connection down after this long with no packets.
	// It is negotiated to the lower of the two ends' values.
	MaxIdleTimeout time.Duration
	// SO_RCVBUF/SO_SNDBUF size the underlying UDP socket. The kernel default is a
	// few hundred KB, which a flood overruns in a blink; the preset's several MB
	// is what keeps QUIC fed under load. See the UDP transport for the same fix.
	SO_RCVBUF int
	SO_SNDBUF int
}

func (s QUICSettings) quicConfig() *quic.Config {
	return &quic.Config{
		KeepAlivePeriod: s.KeepAlivePeriod,
		MaxIdleTimeout:  s.MaxIdleTimeout,
		// The pool pre-opens streams and every forwarded flow is one more, so the
		// ceiling has to be generous or the client blocks opening the next one.
		MaxIncomingStreams: 1 << 16,
		// Allow the datagram socket to be reused for a graceful restart.
		Allow0RTT: false,
		// Start small enough to cross a 1280-byte path; discovery grows it.
		InitialPacketSize: QUICInitialPacketSize,
	}
}

// quicServerTLS builds the server's TLS config: an ephemeral self-signed
// certificate and the fixed ALPN. The certificate is never verified by the
// client — the tunnel token is the shared secret, exactly as on KCP and UDP —
// so a fresh throwaway cert each start is all it needs to satisfy TLS 1.3.
func quicServerTLS() (*tls.Config, error) {
	cert, err := selfSignedCert()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{quicALPN},
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// quicClientTLS builds the client's TLS config. It does not verify the server
// certificate: QUIC forces TLS, but the trust here is the token carried over the
// first stream, not a PKI. InsecureSkipVerify only skips the certificate check,
// not the encryption — every byte is still TLS 1.3 encrypted.
func quicClientTLS() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{quicALPN},
		MinVersion:         tls.VersionTLS13,
	}
}

// selfSignedCert generates a throwaway ECDSA certificate for the QUIC listener.
func selfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("quic: generate key: %w", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: quicALPN},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("quic: create certificate: %w", err)
	}

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// bindUDP opens a UDP socket and sizes its buffers. QUIC is handed this socket
// and rides in the datagrams it carries.
func bindUDP(addr string, s QUICSettings) (*net.UDPConn, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("quic: resolve %s: %w", addr, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("quic: listen udp %s: %w", addr, err)
	}
	if s.SO_RCVBUF > 0 {
		_ = conn.SetReadBuffer(s.SO_RCVBUF)
	}
	if s.SO_SNDBUF > 0 {
		_ = conn.SetWriteBuffer(s.SO_SNDBUF)
	}
	return conn, nil
}

// QUICListener bundles a quic listener with the UDP socket it rides on. quic-go
// never closes a socket the caller supplied, so without this the socket would
// outlive the listener — and on a restart the transport could not rebind its own
// port. Closing this closes both.
type QUICListener struct {
	*quic.Listener
	conn *net.UDPConn
}

// Close shuts the listener down and releases the UDP socket underneath it.
func (l *QUICListener) Close() error {
	err := l.Listener.Close()
	_ = l.conn.Close()
	return err
}

// QUICListen opens a QUIC listener on bindAddr. The returned listener yields
// connections whose streams carry the tunnel's traffic, each one TLS 1.3
// encrypted.
func QUICListen(bindAddr string, s QUICSettings) (*QUICListener, error) {
	tlsConf, err := quicServerTLS()
	if err != nil {
		return nil, err
	}
	udpConn, err := bindUDP(bindAddr, s)
	if err != nil {
		return nil, err
	}
	listener, err := quic.Listen(udpConn, tlsConf, s.quicConfig())
	if err != nil {
		udpConn.Close()
		return nil, fmt.Errorf("quic: failed to listen on %s: %w", bindAddr, err)
	}
	return &QUICListener{Listener: listener, conn: udpConn}, nil
}

// QUICDial opens a QUIC connection to remoteAddr with the tuning applied. The
// socket it rides in is bound locally so its buffers can be sized to match.
func QUICDial(ctx context.Context, remoteAddr string, s QUICSettings) (*quic.Conn, error) {
	udpConn, err := bindUDP(":0", s)
	if err != nil {
		return nil, err
	}
	remoteUDPAddr, err := net.ResolveUDPAddr("udp", remoteAddr)
	if err != nil {
		udpConn.Close()
		return nil, fmt.Errorf("quic: resolve %s: %w", remoteAddr, err)
	}
	conn, err := quic.Dial(ctx, udpConn, remoteUDPAddr, quicClientTLS(), s.quicConfig())
	if err != nil {
		udpConn.Close()
		return nil, fmt.Errorf("quic: failed to dial %s: %w", remoteAddr, err)
	}
	// quic-go never closes a socket the caller supplied, so release it when the
	// connection ends — however it ends — or every reconnect would leak one.
	go func() {
		<-conn.Context().Done()
		udpConn.Close()
	}()
	return conn, nil
}

// QUICStreamConn adapts a QUIC stream to net.Conn. A stream carries Read, Write,
// Close and the deadlines already; it only lacks the peer addresses, which come
// from the connection it belongs to. This lets a stream flow through everything
// that expects a net.Conn — the framing helpers, the connection handler, the
// usage counters — with no special-casing.
type QUICStreamConn struct {
	*quic.Stream
	conn *quic.Conn
}

// NewQUICStreamConn wraps a stream and its connection as a net.Conn.
func NewQUICStreamConn(stream *quic.Stream, conn *quic.Conn) net.Conn {
	return &QUICStreamConn{Stream: stream, conn: conn}
}

func (q *QUICStreamConn) LocalAddr() net.Addr  { return q.conn.LocalAddr() }
func (q *QUICStreamConn) RemoteAddr() net.Addr { return q.conn.RemoteAddr() }

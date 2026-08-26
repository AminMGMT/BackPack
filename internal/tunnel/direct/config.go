package direct

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/tunnel/portmap"
)

// Which end this is. The names are geographic because that is what stays
// true: Iran exposes the ports in both directions, and only who dials changes.
const (
	// RoleEdge is the Iran server. It owns the user-facing ports and dials the
	// tunnel out, so it needs no inbound port of its own.
	RoleEdge = "edge"

	// RoleOrigin is the kharej server. It listens for the tunnel and holds the
	// real service.
	RoleOrigin = "origin"
)

// Transports available to a direct tunnel. Both are stream transports carrying
// a mux session; the difference is only what the stream looks like on the
// wire.
const (
	// TransportTCP is a plain TCP connection. Nothing is encrypted by the
	// tunnel itself, so it suits a payload that is already TLS.
	TransportTCP = "tcp"

	// TransportStealth wraps the same connection in the Noise record layer, so
	// the stream has no handshake and no fingerprint for inspection to match.
	TransportStealth = "stealth"

	// TransportWS opens with an HTTP upgrade, so the connection looks like an
	// ordinary web request and a CDN is willing to proxy it.
	TransportWS = "ws"

	// TransportWSS is the same upgrade inside TLS — ordinary HTTPS on the
	// wire, and what a CDN in front of the origin requires.
	TransportWSS = "wss"
)

const (
	defaultDialTimeout  = 10 * time.Second
	defaultRetryDelay   = 3 * time.Second
	defaultBackendDial  = 10 * time.Second
	defaultMaxFrameSize = 32768
	defaultMaxReceive   = 4194304
	defaultMaxStream    = 65536

	// defaultACMECacheDir must survive restarts: re-issuing works, but doing
	// it repeatedly runs into Let's Encrypt's rate limits, and then the tunnel
	// has no certificate at all until the limit resets. It is the same
	// directory the reverse transports use, so a host running both does not
	// hold two accounts.
	defaultACMECacheDir = "/etc/backpack/acme"
)

// Config is one resolved direct tunnel.
type Config struct {
	// Role is RoleEdge or RoleOrigin.
	Role string

	// Addr is the origin's host:port when this is the edge, or the address to
	// bind when this is the origin.
	Addr string

	// Token is the shared secret both ends hold. It never travels on the wire;
	// see handshake.go.
	Token string

	// Transport is one of the four above.
	Transport string

	// ServerName is the name the edge presents in SNI and the Host header on
	// the websocket transports — the domain in front of a CDN, or whatever
	// name should reach an inspecting middlebox. Empty uses the host of Addr
	// and, on wss, skips certificate verification: the tunnel authenticates on
	// the token rather than the certificate, so a self-signed origin is the
	// expected case. Setting it turns verification on.
	ServerName string

	// TLSCertFile and TLSKeyFile are the origin's certificate for wss. Both
	// empty generates a self-signed one, which is what a direct connection to
	// an IP address wants.
	TLSCertFile string
	TLSKeyFile  string

	// ACMEDomain switches the origin to a Let's Encrypt certificate for that
	// domain instead of a generated one. It must resolve to the origin.
	ACMEDomain   string
	ACMEEmail    string
	ACMECacheDir string

	// Ports are the forwarded port mappings, served on the edge. They use the
	// same syntax the reverse tunnel does.
	Ports []string

	// AcceptUDP forwards UDP as well as TCP on those ports.
	AcceptUDP bool

	// MaxConnections caps simultaneous forwarded connections (0 = unlimited).
	// Enforced on the Iran side, where the connections arrive.
	MaxConnections int

	// BandwidthMbps caps total tunnel throughput in Mbit/s (0 = unlimited).
	BandwidthMbps int

	// Sessions is how many mux sessions the edge keeps open, with new streams
	// spread across them. One is enough for most traffic; more helps when a
	// single connection is being shaped, or when head-of-line blocking in the
	// transport underneath starts to show.
	Sessions int

	// DialTimeout bounds a dial to the origin.
	DialTimeout time.Duration

	// RetryDelay is how long the edge waits before redialling a session that
	// dropped.
	RetryDelay time.Duration

	// MaxFrameSize, MaxReceiveBuffer and MaxStreamBuffer tune the mux session.
	// Zero takes the same defaults the reverse transports use.
	MaxFrameSize     int
	MaxReceiveBuffer int
	MaxStreamBuffer  int

	// MuxVersion pins the smux version. Zero settles it automatically.
	MuxVersion int

	// Nodelay disables Nagle on the tunnel connection, which is what an
	// interactive workload wants and a bulk one does not care about.
	Nodelay bool

	// Keepalive is the TCP keepalive period on the tunnel connection.
	Keepalive time.Duration

	// MSS clamps the largest TCP payload this end sends on the tunnel
	// connection. Zero leaves it to the kernel. See config.DirectConfig.MSS
	// for what it is for.
	MSS int
}

// Validate fills in what was left out and refuses what cannot work.
func (c *Config) Validate() error {
	c.Role = strings.ToLower(strings.TrimSpace(c.Role))
	switch c.Role {
	case RoleEdge, RoleOrigin:
	case "":
		return fmt.Errorf("direct: role is required (%q on the Iran server, %q on the kharej server)",
			RoleEdge, RoleOrigin)
	default:
		return fmt.Errorf("direct: unknown role %q (want %q or %q)", c.Role, RoleEdge, RoleOrigin)
	}

	if strings.TrimSpace(c.Addr) == "" {
		if c.Role == RoleEdge {
			return fmt.Errorf("direct: addr is required and must be the kharej server's host:port")
		}
		return fmt.Errorf("direct: addr is required and must be the address to bind")
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return fmt.Errorf("direct: addr %q must be host:port: %w", c.Addr, err)
	}

	if strings.TrimSpace(c.Token) == "" {
		return fmt.Errorf("direct: token is required and must match the other end")
	}

	c.Transport = strings.ToLower(strings.TrimSpace(c.Transport))
	switch c.Transport {
	case "":
		c.Transport = TransportTCP
	case TransportTCP, TransportStealth, TransportWS, TransportWSS:
	default:
		return fmt.Errorf("direct: transport %q is not available (have %q, %q, %q, %q)",
			c.Transport, TransportTCP, TransportStealth, TransportWS, TransportWSS)
	}
	// A half-written certificate pair is a configuration that would come up
	// serving a self-signed certificate while its operator believed otherwise.
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return fmt.Errorf("direct: tls_cert and tls_key must be given together")
	}
	if c.ACMECacheDir == "" {
		c.ACMECacheDir = defaultACMECacheDir
	}

	// The edge is the only side that serves ports; the origin learns each
	// target from the stream that asks for it.
	if c.Role == RoleEdge {
		if len(c.Ports) == 0 {
			return fmt.Errorf("direct: the %s needs at least one forwarded port mapping", RoleEdge)
		}
		if _, err := portmap.Expand(c.Ports, DefaultBackendHost); err != nil {
			return fmt.Errorf("direct: %w", err)
		}
	}

	if c.Sessions <= 0 {
		c.Sessions = 1
	}
	if c.Sessions > 64 {
		return fmt.Errorf("direct: sessions = %d is more than the 64 a tunnel can use", c.Sessions)
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = defaultDialTimeout
	}
	if c.RetryDelay <= 0 {
		c.RetryDelay = defaultRetryDelay
	}
	if c.MaxFrameSize <= 0 {
		c.MaxFrameSize = defaultMaxFrameSize
	}
	if c.MaxReceiveBuffer <= 0 {
		c.MaxReceiveBuffer = defaultMaxReceive
	}
	if c.MaxStreamBuffer <= 0 {
		c.MaxStreamBuffer = defaultMaxStream
	}
	return nil
}

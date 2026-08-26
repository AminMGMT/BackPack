package direct

import (
	"context"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/xtaci/smux"
)

// The tunnel session.
//
// One TCP connection, optionally wrapped in the Noise record layer, carrying a
// smux session. Both ends build it the same way and differ only in who dials.
//
// The mux is what makes the direct direction cheap. A raw connection can carry
// one conversation, so a reverse tunnel needs a pool of them and a channel to
// ask for more; a mux session carries as many as are opened on it, from either
// end. So the edge dials one session and opens a stream per user connection,
// and there is nothing else to coordinate.

// DefaultBackendHost is what a mapping's target means when it names no host of
// its own: the loopback of the machine at the far end, where the real service
// listens. It matches what the reverse tunnel does with the same syntax.
const DefaultBackendHost = "127.0.0.1"

// muxSettings renders the tuning smux is built with.
func (c *Config) muxSettings() *smux.Config {
	return network.SmuxConfig(
		network.ResolveMuxVersion(c.MuxVersion),
		network.MuxSettings{
			MaxFrameSize:     c.MaxFrameSize,
			MaxReceiveBuffer: c.MaxReceiveBuffer,
			MaxStreamBuffer:  c.MaxStreamBuffer,
		},
	)
}

// tuneConn applies the socket options the tunnel connection wants. Both ends
// call it, on their own side of the same connection.
func tuneConn(conn net.Conn, cfg *Config) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	// Nagle batches small writes, which is what a bulk transfer wants and an
	// interactive one does not. The default follows the reverse tunnel's.
	_ = tcp.SetNoDelay(cfg.Nodelay)
	if cfg.Keepalive > 0 {
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(cfg.Keepalive)
	}
	clampMSS(tcp, cfg.MSS)
}

// clampMSS caps what this end puts in one TCP segment.
//
// It is applied to a connected socket rather than in the dialler, because both
// ends need it and only one of them dials: the origin's connections arrive from
// Accept, where there is no dial to configure. Linux takes TCP_MAXSEG on an
// established socket and clamps what is sent from then on, which is exactly the
// half of the problem this end owns — the other end clamps its own.
//
// Best effort by design. A kernel that will not take it leaves the tunnel
// working exactly as it did before, which is the right outcome for a knob that
// is off unless somebody set it.
func clampMSS(tcp *net.TCPConn, mss int) {
	if mss <= 0 {
		return
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_MAXSEG, mss)
	})
}

// Knowing when a session has died.
//
// smux does not say. Its Close() closes an internal channel that IsClosed()
// reports on, but a session whose *socket* fails does not go through Close:
// the read error is recorded separately, and IsClosed() keeps answering false
// for a session that can never carry another byte. Polling it therefore never
// fires, and an edge that trusted it would go on handing user connections to a
// dead session for as long as the process lived.
//
// The transport is the thing that actually died, so that is what is watched.
// This wrapper sits under smux and closes a channel the moment a read or write
// on the underlying connection fails — which is immediately when the peer
// closes, and within smux's own keepalive interval when it vanishes silently.

// watchedConn is a net.Conn that announces its own death.
type watchedConn struct {
	net.Conn
	once sync.Once
	dead chan struct{}
}

func newWatchedConn(conn net.Conn) *watchedConn {
	return &watchedConn{Conn: conn, dead: make(chan struct{})}
}

func (c *watchedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil {
		c.markDead()
	}
	return n, err
}

func (c *watchedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err != nil {
		c.markDead()
	}
	return n, err
}

func (c *watchedConn) Close() error {
	c.markDead()
	return c.Conn.Close()
}

func (c *watchedConn) markDead() { c.once.Do(func() { close(c.dead) }) }

// tunnelSession is a mux session together with the signal that its transport
// has failed.
type tunnelSession struct {
	*smux.Session
	dead <-chan struct{}
}

// Dead reports when the session can no longer carry anything.
func (s *tunnelSession) Dead() <-chan struct{} { return s.dead }

// Usable reports whether a session is worth handing a connection to.
func (s *tunnelSession) Usable() bool {
	select {
	case <-s.dead:
		return false
	default:
		return !s.IsClosed()
	}
}

// dialTransport opens the raw stream the session will run on. Which of the
// four transports it is decides only how the connection opens; everything
// above this point is identical.
func dialTransport(ctx context.Context, cfg *Config) (net.Conn, error) {
	if cfg.Transport == TransportWS || cfg.Transport == TransportWSS {
		return dialWebSocket(ctx, cfg)
	}
	dialer := net.Dialer{Timeout: cfg.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("direct: dialling %s: %w", cfg.Addr, err)
	}
	tuneConn(conn, cfg)
	return conn, nil
}

// dialSession opens one tunnel session from the edge to the origin.
func dialSession(ctx context.Context, cfg *Config) (*tunnelSession, error) {
	conn, err := dialTransport(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Stealth first, so everything above it — the token proof, the mux, every
	// stream — travels inside the encrypted record layer.
	if cfg.Transport == TransportStealth {
		wrapped, err := network.NoiseClientConn(conn, cfg.Token, handshakeTimeout)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("direct: stealth handshake with %s: %w", cfg.Addr, err)
		}
		conn = wrapped
	}

	if err := edgeHandshake(conn, cfg.Token); err != nil {
		conn.Close()
		return nil, err
	}

	// Wrapped only now, so the handshake's own deadlines and errors do not
	// count as the session dying before it has begun.
	watched := newWatchedConn(conn)

	// The edge dials the session, so it is the smux client.
	session, err := smux.Client(watched, cfg.muxSettings())
	if err != nil {
		watched.Close()
		return nil, fmt.Errorf("direct: opening the mux session: %w", err)
	}
	return &tunnelSession{Session: session, dead: watched.dead}, nil
}

// acceptSession takes one accepted connection through everything it must pass
// before it can carry streams.
func acceptSession(conn net.Conn, cfg *Config) (*smux.Session, error) {
	tuneConn(conn, cfg)

	if cfg.Transport == TransportStealth {
		wrapped, err := network.NoiseServerConn(conn, cfg.Token, handshakeTimeout)
		if err != nil {
			return nil, fmt.Errorf("stealth handshake: %w", err)
		}
		conn = wrapped
	}

	if err := originHandshake(conn, cfg.Token); err != nil {
		return nil, err
	}

	session, err := smux.Server(conn, cfg.muxSettings())
	if err != nil {
		return nil, fmt.Errorf("opening the mux session: %w", err)
	}
	return session, nil
}

// openStream opens a stream on a session and asks the origin to join it to a
// backend, returning only once the origin says it has.
//
// Waiting for that answer is what turns a dead backend into a failure the edge
// can act on — refusing the user's connection — rather than a stream that
// opens and closes a moment later for no visible reason.
func openStream(session *tunnelSession, kind byte, target string, timeout time.Duration) (net.Conn, error) {
	stream, err := session.OpenStream()
	if err != nil {
		return nil, err
	}
	_ = stream.SetDeadline(time.Now().Add(timeout))

	if err := writeRequest(stream, kind, target); err != nil {
		stream.Close()
		return nil, err
	}
	var answer [1]byte
	if _, err := stream.Read(answer[:]); err != nil {
		stream.Close()
		return nil, err
	}
	if answer[0] != streamOK {
		stream.Close()
		return nil, fmt.Errorf("direct: the origin could not reach %s", target)
	}

	// Cleared before the stream carries user bytes, which have no deadline.
	_ = stream.SetDeadline(time.Time{})

	// Counted from here, and only here: this is the tunnel side of the
	// transfer, and wrapping the local side as well would count every byte
	// twice. The request and the one-byte answer above are deliberately
	// outside it — they are the tunnel's own overhead, not the user's traffic.
	//
	// Without this the panel showed a working direct tunnel moving nothing at
	// all: the reverse transports count inside their copy loops, and this
	// engine has no copy loop of theirs to inherit it from.
	return metrics.CountedConn(stream), nil
}

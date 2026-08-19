package direct

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/sirupsen/logrus"
	"github.com/xtaci/smux"
)

// The kharej side.
//
// It listens for tunnel sessions, and for each stream opened on one it dials
// the backend that stream asks for and copies bytes. It holds no port
// mappings of its own: every target arrives on the stream that wants it, so
// changing what is forwarded is a change to the edge's config alone.

// OriginStats is what the kharej side reports.
type OriginStats struct {
	Sessions int64
	Streams  uint64
	Failed   uint64
	Active   int64
	Rejected uint64
}

// Origin is the listening half of a direct tunnel.
type Origin struct {
	cfg Config
	log *logrus.Logger

	addrMu sync.RWMutex
	addr   net.Addr

	stats struct {
		sessions atomic.Int64
		streams  atomic.Uint64
		failed   atomic.Uint64
		active   atomic.Int64
		rejected atomic.Uint64
	}
}

// NewOrigin validates a configuration and opens nothing.
func NewOrigin(cfg Config, log *logrus.Logger) (*Origin, error) {
	cfg.Role = RoleOrigin
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = logrus.StandardLogger()
	}
	return &Origin{cfg: cfg, log: log}, nil
}

// LocalAddr is the address the listener bound, or nil before Run has bound it.
func (o *Origin) LocalAddr() net.Addr {
	o.addrMu.RLock()
	defer o.addrMu.RUnlock()
	return o.addr
}

// Stats returns a snapshot.
func (o *Origin) Stats() OriginStats {
	return OriginStats{
		Sessions: o.stats.sessions.Load(),
		Streams:  o.stats.streams.Load(),
		Failed:   o.stats.failed.Load(),
		Active:   o.stats.active.Load(),
		Rejected: o.stats.rejected.Load(),
	}
}

// listen binds whichever transport this origin serves. The websocket
// transports present themselves as an ordinary net.Listener, so everything
// past this point is the same for all four.
func (o *Origin) listen() (net.Listener, error) {
	if o.cfg.Transport == TransportWS || o.cfg.Transport == TransportWSS {
		return listenWebSocket(&o.cfg, o.log)
	}
	return net.Listen("tcp", o.cfg.Addr)
}

// Run serves tunnel sessions until ctx ends.
func (o *Origin) Run(ctx context.Context) error {
	listener, err := o.listen()
	if err != nil {
		return err
	}
	defer listener.Close()
	go func() { <-ctx.Done(); listener.Close() }()

	o.addrMu.Lock()
	o.addr = listener.Addr()
	o.addrMu.Unlock()

	o.log.Infof("direct: origin listening on %s (%s)", listener.Addr(), o.cfg.Transport)

	var sessions sync.WaitGroup
	defer sessions.Wait()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		// Admission happens in the connection's own goroutine. A peer that
		// connects and then says nothing costs one goroutine and never delays
		// the connections behind it — which matters on a public address, where
		// scanners find an open port within hours.
		sessions.Add(1)
		go func() {
			defer sessions.Done()
			o.admit(ctx, conn)
		}()
	}
}

// admit takes one accepted connection through the handshake and, if it passes,
// serves the streams opened on it.
func (o *Origin) admit(ctx context.Context, conn net.Conn) {
	session, err := acceptSession(conn, &o.cfg)
	if err != nil {
		// Nothing is sent back. A peer without the token learns only that
		// something accepted a TCP connection and then closed it.
		o.stats.rejected.Add(1)
		o.log.Debugf("direct: refusing a session from %s: %v", conn.RemoteAddr(), err)
		conn.Close()
		return
	}
	defer session.Close()

	o.stats.sessions.Add(1)
	defer o.stats.sessions.Add(-1)
	o.log.Infof("direct: session established with %s", conn.RemoteAddr())

	// The watcher is bound to this session's lifetime, not the process's.
	//
	// Watching ctx alone left one goroutine per session alive until shutdown,
	// each holding a dead smux session and its buffers. A session lasts as long
	// as the link is up, so on a steady tunnel that is nothing — but a flapping
	// link reconnects every few seconds, and a month of that is a slow leak
	// with no symptom until the process is large.
	sessionCtx, stopWatching := context.WithCancel(ctx)
	defer stopWatching()
	go func() { <-sessionCtx.Done(); session.Close() }()

	var streams sync.WaitGroup
	defer streams.Wait()

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			if ctx.Err() == nil {
				o.log.Infof("direct: session with %s ended: %v", conn.RemoteAddr(), err)
			}
			return
		}
		streams.Add(1)
		go func() {
			defer streams.Done()
			o.serveStream(ctx, stream)
		}()
	}
}

// serveStream joins one stream to the backend it asks for.
func (o *Origin) serveStream(ctx context.Context, stream *smux.Stream) {
	defer stream.Close()

	// The request arrives first and must arrive promptly; after that the
	// stream carries user bytes and has no deadline of its own.
	_ = stream.SetReadDeadline(time.Now().Add(handshakeTimeout))
	kind, target, err := readRequest(stream)
	if err != nil {
		o.stats.failed.Add(1)
		o.log.Debugf("direct: unreadable request on a stream: %v", err)
		return
	}
	_ = stream.SetReadDeadline(time.Time{})

	network := "tcp"
	if kind == kindUDP {
		network = "udp"
	}

	backend, err := dialBackend(ctx, network, target, o.cfg.DialTimeout)
	if err != nil {
		o.stats.failed.Add(1)
		o.log.Warnf("direct: could not reach %s/%s: %v", network, target, err)
		_, _ = stream.Write([]byte{streamFailed})
		return
	}
	defer backend.Close()

	if _, err := stream.Write([]byte{streamOK}); err != nil {
		return
	}

	o.stats.streams.Add(1)
	o.stats.active.Add(1)
	defer o.stats.active.Add(-1)

	// The tunnel side, wrapped so its traffic is recorded — the same rule as on
	// the edge, and for the same reason. The backend is the local side and is
	// deliberately left alone: counting both would double every byte.
	//
	// The direction reads backwards at a glance and is right. From this
	// machine, a byte read off the stream came in over the tunnel, and a byte
	// written to it goes out over the tunnel, which is exactly what
	// CountedConn records.
	counted := metrics.CountedConn(stream)

	if kind == kindUDP {
		relayDatagrams(ctx, counted, backend)
		return
	}
	pipe(ctx, counted, backend)
}

// dialBackend reaches the service a stream named.
func dialBackend(ctx context.Context, network, target string, timeout time.Duration) (net.Conn, error) {
	// The one class of address a forwarded port never legitimately reaches.
	// See target.go for why this is not the usual block-all-private rule.
	if err := vetTarget(target); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = defaultBackendDial
	}
	dialer := net.Dialer{Timeout: timeout}
	return dialer.DialContext(ctx, network, target)
}

// relayDatagrams carries a UDP conversation between a stream and a socket,
// stripping the length prefix on the way out and adding it on the way in.
//
// Unlike a TCP relay this cannot use io.Copy in either direction: every
// datagram has to be moved whole, and a copy that merged two of them into one
// write — or split one across two — would hand the receiver something neither
// end sent.
func relayDatagrams(ctx context.Context, stream net.Conn, backend net.Conn) {
	done := make(chan struct{}, 2)

	// stream -> socket
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, maxDatagram)
		for {
			n, err := readDatagram(stream, buf)
			if err != nil {
				return
			}
			// The socket is connected, so a plain write reaches the backend.
			if _, err := backend.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	// socket -> stream
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, maxDatagram)
		for {
			// A datagram socket returns exactly one datagram per read, so the
			// boundary is preserved by the read itself.
			n, err := backend.Read(buf)
			if err != nil {
				return
			}
			if err := writeDatagram(stream, buf[:n]); err != nil {
				return
			}
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}
	stream.Close()
	backend.Close()
	<-done
}

// pipe copies in both directions until either side is done, then closes both.
//
// Each direction half-closes what it was writing to when its source ends, so a
// peer that has finished sending gets an orderly end rather than waiting for
// the other direction to time out.
func pipe(ctx context.Context, a, b net.Conn) {
	done := make(chan struct{}, 2)
	copyOne := func(dst, src net.Conn) {
		defer func() { done <- struct{}{} }()
		_, _ = io.Copy(dst, src)
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}
	go copyOne(a, b)
	go copyOne(b, a)

	select {
	case <-done:
	case <-ctx.Done():
	}
	a.Close()
	b.Close()
	<-done // the second copy cannot outlive the closes above
}

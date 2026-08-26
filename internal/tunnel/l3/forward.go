package l3

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/tunnel/limits"
	"github.com/backpack/backpack/internal/tunnel/portmap"
	"github.com/sirupsen/logrus"
)

// The forwarder.
//
// This is what gives a layer-3 tunnel the same ports = [...] interface the
// reverse tunnel has. It is intentionally the dullest code in the package: a
// listener, a dial, and bytes copied between them. Everything that makes the
// tunnel a tunnel has already happened by the time a packet from here reaches
// the interface.
//
// # Why it runs beside the tunnel rather than inside it
//
// The engine restarts on error — a carrier that could not bind, a device that
// went away — and the forwarder deliberately does not restart with it. A user
// connection to a forwarded port has no reason to be dropped because the
// tunnel resealed its session, and rebinding the listeners on every restart
// would race the ports against the generation replacing them. So the listeners
// are opened once and outlive any number of tunnel restarts. While the tunnel
// is down the dial simply fails and the connection is refused, which is both
// correct and self-healing.

const (
	// forwardDialTimeout bounds a dial to a backend. Across a tunnel that is
	// down this is what turns a hang into a refusal.
	forwardDialTimeout = 10 * time.Second

	// udpFlowIdle is how long a UDP flow with no traffic is kept before its
	// socket is released. UDP has no close, so a timer is the only way.
	udpFlowIdle = 2 * time.Minute

	// udpBufferSize is large enough for any datagram that fits an interface
	// MTU, with room for a jumbo frame.
	udpBufferSize = 65535
)

// ForwardStats is what the forwarder reports.
type ForwardStats struct {
	Accepted uint64
	Refused  uint64
	Active   int64
}

// Forwarder serves the configured port mappings.
type Forwarder struct {
	mappings  []portmap.Mapping
	acceptUDP bool
	log       *logrus.Logger

	// limiter is nil unless the config asked for a cap. It covers the
	// forwarded ports only — routed traffic goes through the interface and
	// never passes this way, so there is nothing here to count it.
	limiter *limits.Limiter

	stats struct {
		accepted atomic.Uint64
		refused  atomic.Uint64
		active   atomic.Int64
	}
}

// NewForwarder builds the forwarder for a configuration, or returns nil when
// no ports are configured. The mappings are expanded and validated here, so a
// bad one is reported before any socket is opened.
func NewForwarder(cfg Config, log *logrus.Logger) (*Forwarder, error) {
	if len(cfg.Ports) == 0 {
		return nil, nil
	}
	mappings, err := portmap.Expand(cfg.Ports, cfg.PeerIP)
	if err != nil {
		return nil, err
	}
	if len(mappings) == 0 {
		return nil, nil
	}
	if log == nil {
		log = logrus.StandardLogger()
	}
	return &Forwarder{
		mappings:  mappings,
		acceptUDP: cfg.AcceptUDP,
		log:       log,
		limiter: limits.New(limits.Config{
			MaxConnections: cfg.MaxConnections,
			BandwidthMbps:  cfg.BandwidthMbps,
		}),
	}, nil
}

// Stats returns a snapshot.
func (f *Forwarder) Stats() ForwardStats {
	return ForwardStats{
		Accepted: f.stats.accepted.Load(),
		Refused:  f.stats.refused.Load(),
		Active:   f.stats.active.Load(),
	}
}

// Run serves every mapping until ctx ends.
func (f *Forwarder) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once

	for _, mapping := range f.mappings {
		wg.Add(1)
		go func(m portmap.Mapping) {
			defer wg.Done()
			if err := f.serveTCP(ctx, m); err != nil && ctx.Err() == nil {
				// Said out loud as well as returned. A listener that cannot
				// bind is the single most likely thing to go wrong here, and
				// what it looks like from the outside is a port that quietly
				// does nothing.
				f.log.Errorf("l3: tcp forwarder for %s stopped: %v", m.Listen, err)
				errOnce.Do(func() { firstErr = err })
			}
		}(mapping)

		if f.acceptUDP {
			wg.Add(1)
			go func(m portmap.Mapping) {
				defer wg.Done()
				if err := f.serveUDP(ctx, m); err != nil && ctx.Err() == nil {
					f.log.Errorf("l3: udp forwarder for %s stopped: %v", m.Listen, err)
					errOnce.Do(func() { firstErr = err })
				}
			}(mapping)
		}
	}

	wg.Wait()
	// firstErr is only ever set while the context was still live, so returning
	// it directly cannot turn an ordinary shutdown into a failure.
	//
	// It used to be discarded whenever the context had since been cancelled,
	// which is every shutdown — so a UDP listener that could not bind while TCP
	// bound fine was swallowed completely: Run blocked on the healthy listener
	// until cancellation, then reported success. The forwarder went on carrying
	// TCP with nothing anywhere to say the UDP half had never started.
	return firstErr
}

// ---------------------------------------------------------------- tcp

func (f *Forwarder) serveTCP(ctx context.Context, m portmap.Mapping) error {
	listener, err := net.Listen("tcp", m.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	go func() { <-ctx.Done(); listener.Close() }()

	f.log.Infof("l3: forwarding tcp %s", m)

	var cursor atomic.Uint64
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go f.handleTCP(ctx, conn, m, &cursor)
	}
}

func (f *Forwarder) handleTCP(ctx context.Context, local net.Conn, m portmap.Mapping, cursor *atomic.Uint64) {
	defer local.Close()

	if !f.limiter.Acquire() {
		f.stats.refused.Add(1)
		f.log.Debugf("l3: connection limit reached, refusing %s", local.RemoteAddr())
		return
	}
	defer f.limiter.Release()
	local = f.limiter.Wrap(local)

	f.stats.active.Add(1)
	defer f.stats.active.Add(-1)

	backend, err := dialBackend(ctx, "tcp", m.Targets, cursor)
	if err != nil {
		f.stats.refused.Add(1)
		f.log.Debugf("l3: no backend for %s: %v", m.Listen, err)
		return
	}
	defer backend.Close()
	f.stats.accepted.Add(1)

	pipe(ctx, local, backend)
}

// dialBackend tries the mapping's backends in turn, starting one further along
// than the last connection did, so healthy members share the load and a member
// that refuses is skipped rather than failing the connection.
func dialBackend(ctx context.Context, network string, targets []string, cursor *atomic.Uint64) (net.Conn, error) {
	if len(targets) == 0 {
		return nil, errors.New("no backends configured")
	}
	start := int(cursor.Add(1)-1) % len(targets)
	dialer := net.Dialer{Timeout: forwardDialTimeout}

	var lastErr error
	for i := range targets {
		target := targets[(start+i)%len(targets)]
		conn, err := dialer.DialContext(ctx, network, target)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// pipe copies in both directions until either side is done, then closes both.
//
// Each direction closes the connection it was writing to when its source ends,
// so a peer that has finished sending gets an orderly end rather than waiting
// for the other direction's timeout.
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

// ---------------------------------------------------------------- udp

// udpFlow is one client's conversation with a backend. UDP has no connection,
// so a flow is recognised by its source address and ends when it goes quiet.
type udpFlow struct {
	backend  *net.UDPConn
	lastSeen atomic.Int64 // unix nanoseconds
}

func (f *udpFlow) touch() { f.lastSeen.Store(time.Now().UnixNano()) }

func (f *udpFlow) idle(now time.Time, limit time.Duration) bool {
	return now.Sub(time.Unix(0, f.lastSeen.Load())) > limit
}

func (f *Forwarder) serveUDP(ctx context.Context, m portmap.Mapping) error {
	conn, err := net.ListenPacket("udp", m.Listen)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() { <-ctx.Done(); conn.Close() }()

	f.log.Infof("l3: forwarding udp %s", m)

	var flows sync.Map // client address string -> *udpFlow
	defer func() {
		flows.Range(func(_, v any) bool {
			v.(*udpFlow).backend.Close()
			return true
		})
	}()

	go f.reapUDPFlows(ctx, &flows)

	var cursor atomic.Uint64
	buf := make([]byte, udpBufferSize)
	for {
		n, client, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		flow, err := f.udpFlowFor(ctx, &flows, conn, client, m, &cursor)
		if err != nil {
			f.stats.refused.Add(1)
			f.log.Debugf("l3: no udp backend for %s: %v", m.Listen, err)
			continue
		}
		flow.touch()
		if _, err := flow.backend.Write(buf[:n]); err != nil {
			f.log.Debugf("l3: forwarding udp to %s: %v", m.Targets, err)
		}
	}
}

// udpFlowFor returns the flow for a client, creating it on first sight.
func (f *Forwarder) udpFlowFor(
	ctx context.Context,
	flows *sync.Map,
	local net.PacketConn,
	client net.Addr,
	m portmap.Mapping,
	cursor *atomic.Uint64,
) (*udpFlow, error) {
	key := client.String()
	if existing, ok := flows.Load(key); ok {
		return existing.(*udpFlow), nil
	}

	// A new flow counts against the cap, as a TCP connection does. UDP source
	// addresses are free to invent, so a flow table keyed on them is the
	// easiest way to make a host open sockets without limit.
	if !f.limiter.Acquire() {
		return nil, errors.New("the connection limit is reached")
	}

	conn, err := dialBackend(ctx, "udp", m.Targets, cursor)
	if err != nil {
		f.limiter.Release()
		return nil, err
	}
	udpConn, ok := conn.(*net.UDPConn)
	if !ok {
		conn.Close()
		f.limiter.Release()
		return nil, errors.New("udp backend did not yield a UDP socket")
	}

	flow := &udpFlow{backend: udpConn}
	flow.touch()

	// Two goroutines could reach here for the same client at once; only one
	// flow may survive, or the loser's reply reader would write into a socket
	// nobody is tracking.
	if actual, loaded := flows.LoadOrStore(key, flow); loaded {
		udpConn.Close()
		f.limiter.Release()
		return actual.(*udpFlow), nil
	}

	f.stats.accepted.Add(1)
	f.stats.active.Add(1)
	go f.pumpUDPReplies(ctx, flows, key, flow, local, client)
	return flow, nil
}

// pumpUDPReplies carries what the backend sends back to the client it belongs
// to, until the flow is closed or goes quiet.
func (f *Forwarder) pumpUDPReplies(
	ctx context.Context,
	flows *sync.Map,
	key string,
	flow *udpFlow,
	local net.PacketConn,
	client net.Addr,
) {
	defer func() {
		flows.Delete(key)
		flow.backend.Close()
		f.stats.active.Add(-1)
		// Paired with the Acquire in udpFlowFor. This pump is where a flow ends
		// however it ends, so it is the one place the slot is given back.
		f.limiter.Release()
	}()

	buf := make([]byte, udpBufferSize)
	for {
		// A read deadline rather than a blocking read: it is what lets a flow
		// whose backend never answers be reaped instead of holding a goroutine
		// and a socket forever.
		_ = flow.backend.SetReadDeadline(time.Now().Add(udpFlowIdle))
		n, err := flow.backend.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() && !flow.idle(time.Now(), udpFlowIdle) {
				continue // the client is still sending; keep waiting for a reply
			}
			return
		}
		flow.touch()
		if _, err := local.WriteTo(buf[:n], client); err != nil {
			return
		}
	}
}

// reapUDPFlows closes flows that have gone quiet in both directions.
func (f *Forwarder) reapUDPFlows(ctx context.Context, flows *sync.Map) {
	ticker := time.NewTicker(udpFlowIdle / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			flows.Range(func(_, v any) bool {
				if flow := v.(*udpFlow); flow.idle(now, udpFlowIdle) {
					// Closing wakes the reply pump, which does the removal.
					flow.backend.Close()
				}
				return true
			})
		}
	}
}

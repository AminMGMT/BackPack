package direct

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/tunnel/limits"
	"github.com/backpack/backpack/internal/tunnel/portmap"
	"github.com/sirupsen/logrus"
)

// The Iran side.
//
// It keeps one or more mux sessions open to the origin, and serves the
// forwarded ports. A user connection becomes a stream on whichever session is
// healthy; the origin dials the backend at the far end.
//
// # Why the listeners do not wait for the tunnel
//
// The ports are bound as soon as the edge starts, before any session exists.
// That is deliberate: the listeners are what users reach, and a port that
// disappears whenever the tunnel reconnects is worse than one that accepts and
// then refuses. While no session is up a connection is refused promptly, which
// is a thing a client can act on.

// EdgeStats is what the Iran side reports.
type EdgeStats struct {
	Sessions int64
	Accepted uint64
	Refused  uint64
	Active   int64
}

// Edge is the dialling half of a direct tunnel.
type Edge struct {
	cfg      Config
	mappings []portmap.Mapping
	log      *logrus.Logger

	// limiter is nil unless the config asked for a cap, so an unlimited
	// tunnel pays a nil check and nothing else.
	limiter *limits.Limiter

	// sessions holds the live mux sessions, replaced wholesale whenever one is
	// added or removed. Reads are frequent — one per user connection — and
	// writes are rare, so the lock is held only long enough to copy a slice
	// header.
	mu       sync.RWMutex
	sessions []*tunnelSession

	cursor atomic.Uint64

	stats struct {
		sessions atomic.Int64
		accepted atomic.Uint64
		refused  atomic.Uint64
		active   atomic.Int64
	}
}

// NewEdge validates a configuration and opens nothing.
func NewEdge(cfg Config, log *logrus.Logger) (*Edge, error) {
	cfg.Role = RoleEdge
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	mappings, err := portmap.Expand(cfg.Ports, DefaultBackendHost)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = logrus.StandardLogger()
	}
	return &Edge{
		cfg:      cfg,
		mappings: mappings,
		log:      log,
		limiter: limits.New(limits.Config{
			MaxConnections: cfg.MaxConnections,
			BandwidthMbps:  cfg.BandwidthMbps,
		}),
	}, nil
}

// Stats returns a snapshot.
func (e *Edge) Stats() EdgeStats {
	return EdgeStats{
		Sessions: e.stats.sessions.Load(),
		Accepted: e.stats.accepted.Load(),
		Refused:  e.stats.refused.Load(),
		Active:   e.stats.active.Load(),
	}
}

// Run keeps the sessions up and serves the ports until ctx ends.
func (e *Edge) Run(ctx context.Context) error {
	var wg sync.WaitGroup

	for i := 0; i < e.cfg.Sessions; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			e.keepSession(ctx, index)
		}(i)
	}

	for _, mapping := range e.mappings {
		wg.Add(1)
		go func(m portmap.Mapping) {
			defer wg.Done()
			if err := e.serve(ctx, m); err != nil && ctx.Err() == nil {
				e.log.Errorf("direct: forwarding %s stopped: %v", m, err)
			}
		}(mapping)

		if e.cfg.AcceptUDP {
			wg.Add(1)
			go func(m portmap.Mapping) {
				defer wg.Done()
				if err := e.serveUDP(ctx, m); err != nil && ctx.Err() == nil {
					e.log.Errorf("direct: forwarding udp %s stopped: %v", m, err)
				}
			}(mapping)
		}
	}

	wg.Wait()
	return nil
}

// ---------------------------------------------------------------- sessions

// keepSession dials one session and redials it whenever it drops.
func (e *Edge) keepSession(ctx context.Context, index int) {
	for {
		if ctx.Err() != nil {
			return
		}

		session, err := dialSession(ctx, &e.cfg)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			e.log.Warnf("direct: session %d could not be established: %v — retrying in %s",
				index, err, e.cfg.RetryDelay)
			if !sleepUntil(ctx, e.cfg.RetryDelay) {
				return
			}
			continue
		}

		e.addSession(session)
		e.log.Infof("direct: session %d established with %s", index, e.cfg.Addr)

		// smux keeps the session alive on its own; this waits for its transport
		// to fail. See watchedConn for why smux's own IsClosed cannot be used.
		select {
		case <-ctx.Done():
			e.removeSession(session)
			session.Close()
			return
		case <-session.Dead():
		}

		e.removeSession(session)
		session.Close()
		if ctx.Err() != nil {
			return
		}
		e.log.Warnf("direct: session %d dropped — reconnecting in %s", index, e.cfg.RetryDelay)
		if !sleepUntil(ctx, e.cfg.RetryDelay) {
			return
		}
	}
}

func sleepUntil(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (e *Edge) addSession(session *tunnelSession) {
	e.mu.Lock()
	e.sessions = append(append([]*tunnelSession(nil), e.sessions...), session)
	e.mu.Unlock()
	e.stats.sessions.Add(1)
}

func (e *Edge) removeSession(session *tunnelSession) {
	e.mu.Lock()
	next := make([]*tunnelSession, 0, len(e.sessions))
	removed := false
	for _, s := range e.sessions {
		if s == session {
			removed = true
			continue
		}
		next = append(next, s)
	}
	e.sessions = next
	e.mu.Unlock()
	if removed {
		e.stats.sessions.Add(-1)
	}
}

// pickSession returns a live session, rotating so several sessions share the
// streams. A session that has closed since it was listed is skipped.
func (e *Edge) pickSession() *tunnelSession {
	e.mu.RLock()
	sessions := e.sessions
	e.mu.RUnlock()

	if len(sessions) == 0 {
		return nil
	}
	start := int(e.cursor.Add(1)-1) % len(sessions)
	for i := range sessions {
		if s := sessions[(start+i)%len(sessions)]; s.Usable() {
			return s
		}
	}
	return nil
}

// ---------------------------------------------------------------- ports

func (e *Edge) serve(ctx context.Context, m portmap.Mapping) error {
	listener, err := net.Listen("tcp", m.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	go func() { <-ctx.Done(); listener.Close() }()

	e.log.Infof("direct: forwarding %s", m)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go e.handle(ctx, conn, m)
	}
}

// handle joins one user connection to a stream on the tunnel.
func (e *Edge) handle(ctx context.Context, local net.Conn, m portmap.Mapping) {
	defer local.Close()

	// Refused before the connection costs anything: a slot taken and then
	// given back is a slot another connection could not have had.
	if !e.limiter.Acquire() {
		e.stats.refused.Add(1)
		e.log.Debugf("direct: connection limit reached, refusing %s", local.RemoteAddr())
		return
	}
	defer e.limiter.Release()
	local = e.limiter.Wrap(local)

	stream, err := e.openFor(kindTCP, m)
	if err != nil {
		e.stats.refused.Add(1)
		e.log.Debugf("direct: refusing %s: %v", local.RemoteAddr(), err)
		return
	}
	defer stream.Close()

	e.stats.accepted.Add(1)
	e.stats.active.Add(1)
	defer e.stats.active.Add(-1)

	pipe(ctx, local, stream)
}

// openFor opens a stream to one of a mapping's backends.
//
// Each backend is tried in turn from a rotating start, so a target the origin
// cannot reach falls through to the next rather than failing the connection,
// and healthy ones share the load.
func (e *Edge) openFor(kind byte, m portmap.Mapping) (net.Conn, error) {
	session := e.pickSession()
	if session == nil {
		return nil, errNoSession
	}

	var lastErr error
	start := int(e.cursor.Add(1)-1) % len(m.Targets)
	for i := range m.Targets {
		target := m.Targets[(start+i)%len(m.Targets)]
		stream, err := openStream(session, kind, target, e.cfg.DialTimeout)
		if err == nil {
			return stream, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("direct: no backends configured")
	}
	return nil, lastErr
}

var (
	errNoSession = errors.New("direct: no tunnel session is established")
	errFlowLimit = errors.New("direct: the connection limit is reached")
)

// ---------------------------------------------------------------- udp

// udpFlow is one client's conversation, carried on a stream of its own.
//
// UDP has no connection to open or close, so a flow is recognised by its
// source address and ends when it goes quiet. Giving each flow its own stream
// is what keeps two clients' datagrams from being interleaved into one
// conversation at the far end.
type udpFlow struct {
	stream   net.Conn
	lastSeen atomic.Int64 // unix nanoseconds
}

func (f *udpFlow) touch() { f.lastSeen.Store(time.Now().UnixNano()) }

func (f *udpFlow) idle(now time.Time, limit time.Duration) bool {
	return now.Sub(time.Unix(0, f.lastSeen.Load())) > limit
}

// udpFlowIdle is how long a flow with no traffic in either direction is kept.
const udpFlowIdle = 2 * time.Minute

func (e *Edge) serveUDP(ctx context.Context, m portmap.Mapping) error {
	conn, err := net.ListenPacket("udp", m.Listen)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() { <-ctx.Done(); conn.Close() }()

	e.log.Infof("direct: forwarding udp %s", m)

	var flows sync.Map // client address -> *udpFlow
	defer func() {
		flows.Range(func(_, v any) bool {
			v.(*udpFlow).stream.Close()
			return true
		})
	}()
	go e.reapUDPFlows(ctx, &flows)

	buf := make([]byte, maxDatagram)
	for {
		n, client, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		flow, err := e.udpFlowFor(&flows, conn, client, m)
		if err != nil {
			e.stats.refused.Add(1)
			e.log.Debugf("direct: no udp backend for %s: %v", m.Listen, err)
			continue
		}
		flow.touch()
		if err := writeDatagram(flow.stream, buf[:n]); err != nil {
			e.log.Debugf("direct: forwarding a datagram for %s: %v", client, err)
			flow.stream.Close()
		}
	}
}

// udpFlowFor returns the flow for a client, opening a stream on first sight.
func (e *Edge) udpFlowFor(flows *sync.Map, local net.PacketConn, client net.Addr, m portmap.Mapping) (*udpFlow, error) {
	key := client.String()
	if existing, ok := flows.Load(key); ok {
		return existing.(*udpFlow), nil
	}

	// A new flow counts against the cap exactly as a TCP connection does.
	//
	// It has to. A flow is recognised by its source address and UDP source
	// addresses are free to invent, so without this a single sender can make
	// the edge open a stream and the origin a socket for every address it
	// cares to forge — which is the cap's whole purpose, on the one protocol
	// where it is easiest to abuse.
	if !e.limiter.Acquire() {
		return nil, errFlowLimit
	}

	stream, err := e.openFor(kindUDP, m)
	if err != nil {
		e.limiter.Release()
		return nil, err
	}
	flow := &udpFlow{stream: stream}
	flow.touch()

	// Two datagrams from the same client can arrive together; only one flow
	// may survive, or the loser's reply reader would be writing replies for a
	// stream nobody is sending on.
	if actual, loaded := flows.LoadOrStore(key, flow); loaded {
		stream.Close()
		e.limiter.Release()
		return actual.(*udpFlow), nil
	}

	e.stats.accepted.Add(1)
	e.stats.active.Add(1)
	go e.pumpUDPReplies(flows, key, flow, local, client)
	return flow, nil
}

// pumpUDPReplies carries what comes back off the stream to the client it
// belongs to.
func (e *Edge) pumpUDPReplies(flows *sync.Map, key string, flow *udpFlow, local net.PacketConn, client net.Addr) {
	defer func() {
		flows.Delete(key)
		flow.stream.Close()
		e.stats.active.Add(-1)
		// Paired with the Acquire in udpFlowFor. This pump is the one place a
		// flow ends, whether it went quiet, was reaped or the stream failed, so
		// it is the one place the slot can be given back exactly once.
		e.limiter.Release()
	}()

	buf := make([]byte, maxDatagram)
	for {
		n, err := readDatagram(flow.stream, buf)
		if err != nil {
			return
		}
		flow.touch()
		if _, err := local.WriteTo(buf[:n], client); err != nil {
			return
		}
	}
}

// reapUDPFlows closes flows that have gone quiet in both directions. Closing
// the stream wakes the reply pump, which does the removal.
func (e *Edge) reapUDPFlows(ctx context.Context, flows *sync.Map) {
	ticker := time.NewTicker(udpFlowIdle / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			flows.Range(func(_, v any) bool {
				if flow := v.(*udpFlow); flow.idle(now, udpFlowIdle) {
					flow.stream.Close()
				}
				return true
			})
		}
	}
}

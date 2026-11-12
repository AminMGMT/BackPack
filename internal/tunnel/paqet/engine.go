package paqet

// The client/server split, the stream protocol handling and the dial/accept
// loops are derived from paqet's internal/client, internal/server and
// internal/forward (MIT); see doc.go. Changed: a supervised slot per KCP
// session, a hello on each, status replies, framed UDP, Backpack's logging, and
// liveness for the watchdog.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/sirupsen/logrus"
	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
)

// ErrFlowStuck is Run's answer when the generation ended because nothing in it
// ever got an answer from the other server. The caller opens a new one, from a
// new range of source ports: a middlebox that has stopped passing a flow keeps
// dropping that flow however often it is retried.
var ErrFlowStuck = errors.New("pck: the other server has not answered for a long time — reopening from new source ports")

// helloTimeout bounds the exchange that proves a session reaches the other
// server. A variable so a test need not wait.
var helloTimeout = 6 * time.Second

const (
	// requestTimeout bounds reading a stream's first message on the server.
	requestTimeout = 15 * time.Second

	// dialTimeout bounds the server's dial to a target.
	dialTimeout = 8 * time.Second

	// udpRelayIdle bounds a UDP relay no one has spoken in. The forwarder closes
	// idle flows itself; this is the server's own limit for a client that went
	// away without closing.
	udpRelayIdle = 3 * time.Minute

	// retryMin and retryMax bound the pause between a client session's
	// attempts.
	retryMin = time.Second
	retryMax = 10 * time.Second

	// reportEveryDefault paces repeated warnings.
	reportEveryDefault = time.Minute
)

// flowStuckAfter is how long a generation with no live session waits before it
// gives up and asks to be reopened. A variable so a test need not wait.
var flowStuckAfter = 90 * time.Second

// wireSet is what a generation runs on: the wire and the claim on the host
// behind it.
type wireSet struct {
	wire  *Wire
	lease portLease
	local net.IP
	// serverSocket and listenPort are the server's one socket and its port;
	// the client's peer is Engine.peer.
	serverSocket *Socket
	listenPort   uint16
	close        func()
}

// portLease is what the engine needs of network.PckLease; tests substitute a
// counter.
type portLease interface {
	ClaimPort() (uint16, error)
	ReleasePort(uint16)
	GuardInstalled() bool
	Close()
}

// Engine is one tunnel's PCK: a client (dials and forwards) or a server
// (accepts and dials targets). Its state outlives generations; Run is one.
type Engine struct {
	cfg   Config
	p     Params
	log   *logrus.Logger
	block kcp.BlockCrypt
	peer  atomic.Pointer[net.UDPAddr] // client only; resolved at the start of each generation

	// open is swapped by tests; nil opens the real capture.
	open func(context.Context) (*wireSet, error)

	mu  sync.RWMutex
	gen *generation

	carry WireStats // counters of finished generations

	warnPeer, warnForeign, warnRefuse, warnTx, warnBusy, warnUDP reportEvery

	// targets decides what a client may ask this server to dial, and dials
	// counts the forwarded connections open through it. Server only.
	targets targetPolicy
	dials   streamLimit

	// stuck is set when a generation ends because the flow went unanswered.
	stuck atomic.Bool

	// sessions counts sessions that completed a hello, for Stats.
	sessions atomic.Uint64

	// dgc seals and opens the datagram lane's packets; dgOut, dgIn and dgDrop
	// count what crossed it at this end and what a full flow queue refused.
	dgc                 *dgCodec
	dgOut, dgIn, dgDrop atomic.Uint64

	// udpDrops counts forwarded datagrams dropped because their flow's queue
	// into the tunnel was full (datagramsend.go), at this end.
	udpDrops atomic.Uint64
}

type reportEvery struct {
	mu   sync.Mutex
	last time.Time
}

func (r *reportEvery) ok() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.last) < reportEveryDefault {
		return false
	}
	r.last = time.Now()
	return true
}

// New validates the configuration and builds the engine. It opens nothing.
func New(cfg Config, log *logrus.Logger) (*Engine, error) {
	cfg.Params = cfg.Params.Resolve("")
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = logrus.StandardLogger()
	}
	key := DeriveKey(cfg.Token)
	block, err := newBlock(cfg.Params.Block, key)
	if err != nil {
		return nil, fmt.Errorf("pck_kcp_block %q: %w", cfg.Params.Block, err)
	}
	dgc, err := newDGCodec(key, cfg.Server)
	if err != nil {
		return nil, err
	}
	e := &Engine{cfg: cfg, p: cfg.Params, log: log, block: block, dgc: dgc}
	e.targets.remote = cfg.RemoteTargets
	e.dials.max = maxServerStreams
	return e, nil
}

// Config is the configuration the engine runs on, with its parameters resolved.
func (e *Engine) Config() Config { return e.cfg }

// resolvePeer looks the other server's name up under ctx. It runs once per
// generation, not once per process: a restart is what notices a peer whose
// address has moved, and a name that does not resolve yet at boot (the network
// comes up after services do) is a retry, not a reason not to start.
func (e *Engine) resolvePeer(ctx context.Context) error {
	host, portStr, err := net.SplitHostPort(e.cfg.Addr)
	if err != nil {
		return err
	}
	port, _ := strconv.Atoi(portStr)
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() == nil {
			return fmt.Errorf("pck: only IPv4 peers are supported, %s is not", ip)
		}
		e.peer.Store(&net.UDPAddr{IP: ip.To4(), Port: port})
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(rctx, "ip4", host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("pck: cannot resolve %q to an IPv4 address: %v", host, err)
	}
	e.peer.Store(&net.UDPAddr{IP: ips[0], Port: port})
	return nil
}

// generation is one Run: a wire, and the sessions on it.
type generation struct {
	e      *Engine
	ws     *wireSet
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.RWMutex
	sessions []*session // client: one slot each (nil when down); server: the live ones
	next     atomic.Uint64
	burned   []uint16

	// ctl drives the sessions' send window; see congestion.go.
	ctl *windowControl

	everUp atomic.Bool
}

// session is one KCP session with its smux session on top.
type session struct {
	sm      *smux.Session
	kc      *kcp.UDPSession
	sock    *Socket
	remote  net.Addr
	since   time.Time
	helloed atomic.Bool
	id      uint64
	// lane is the session's datagram lane; nil when it has none.
	lane *dgLane
}

func (s *session) close() {
	if s.sm != nil {
		s.sm.Close()
	}
	if s.kc != nil {
		s.kc.Close()
	}
	if s.sock != nil {
		s.sock.Close()
	}
}

func (s *session) live() bool { return s.helloed.Load() && s.sm != nil && !s.sm.IsClosed() }

// Up reports whether a session has been proved by a hello and is still alive.
func (e *Engine) Up() bool {
	e.mu.RLock()
	g := e.gen
	e.mu.RUnlock()
	if g == nil {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, s := range g.sessions {
		if s != nil && s.live() {
			return true
		}
	}
	return false
}

// Peer is the other server's address as the engine knows it: the dialled
// address on the client, the most recent live client on the server.
func (e *Engine) Peer() net.Addr {
	if p := e.peer.Load(); p != nil {
		return p
	}
	e.mu.RLock()
	g := e.gen
	e.mu.RUnlock()
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for i := len(g.sessions) - 1; i >= 0; i-- {
		if s := g.sessions[i]; s != nil && s.live() {
			return s.remote
		}
	}
	return nil
}

// Stats are the wire's counters across every generation so far.
func (e *Engine) Stats() WireStats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := e.carry
	out.Sessions = e.sessions.Load()
	if e.gen != nil {
		w := e.gen.ws.wire.Stats()
		out.PacketsIn += w.PacketsIn
		out.PacketsOut += w.PacketsOut
		out.BytesIn += w.BytesIn
		out.BytesOut += w.BytesOut
		out.RxDrops += w.RxDrops
		out.Foreign += w.Foreign
		out.TxErrors += w.TxErrors
	}
	return out
}

// Run opens the wire and serves until ctx ends (nil), the wire fails, or the
// other server stays silent (ErrFlowStuck). Cleans up everything it opened.
func (e *Engine) Run(ctx context.Context) error {
	if !e.cfg.Server {
		if err := e.resolvePeer(ctx); err != nil {
			return err
		}
	}
	open := e.open
	if open == nil {
		open = e.openReal
	}
	ws, err := open(ctx)
	if err != nil {
		return err
	}
	gctx, cancel := context.WithCancel(ctx)
	g := &generation{e: e, ws: ws, ctx: gctx, cancel: cancel}
	g.ctl = newWindowControl(g, e.p.SndWnd, e.p.RcvWnd)
	if !e.cfg.Server {
		// Before the generation is published: Up and Dial read the slice.
		g.sessions = make([]*session, e.p.Conns)
	}
	defer ws.close()
	ws.wire.SetForeignHook(e.noteForeign)

	e.mu.Lock()
	e.gen = g
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		w := ws.wire.Stats()
		e.carry.PacketsIn += w.PacketsIn
		e.carry.PacketsOut += w.PacketsOut
		e.carry.BytesIn += w.BytesIn
		e.carry.BytesOut += w.BytesOut
		e.carry.RxDrops += w.RxDrops
		e.carry.Foreign += w.Foreign
		e.carry.TxErrors += w.TxErrors
		e.gen = nil
		e.mu.Unlock()
		metrics.ClearPeer()
	}()

	if !ws.lease.GuardInstalled() {
		e.log.Warnf("pck: the firewall rules that keep this host's kernel out of the tunnel's flow are not " +
			"in place (iptables is missing, or Backpack is not allowed to change it). The kernel will answer " +
			"the tunnel's segments with RSTs, and a firewall in between may then drop the flow.")
	}

	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()

	if e.cfg.Server {
		wg.Add(1)
		go func() { defer wg.Done(); defer cancel(); g.serve() }()
	} else {
		for i := 0; i < e.p.Conns; i++ {
			wg.Add(1)
			go func(i int) { defer wg.Done(); g.slot(i) }(i)
		}
	}
	wg.Add(1)
	go func() { defer wg.Done(); g.watch() }()
	wg.Add(1)
	go func() { defer wg.Done(); g.ctl.run(gctx.Done()) }()

	select {
	case <-ctx.Done():
		return nil
	case <-ws.wire.Done():
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("pck: the packet capture stopped")
	case <-gctx.Done():
		if ctx.Err() != nil {
			return nil
		}
		if e.stuck.Swap(false) {
			return ErrFlowStuck
		}
		return errors.New("pck: the tunnel stopped unexpectedly")
	}
}

// watch publishes liveness and, on the client, decides when a silent flow is
// stuck.
func (g *generation) watch() {
	e := g.e
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var downSince = time.Now()
	wasUp := false
	var lastPeer string
	var seenTx, seenUDP uint64
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-tick.C:
		}
		if n := g.ws.wire.txErrs.Load(); n > seenTx {
			if e.warnTx.ok() {
				reason := "unknown"
				if p := g.ws.wire.lastTxErr.Load(); p != nil {
					reason = (*p).Error()
				}
				e.log.Warnf("pck: %d segments could not be sent so far (last error: %s). KCP retransmits them; "+
					"a full transmit queue is the usual reason, and a slower link than the preset assumes the cause.", n, reason)
			}
			seenTx = n
		}
		if n := e.udpDrops.Load(); n > seenUDP {
			if e.warnUDP.ok() {
				e.log.Warnf("pck: %d forwarded UDP datagrams were dropped so far at this end because the tunnel "+
					"was full: their flows offered more than the tunnel carried at the time.", n)
			}
			seenUDP = n
		}
		up := e.Up()
		switch {
		case up && !wasUp:
			downSince = time.Time{}
		case !up && wasUp:
			downSince = time.Now()
			metrics.ClearPeer()
			lastPeer = ""
			e.log.Warnf("pck: the other server has stopped answering")
		case !up && downSince.IsZero():
			downSince = time.Now()
		}
		wasUp = up
		if up {
			if p := e.Peer(); p != nil && p.String() != lastPeer {
				lastPeer = p.String()
				metrics.ReportPeer(lastPeer)
			}
		}
		if !e.cfg.Server && !up && !downSince.IsZero() && time.Since(downSince) > flowStuckAfter {
			e.stuck.Store(true)
			g.cancel()
			return
		}
	}
}

func (e *Engine) noteForeign(src net.Addr) {
	if !e.warnForeign.ok() {
		return
	}
	e.log.Errorf("pck: %s is still running the previous PCK (a raw datagram handshake arrived). "+
		"PCK was replaced in this version of Backpack and the two wires cannot talk to each other. "+
		"Update the other server to the same version (sudo backpack → Update) and the tunnel will come up.", src)
}

// ------------------------------------------------------------------ client

// slot keeps one KCP session up: dial, hold, redial.
func (g *generation) slot(idx int) {
	e := g.e
	backoff := retryMin
	attempts := 0
	for g.ctx.Err() == nil {
		s, err := g.dialSession()
		if err != nil {
			attempts++
			if g.ctx.Err() != nil {
				return
			}
			if e.warnPeer.ok() {
				e.log.Warnf("pck: no answer from %s (attempt %d): %v. Check that the other server is running "+
					"this tunnel with the same token and port, that its firewall lets TCP port %d in, and that "+
					"it runs this version of Backpack — the PCK wire changed in v2.0.0 and an older release "+
					"cannot answer it.", e.peer.Load(), attempts, err, e.peer.Load().Port)
			}
			select {
			case <-g.ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff *= 2; backoff > retryMax {
				backoff = retryMax
			}
			continue
		}
		attempts, backoff = 0, retryMin
		g.everUp.Store(true)
		e.sessions.Add(1)
		g.mu.Lock()
		g.sessions[idx] = s
		g.mu.Unlock()
		e.log.Infof("pck: session %d to %s is up (source port %d, %s, %d sessions wanted)",
			idx+1, e.peer.Load(), s.sock.port, e.p.Mode, e.p.Conns)
		select {
		case <-s.sm.CloseChan():
			if g.ctx.Err() == nil {
				e.log.Warnf("pck: session %d to %s was lost — redialling", idx+1, e.peer.Load())
			}
		case <-g.ctx.Done():
		}
		g.mu.Lock()
		if g.sessions[idx] == s {
			g.sessions[idx] = nil
		}
		g.mu.Unlock()
		// A session that dies on a live wire may have had its flow cut by the
		// path, so its port is not reused for the next one: the redial goes out
		// from a new source port straight away rather than after a failure on
		// the old one. The port is released with the next success.
		if g.ctx.Err() == nil {
			g.burn(s.sock.port)
		}
		s.close()
	}
}

// dialSession builds a session and proves it with a hello.
func (g *generation) dialSession() (*session, error) {
	e := g.e
	port, err := g.ws.lease.ClaimPort()
	if err != nil {
		return nil, err
	}
	sock, err := g.ws.wire.Open(port, func() {
		g.mu.Lock()
		burn := false
		for _, b := range g.burned {
			if b == port {
				burn = true
			}
		}
		g.mu.Unlock()
		if !burn {
			g.ws.lease.ReleasePort(port)
		}
	})
	if err != nil {
		g.ws.lease.ReleasePort(port)
		return nil, err
	}
	s := &session{sock: sock, remote: e.peer.Load(), since: time.Now()}
	s.lane = e.newLane(sock, s.remote)
	if s.lane != nil {
		sock.SetDatagramHook(e.laneHook(func(net.Addr) *dgLane { return s.lane }))
	}
	fail := func(err error) (*session, error) {
		// The port stays claimed for a while: handing the lowest free port to
		// the next attempt would send the same flow again, and a flow that a
		// middlebox has stopped passing is not helped by being retried. It is
		// burned BEFORE the socket closes, because the socket's close releases
		// a port that is not burned — the other order gave the port straight
		// back and every retry went out from the same one.
		g.burn(port)
		s.close()
		return nil, err
	}
	kc, err := kcp.NewConn(e.peer.Load().String(), e.block, e.p.DShard, e.p.PShard, sock)
	if err != nil {
		return fail(fmt.Errorf("kcp: %w", err))
	}
	s.kc = kc
	applyKCP(kc, e.p)
	sm, err := smux.Client(kc, smuxConfig(e.p))
	if err != nil {
		return fail(fmt.Errorf("smux: %w", err))
	}
	s.sm = sm
	kc.SetWindowSize(g.ctl.Window(), e.p.RcvWnd)
	stop := context.AfterFunc(g.ctx, func() { s.close() })
	err = e.clientHello(sm)
	stop()
	if err != nil {
		return fail(err)
	}
	s.helloed.Store(true)
	g.unburn()
	return s, nil
}

// newLane is the datagram lane to a peer over sock, or nil if the peer's
// address has no usable form.
func (e *Engine) newLane(sock *Socket, remote net.Addr) *dgLane {
	l := newDGLane(e.dgc, sock, remote, e.p.MTU)
	if l != nil {
		l.engine = e
	}
	return l
}

// laneHook is the socket hook that takes the lane's packets: it opens them and
// gives each datagram to the flow it names, in the session lookup finds.
func (e *Engine) laneHook(lookup func(src net.Addr) *dgLane) func(*net.UDPAddr, []byte) bool {
	scratch := make([]byte, frameBufLen)
	return func(src *net.UDPAddr, payload []byte) bool {
		flow, data, ok := e.dgc.open(payload, scratch)
		if !ok {
			return false
		}
		if l := lookup(src); l != nil {
			l.receive(flow, data)
		}
		return true
	}
}

// burn keeps a failed attempt's port claimed, up to a handful.
func (g *generation) burn(port uint16) {
	g.mu.Lock()
	g.burned = append(g.burned, port)
	var drop []uint16
	if len(g.burned) > 12 {
		drop = g.burned[:len(g.burned)-12]
		g.burned = g.burned[len(g.burned)-12:]
	}
	g.mu.Unlock()
	for _, p := range drop {
		g.ws.lease.ReleasePort(p)
	}
}

func (g *generation) unburn() {
	g.mu.Lock()
	ports := g.burned
	g.burned = nil
	g.mu.Unlock()
	for _, p := range ports {
		g.ws.lease.ReleasePort(p)
	}
}

func (e *Engine) clientHello(sm *smux.Session) error {
	st, err := sm.OpenStream()
	if err != nil {
		return fmt.Errorf("opening the control stream: %w", err)
	}
	defer st.Close()
	st.SetDeadline(time.Now().Add(helloTimeout))
	if err := (&proto{Type: pHello, Version: engineRevision}).write(st); err != nil {
		return fmt.Errorf("sending the hello: %w", err)
	}
	var reply proto
	if err := reply.read(st); err != nil {
		return fmt.Errorf("no hello back within %s: %w", helloTimeout, err)
	}
	if reply.Type != pHello {
		return fmt.Errorf("the other server answered the hello with message type %d", reply.Type)
	}
	if reply.Version != engineRevision {
		return fmt.Errorf("the other server runs PCK engine revision %d and this one %d — update the older of the two", reply.Version, engineRevision)
	}
	return nil
}

// pick returns a live session, taking them in turn.
func (g *generation) pick() *session {
	g.mu.RLock()
	defer g.mu.RUnlock()
	n := len(g.sessions)
	if n == 0 {
		return nil
	}
	start := int(g.next.Add(1) - 1)
	for i := 0; i < n; i++ {
		if s := g.sessions[(start+i)%n]; s != nil && s.live() {
			return s
		}
	}
	return nil
}

var errNotUp = errors.New("pck: the tunnel is not up yet — no session with the other server has completed")

// Dial opens a forwarded connection to addr through the other server, which
// dials it. network is "tcp" or "udp". report, if not nil, is called once with
// the other server's verdict (nil when it connected) when the first read
// learns it.
func (e *Engine) Dial(ctx context.Context, network, addr string, report func(error)) (net.Conn, error) {
	var kind byte
	switch network {
	case "tcp", "tcp4", "tcp6":
		kind = pTCP
	case "udp", "udp4", "udp6":
		kind = pUDP
	default:
		return nil, fmt.Errorf("pck: cannot forward %q", network)
	}
	target, err := ParseAddr(addr)
	if err != nil {
		return nil, fmt.Errorf("pck: target %q: %w", addr, err)
	}
	e.mu.RLock()
	g := e.gen
	e.mu.RUnlock()
	if g == nil || e.cfg.Server {
		return nil, errNotUp
	}
	var lastErr error = errNotUp
	for tries := 0; tries < len(g.sessions)+1; tries++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s := g.pick()
		if s == nil {
			return nil, lastErr
		}
		st, err := s.sm.OpenStream()
		if err != nil {
			lastErr = err
			continue
		}
		if err := (&proto{Type: kind, Addr: target}).write(st); err != nil {
			st.Close()
			lastErr = err
			continue
		}
		if kind == pTCP {
			return &tcpConn{framedStream: newFramedStream(st), st: statusReader{report: report}}, nil
		}
		return newDatagramConn(st, report, s.lane, newDatagramSender(st, &e.udpDrops)), nil
	}
	return nil, lastErr
}

// ------------------------------------------------------------------ server

func (g *generation) serve() {
	e := g.e
	l, err := kcp.ServeConn(e.block, e.p.DShard, e.p.PShard, g.ws.serverSocket)
	if err != nil {
		e.log.Errorf("pck: could not start the KCP listener: %v", err)
		return
	}
	defer l.Close()
	// kcp-go's listener does not close the sessions it accepted, so a
	// generation that ends would leave them flushing to a closed wire until
	// their keepalive gave up. They end with it.
	context.AfterFunc(g.ctx, func() {
		l.Close()
		g.mu.RLock()
		live := slices.Clone(g.sessions)
		g.mu.RUnlock()
		for _, s := range live {
			s.close()
		}
	})
	g.ws.serverSocket.SetDatagramHook(e.laneHook(g.laneFor))
	e.log.Infof("pck: listening for the other server on TCP port %d (%s, windows up to %d packets, %s)",
		g.ws.listenPort, e.p.Mode, e.p.SndWnd, e.p.Block)
	var id atomic.Uint64
	for {
		kc, err := l.AcceptKCP()
		if err != nil {
			if g.ctx.Err() != nil {
				return
			}
			e.log.Errorf("pck: accepting a session failed: %v", err)
			return
		}
		applyKCP(kc, e.p)
		sm, err := smux.Server(kc, smuxConfig(e.p))
		if err != nil {
			kc.Close()
			continue
		}
		s := &session{sm: sm, kc: kc, remote: kc.RemoteAddr(), since: time.Now(), id: id.Add(1)}
		kc.SetWindowSize(g.ctl.Window(), e.p.RcvWnd)
		s.lane = e.newLane(g.ws.serverSocket, s.remote)
		g.mu.Lock()
		g.sessions = append(g.sessions, s)
		g.mu.Unlock()
		if g.ctx.Err() != nil {
			// Accepted as the generation ended, after the sweep above.
			s.close()
		}
		go g.serveSession(s)
	}
}

// laneFor is the datagram lane of the session a segment came from.
func (g *generation) laneFor(src net.Addr) *dgLane {
	ua, ok := src.(*net.UDPAddr)
	if !ok {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, s := range g.sessions {
		if s == nil || s.lane == nil {
			continue
		}
		if s.lane.remote.Port == ua.Port && s.lane.remote.IP.Equal(ua.IP) {
			return s.lane
		}
	}
	return nil
}

func (g *generation) serveSession(s *session) {
	e := g.e
	defer func() {
		s.close()
		g.mu.Lock()
		for i, o := range g.sessions {
			if o == s {
				g.sessions = append(g.sessions[:i], g.sessions[i+1:]...)
				break
			}
		}
		g.mu.Unlock()
		if s.helloed.Load() && g.ctx.Err() == nil {
			e.log.Infof("pck: session from %s ended after %s", s.remote, time.Since(s.since).Round(time.Second))
		}
	}()
	for {
		st, err := s.sm.AcceptStream()
		if err != nil {
			return
		}
		go g.serveStream(s, st)
	}
}

func (g *generation) serveStream(s *session, st *smux.Stream) {
	e := g.e
	defer st.Close()
	st.SetReadDeadline(time.Now().Add(requestTimeout))
	var p proto
	if err := p.read(st); err != nil {
		if errors.Is(err, errStockPaqet) || errors.Is(err, errBadMagic) {
			e.log.Warnf("pck: %s sent a stream this engine cannot read: %v", s.remote, err)
		}
		return
	}
	st.SetReadDeadline(time.Time{})
	switch p.Type {
	case pHello:
		if p.Version != engineRevision {
			(&proto{Type: pHello, Version: engineRevision}).write(st)
			e.log.Errorf("pck: %s runs PCK engine revision %d and this server %d — update the older of the two",
				s.remote, p.Version, engineRevision)
			return
		}
		// Up before the reply leaves: the client opens its first forwarded
		// stream as soon as it reads the reply, and that stream must find the
		// session proved.
		if !s.helloed.Swap(true) {
			e.sessions.Add(1)
			e.log.Infof("pck: session from %s is up", s.remote)
		}
		(&proto{Type: pHello, Version: engineRevision}).write(st)
	case pPing:
		(&proto{Type: pPong}).write(st)
	case pTCP, pUDP:
		// A session that has not said which engine revision it speaks does
		// not get a target dialled: the client never asks before its hello is
		// answered, so a request without one is not a Backpack client of this
		// version.
		if !s.helloed.Load() {
			st.Write([]byte{statusRefused})
			return
		}
		release, ok := g.e.dials.acquire()
		if !ok {
			if e.warnBusy.ok() {
				e.log.Warnf("pck: %d forwarded connections are already open through this server; refusing more "+
					"until some close (further refusals for the next minute are not repeated)", maxServerStreams)
			}
			st.Write([]byte{statusBusy})
			return
		}
		defer release()
		dial, err := g.e.targets.check(g.ctx, p.Addr)
		if err != nil {
			g.e.noteRefused(s, p.Addr, err)
			st.Write([]byte{statusFor(err)})
			return
		}
		if p.Type == pTCP {
			g.serveTCP(st, dial)
		} else {
			g.serveUDP(s, st, dial)
		}
	}
}

// statusFor classifies a dial error into the byte the client is told.
func statusFor(err error) byte {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return statusTimeout
	case errors.Is(err, errBadTarget):
		return statusBadTarget
	case errors.Is(err, errForbiddenTarget):
		return statusForbidden
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		return statusBadTarget
	}
	if isUnreachable(err) {
		return statusUnreachable
	}
	return statusRefused
}

var errBadTarget = errors.New("bad target")

func (e *Engine) noteRefused(s *session, target Addr, err error) {
	if e.warnRefuse.ok() {
		e.log.Warnf("pck: could not reach %s for %s: %v (further failures for the next minute are not repeated)",
			target, s.remote, err)
	}
}

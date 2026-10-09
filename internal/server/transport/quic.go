package transport

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/backpack/backpack/internal/web"

	"github.com/quic-go/quic-go"
	"github.com/sirupsen/logrus"
)

// quicGen is the state of a single run of the transport: the context that ends
// when the run does, and the channels its goroutines pass work over. Restart
// builds a fresh set for the next run, so carrying them here keeps a goroutine
// that outlives its run from reaching into the run that replaced it.
type quicGen struct {
	ctx            context.Context
	tunnelChannel  chan net.Conn // ready data streams waiting for a local conn
	localChannel   chan LocalTCPConn
	reqNewConnChan chan struct{}
	usageMonitor   *web.Usage
	// bye is said once the seated client has been told this run is ending;
	// each client gets its own. See farewell.
	bye atomic.Pointer[farewell]
	// seat holds the client this generation serves; see clientSeat.
	seat clientSeat
	// client is the QUIC connection the seated client's control stream came
	// on. It carries all of that client's streams, so a data stream from any
	// other connection belongs to a client that is not seated.
	client atomic.Pointer[quic.Conn]
}

// quicClaim is a control stream that has proved the token, with the
// connection it came on.
type quicClaim struct {
	ctrl net.Conn
	conn *quic.Conn
}

// QuicTransport is the server side of the QUIC transport. One QUIC connection
// from the client carries everything: a control stream for the signalling, and
// a stream per forwarded flow. QUIC brings its own TLS 1.3, stream multiplexing,
// congestion control and loss recovery, so there is no smux, no FEC and no
// hand-tuning here — the protocol does what KCP needed a stack of settings for.
type QuicTransport struct {
	// The generations of this transport and what outlives them: see
	// lifecycle.go.
	lifecycle

	config       *QuicConfig
	quicSettings network.QUICSettings
	// The run's channels and its usage monitor are deliberately not fields:
	// they belong to one generation, and a field outlives the generation that
	// made it. See Start.
	controlChannel netControl
	limits         *limiter
}

type QuicConfig struct {
	BindAddr      string
	SnifferLog    string
	Token         string
	Ports         []string
	AcceptUDP     bool
	Sniffer       bool
	ChannelSize   int
	WebPort       int
	Heartbeat     time.Duration
	KeepAlive     time.Duration
	SO_RCVBUF     int
	SO_SNDBUF     int
	ProxyProtocol bool
	// MaxConnections caps simultaneous forwarded connections (0 = unlimited).
	MaxConnections int
	// BandwidthMbps caps total tunnel throughput (0 = unlimited).
	BandwidthMbps int
}

func (c *QuicConfig) settings() network.QUICSettings {
	return network.QUICSettings{
		KeepAlivePeriod: c.KeepAlive,
		MaxIdleTimeout:  quicIdleTimeout(c.KeepAlive),
		SO_RCVBUF:       c.SO_RCVBUF,
		SO_SNDBUF:       c.SO_SNDBUF,
	}
}

// quicIdleTimeout derives how long a connection may sit with no packets before
// QUIC tears it down. It has to comfortably exceed the keepalive, or a healthy
// but quiet tunnel would drop; a floor keeps it sane when keepalive is disabled.
func quicIdleTimeout(keepAlive time.Duration) time.Duration {
	if keepAlive <= 0 {
		return 30 * time.Second
	}
	return 3 * keepAlive
}

func NewQuicServer(parentCtx context.Context, config *QuicConfig, logger *logrus.Logger) *QuicTransport {
	server := &QuicTransport{
		config:       config,
		quicSettings: config.settings(),
		lifecycle: lifecycle{
			parentctx: parentCtx,
			logger:    logger,
			usage:     usageSpec{webPort: config.WebPort, snifferLog: config.SnifferLog, sniffer: config.Sniffer},
		},
		limits: newLimiter(Limits{MaxConnections: config.MaxConnections, BandwidthMbps: config.BandwidthMbps}),
	}

	// The first run is installed the same way every later one is, so there is
	// only one path that ever writes it.
	server.firstGeneration()

	return server
}

// Start brings up the first run, building its generation exactly the way
// Restart builds every later one.
//
// It used to take the first generation's channels from fields on the transport,
// and Restart never replaced those fields — it only built fresh channels for
// the new generation. So the first run's channels stayed reachable from the
// struct for the life of the process, and with them every stream still queued
// in them, none of which was ever closed. The same shape was measured on the
// plain TCP transport: with a pool of 64, 64 sockets were still open after the
// client had gone and the run had been torn down, and a forced GC did not
// release them — an unreachable connection is closed by its finalizer, but
// these were still reachable. Building the generation here leaves nothing
// behind to pin.
func (s *QuicTransport) Start() {
	s.start(s.newGen(s.run.context()))
}

// start runs one generation of the transport. Everything it needs is in g:
// nothing in here reaches back for a field that the next Restart is entitled to
// replace while this run is still using it.
func (s *QuicTransport) start(g *quicGen) {
	go sweepTunnelConns(g.ctx, g.tunnelChannel)
	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}
	s.status.set("Disconnected (QUIC)")

	// handshakeChannel is local to this run: the accept loop publishes the
	// control stream onto it, and channelHandshake below takes it. Keeping it off
	// the struct means a goroutine from an old run can never hand its control
	// stream to the run that replaced it.
	handshake := make(chan quicClaim)

	go s.tunnelListener(g, handshake)

	// Block until the control stream arrives (or the run ends).
	var first quicClaim
	select {
	case <-g.ctx.Done():
		return
	case first = <-handshake:
	}
	s.seatClient(g, first)
	s.serveGeneration(s.forwarder(g), func() { s.handleLoop(g) })
}

// seatClient makes claim this generation's client, in place of whoever was.
func (s *QuicTransport) seatClient(g *quicGen, claim quicClaim) {
	g.seat.sit(g.ctx,
		func() { s.vacate(g) },
		func() {
			g.bye.Store(newFarewell())
			g.client.Store(claim.conn)
			s.controlChannel.Set(claim.ctrl)
			s.seated(claim.ctrl.RemoteAddr().String())
			s.status.set("Connected (QUIC)")
			s.logger.Info("control channel successfully established.")
		},
		func(ctx context.Context, lost func()) {
			loop := s.control(g, ctx, lost)
			// Bind to this claim even if its goroutine starts after replacement.
			loop.link = controlwire.Net(claim.ctrl)
			loop.run()
		})
}

// vacate empties the seat: the client's connection is closed with a word to
// the peer — over QUIC a silent close is no close at all — and the data
// streams it opened that wait in the pool are dropped. The listener and the
// forwarded ports stay up for the next client.
func (s *QuicTransport) vacate(g *quicGen) {
	s.controlChannel.Close()
	s.controlChannel.Clear()
	if old := g.client.Swap(nil); old != nil {
		_ = old.CloseWithError(0, "replaced by a newer connection from the client")
	}
	drainTunnelConns(g.tunnelChannel)
	s.status.set("Disconnected (QUIC)")
	metrics.ClearPeer()
}

func (s *QuicTransport) Restart() {
	s.restart(s.controlChannel.Close, func(ctx context.Context) {
		s.controlChannel.Clear()
		go s.start(s.newGen(ctx))
	})
}

// newGen builds one generation's channels, usage monitor and farewell.
func (s *QuicTransport) newGen(ctx context.Context) *quicGen {
	g := &quicGen{
		ctx:            ctx,
		tunnelChannel:  make(chan net.Conn, s.config.ChannelSize),
		localChannel:   make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan: make(chan struct{}, s.config.ChannelSize),
		usageMonitor:   s.usageMonitor(ctx),
	}
	g.bye.Store(newFarewell())
	return g
}

// quicFarewellFlush is how long a goodbye is given to leave before what carries
// it is closed: SG_Closed before the connection, CONNECTION_CLOSE before the
// socket. Each is a write handed to a sender goroutine, not a write that has
// happened.
const quicFarewellFlush = 150 * time.Millisecond

// tunnelListener accepts QUIC connections for the whole run and hands each to
// handleConn, which sorts its streams into the control stream and data streams.
// The re-adopt decision lives on the control stream instead of here, because a
// connection has proved nothing until its control stream passes the token — a
// peer that has not is not a reason to disturb the running tunnel.
func (s *QuicTransport) tunnelListener(g *quicGen, handshake chan<- quicClaim) {
	// Counted while this goroutine holds a listener, so Start can wait for the
	// port rather than sleeping and hoping. See listeners.go.
	s.listeners.hold()
	defer s.listeners.release()

	// The tunnel's own port: retried rather than fatal. See bindfail.go.
	listener, ok := bindTunnelPort(g.ctx, s.logger, s.config.BindAddr, func() (*network.QUICListener, error) {
		return network.QUICListen(s.config.BindAddr, s.quicSettings)
	})
	if !ok {
		return
	}

	s.logger.Infof("server started successfully, listening on address: %s (QUIC)", listener.Addr().String())

	// Every connection this listener accepted is told the server is going
	// before the socket goes.
	//
	// Closing the listener closes the transport under it, and quic-go ends the
	// connections on a closed transport without a word to the peer. Over TCP a
	// closed socket is a FIN the client reads at once; over QUIC it was
	// silence, so a client whose server had merely restarted — a config edit,
	// an update, systemctl restart — sat on a dead connection until its control
	// deadline ran out: a minute and fifty-three seconds of outage, measured,
	// for a restart that took one. CloseWithError sends CONNECTION_CLOSE, which
	// the client reads as an error on the control stream and redials at once.
	var connsMu sync.Mutex
	conns := map[*quic.Conn]struct{}{}

	defer listener.Close()

	// goodbye runs on the way out, in this goroutine and before the deferred
	// Close — which is what releases the port and lets Start return and the
	// process exit. It used to run in a goroutine of its own, and lost that
	// race every time: Accept takes the context and returns the instant it is
	// cancelled, so the listener was gone before the goodbye had begun.
	goodbye := func() {
		// First the client's own goodbye, SG_Closed on the control stream,
		// which the control loop writes (controlLoop.farewell). It is the one the client acts on by
		// itself; CONNECTION_CLOSE below is the second chance.
		if s.controlChannel.IsSet() {
			g.bye.Load().wait(farewellWait)
		}
		connsMu.Lock()
		told := len(conns) > 0
		for conn := range conns {
			_ = conn.CloseWithError(0, "server stopping")
		}
		connsMu.Unlock()
		if told {
			time.Sleep(quicFarewellFlush)
		}
	}

	for {
		conn, err := listener.Accept(g.ctx)
		if err != nil {
			if g.ctx.Err() != nil {
				goodbye()
				return
			}
			s.logger.Debugf("failed to accept quic connection on %s: %v", listener.Addr().String(), err)
			continue
		}

		connsMu.Lock()
		conns[conn] = struct{}{}
		connsMu.Unlock()

		go func() {
			s.handleConn(g, conn, handshake)
			connsMu.Lock()
			delete(conns, conn)
			connsMu.Unlock()
		}()
	}
}

// handleConn accepts the streams of one QUIC connection and files each as the
// control stream or a data stream.
//
// QUIC's own handshake proves nothing about the tunnel token, so a connection
// is a stranger until one of its streams has presented it. Until then it may
// have only unauthenticatedStreams streams waiting on their announcement, and
// one that opens more than that before proving anything is closed. Without the
// bound, each stream held a goroutine for up to controlClaimTimeout, and QUIC
// lets a peer open 65,536 of them per connection.
func (s *QuicTransport) handleConn(g *quicGen, conn *quic.Conn, handshake chan<- quicClaim) {
	gate := &quicGate{pending: make(chan struct{}, unauthenticatedStreams)}
	for {
		stream, err := conn.AcceptStream(g.ctx)
		if err != nil {
			s.logger.Debugf("quic connection from %s closed: %v", conn.RemoteAddr(), err)
			return
		}
		reserved, ok := gate.admit()
		if !ok {
			s.logger.Warnf("closing the QUIC connection from %s: it opened more than %d streams "+
				"before presenting the tunnel token", conn.RemoteAddr(), unauthenticatedStreams)
			_ = conn.CloseWithError(0, "unauthenticated")
			return
		}
		go s.acceptStream(g, conn, stream, handshake, gate, reserved)
	}
}

// unauthenticatedStreams is how many streams a QUIC connection may have waiting
// on their announcement before it has presented the tunnel token once. A
// genuine client opens one — its control stream — and waits for the answer.
const unauthenticatedStreams = 8

// quicGate is one QUIC connection's standing: whether it has presented the
// token yet, and the streams it has waiting until it does.
type quicGate struct {
	authed  atomic.Bool
	pending chan struct{}
}

// admit reserves a place for one more stream's announcement on a connection
// that has not authenticated, and reports false when it has none left. An
// authenticated connection needs no place: reserved is false.
func (q *quicGate) admit() (reserved, ok bool) {
	if q.authed.Load() {
		return false, true
	}
	select {
	case q.pending <- struct{}{}:
		return true, true
	default:
		return false, false
	}
}

// settled gives the stream's place back once its announcement has been judged,
// and records whether it proved the token. Called exactly once per admitted
// stream; after the connection is authenticated there is nothing to give back.
func (q *quicGate) settled(authenticated bool, reserved bool) {
	if authenticated {
		q.authed.Store(true)
	}
	if reserved {
		<-q.pending
	}
}

// acceptStream completes the token handshake for one stream and routes it.
//
// The decision is made from the signal the peer sends, never from whether a
// control channel exists — a data stream that races in before the control
// stream is established just waits its turn on the channel.
func (s *QuicTransport) acceptStream(g *quicGen, conn *quic.Conn, stream *quic.Stream, handshake chan<- quicClaim, gate *quicGate, reserved bool) {
	wrapped := network.NewQUICStreamConn(stream, conn)
	// The stream's place among the connection's unauthenticated ones, if it
	// took one (see handleConn), is given back once its announcement has been
	// judged.
	judged := false
	judge := func(ok bool) {
		if !judged {
			judged = true
			gate.settled(ok, reserved)
		}
	}
	defer judge(false)

	if err := stream.SetReadDeadline(time.Now().Add(controlClaimTimeout)); err != nil {
		wrapped.Close()
		return
	}
	token, signal, err := utils.ReceiveBinaryTransportString(wrapped)
	if err != nil {
		s.logger.Debugf("no announcement from %s: %v", conn.RemoteAddr(), err)
		wrapped.Close()
		return
	}
	stream.SetReadDeadline(time.Time{})

	// A client from v1.8.2 on proves the token, bound to this connection's TLS
	// session; an older one sends the token itself. Both are accepted, so the
	// server can be upgraded first and its old clients keep working until they
	// are upgraded too. What a bound client never does is fall back to sending
	// the token — that would let anything between them force the old handshake
	// by dropping the new one. See network/quicbind.go.
	bound := network.QUICProofMatches(conn, s.config.Token, token)
	if !bound && !tokenMatches(token, s.config.Token) {
		s.logger.Warnf("invalid security token received from %s — telling it so, rather than "+
			"closing without a word, which reads to the client exactly like an old server", conn.RemoteAddr())
		// wrapped, not the bare stream: it is what every other read and write
		// on this path uses, and the refusal is just another write.
		refuseControl(wrapped, utils.RefusedBadToken)
		return
	}
	judge(true)

	switch signal {
	case utils.SG_Chan:
		// The control stream. The answer proves this server holds the token
		// too: to a bound client, the server's own proof; to an old client, the
		// token, which is what that client already sent in the clear.
		answer := s.config.Token
		if bound {
			proof, err := network.QUICServerProof(conn, s.config.Token)
			if err != nil {
				s.logger.Errorf("could not bind the answer to the QUIC session: %v", err)
				wrapped.Close()
				return
			}
			answer = proof
		}
		if err := utils.SendBinaryTransportStringWithin(wrapped, answer, utils.SG_Chan, 10*time.Second); err != nil {
			s.logger.Errorf("failed to send security token: %v", err)
			wrapped.Close()
			return
		}

		// A claim while the generation is serving is a client that re-dialed,
		// often before this side noticed its old connection was dead: it takes
		// the seat in place, and the ports and their users stay up. It used to
		// rebuild the whole run. See clientSeat.
		if g.seat.serving() {
			s.logger.Warn("a new control channel claim arrived; adopting the new client in place")
			s.seatClient(g, quicClaim{ctrl: wrapped, conn: conn})
			return
		}

		select {
		case handshake <- quicClaim{ctrl: wrapped, conn: conn}:
		default:
			s.logger.Warnf("control channel handshake already in progress, discarding duplicate")
			wrapped.Close()
		}

	case utils.SG_TCP:
		// A data stream is useless without a control channel to drive it.
		if !s.controlChannel.IsSet() {
			s.logger.Debugf("data stream from %s arrived before a control channel, discarding", conn.RemoteAddr())
			wrapped.Close()
			return
		}
		// Only the seated client's connection carries its data streams.
		if c := g.client.Load(); c != nil && c != conn {
			s.logger.Debugf("data stream from %s on a connection that is no longer the client's, discarding", conn.RemoteAddr())
			wrapped.Close()
			return
		}
		select {
		case g.tunnelChannel <- wrapped:
		default:
			s.logger.Warnf("tunnel channel is full, discarding data stream from %s", conn.RemoteAddr())
			wrapped.Close()
		}

	default:
		s.logger.Warnf("unexpected announcement signal %v from %s", signal, conn.RemoteAddr())
		wrapped.Close()
	}
}

// control is this generation's control loop, bound to the control channel
// the generation was started with. See controlLoop.
func (s *QuicTransport) control(g *quicGen, ctx context.Context, lost func()) controlLoop {
	return controlLoop{
		ctx:           ctx,
		link:          controlwire.Net(s.controlChannel.Get()),
		beat:          s.config.Heartbeat,
		requests:      g.reqNewConnChan,
		log:           s.logger,
		restart:       lost,
		farewellFlush: quicFarewellFlush,
		bye:           g.bye.Load(),
	}
}

// handleLoop pairs each accepted local connection with a data stream, asking the
// client to open one if the pool has run dry, then forwards between the two.
func (s *QuicTransport) handleLoop(g *quicGen) {
	for {
		select {
		case <-g.ctx.Done():
			return

		case localConn := <-g.localChannel:
			if expired(localConn) {
				drop(localConn, s.limits, s.logger)
				continue
			}

			// Ask the client to open a fresh stream so the pool stays topped
			// up; a warm one already waiting is taken straight off the channel.
			askStream := func() {
				select {
				case g.reqNewConnChan <- struct{}{}:
				default:
				}
			}
			askStream()

			pairing[net.Conn]{
				ctx: g.ctx, local: localConn, tunnel: g.tunnelChannel,
				limits: s.limits, log: s.logger, request: askStream,
				announce: func(st net.Conn, addr string) error {
					if err := st.SetWriteDeadline(time.Now().Add(pairingWait(localConn.timeCreated))); err != nil {
						return err
					}
					defer st.SetWriteDeadline(time.Time{})
					return utils.SendBinaryString(st, addr)
				},
				discard: func(st net.Conn) { st.Close() },
				relay: func(st net.Conn, local LocalTCPConn) {
					go func() {
						// Free the connection slot once the transfer ends, or
						// the limit would fill up permanently.
						defer s.limits.release()
						handlers.TCPConnectionHandler(g.ctx,
							s.config.ProxyProtocol && !isUDPFlow(local.conn),
							local.conn, metrics.CountedConn(st), s.logger,
							g.usageMonitor, localForwardPort(local.conn), s.config.Sniffer)
					}()
				},
			}.run()
		}
	}
}

// forwarder is this transport's forwarded ports for one generation.
func (s *QuicTransport) forwarder(g *quicGen) portForwarder {
	return portForwarder{
		ctx: g.ctx, ports: s.config.Ports, acceptUDP: s.config.AcceptUDP,
		queue: g.localChannel, limits: s.limits, listeners: &s.listeners, log: s.logger,
		tune:   alwaysNodelay(s.logger),
		queued: nil,
	}
}

package transport

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"time"

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
	// bye is said once the client has been told this run is ending. See
	// farewell.
	bye *farewell
}

// QuicTransport is the server side of the QUIC transport. One QUIC connection
// from the client carries everything: a control stream for the signalling, and
// a stream per forwarded flow. QUIC brings its own TLS 1.3, stream multiplexing,
// congestion control and loss recovery, so there is no smux, no FEC and no
// hand-tuning here — the protocol does what KCP needed a stack of settings for.
type QuicTransport struct {
	// The listeners this transport is holding right now. Start waits on it, so
	// "Start returned" means "the ports are free". See listeners.go.
	listeners listenerSet

	// The status shown in the panel. Behind a lock because the run being
	// replaced and the run replacing it both write it. See tunnelStatus.
	status       tunnelStatus
	config       *QuicConfig
	quicSettings network.QUICSettings
	parentctx    context.Context
	// The current run. Replaced by Restart while the previous run's
	// goroutines are still reading it, so it lives behind a lock.
	run    runState
	logger *logrus.Logger
	// The run's channels and its usage monitor are deliberately not fields:
	// they belong to one generation, and a field outlives the generation that
	// made it. See Start.
	controlChannel netControl
	restartMutex   sync.Mutex
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
	ctx, cancel := context.WithCancel(parentCtx)

	server := &QuicTransport{
		config:       config,
		quicSettings: config.settings(),
		parentctx:    parentCtx,
		logger:       logger,
		limits:       newLimiter(Limits{MaxConnections: config.MaxConnections, BandwidthMbps: config.BandwidthMbps}),
	}

	// The first run is installed the same way every later one is, so there is
	// only one path that ever writes it.
	server.run.set(ctx, cancel)

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
	ctx := s.run.context()
	s.start(&quicGen{
		ctx:            ctx,
		tunnelChannel:  make(chan net.Conn, s.config.ChannelSize),
		localChannel:   make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan: make(chan struct{}, s.config.ChannelSize),
		usageMonitor: web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx,
			s.config.SnifferLog, s.config.Sniffer, s.status.get, s.logger),
		bye: newFarewell(),
	})
}

// start runs one generation of the transport. Everything it needs is in g:
// nothing in here reaches back for a field that the next Restart is entitled to
// replace while this run is still using it.
func (s *QuicTransport) start(g *quicGen) {
	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}
	s.status.set("Disconnected (QUIC)")

	// handshakeChannel is local to this run: the accept loop publishes the
	// control stream onto it, and channelHandshake below takes it. Keeping it off
	// the struct means a goroutine from an old run can never hand its control
	// stream to the run that replaced it.
	handshake := make(chan net.Conn)

	go s.tunnelListener(g, handshake)

	// Block until the control stream arrives (or the run ends).
	select {
	case <-g.ctx.Done():
		return
	case conn := <-handshake:
		s.controlChannel.Set(conn)
		metrics.ReportPeer(conn.RemoteAddr().String())
		s.logger.Info("control channel successfully established.")
	}

	s.status.set("Connected (QUIC)")

	numCPU := runtime.NumCPU()
	if numCPU > 4 {
		numCPU = 4 // Max allowed handler is 4
	}

	go s.parsePortMappings(g)
	go s.channelHandler(g)

	s.logger.Infof("starting %d handle loops on each CPU thread", numCPU)
	for i := 0; i < numCPU; i++ {
		go s.handleLoop(g)
	}
}

func (s *QuicTransport) Restart() {
	if !s.restartMutex.TryLock() {
		s.logger.Warn("server restart already in progress, skipping restart attempt")
		return
	}
	defer s.restartMutex.Unlock()

	s.logger.Info("restarting server...")
	s.run.stop()

	// for removing timeout logs
	level := s.logger.GetLevel()
	s.logger.SetLevel(logrus.FatalLevel)

	if s.controlChannel.IsSet() {
		s.controlChannel.Close()
	}

	// Wait for the listeners rather than guessing at how long they take.
	//
	// This was a flat two-second sleep, and the comment next to it said what it
	// was for: the run being replaced still holds the ports, and binding them
	// again before it lets go fails. A sleep is a guess — usually long enough,
	// never a guarantee, and silently wrong on a loaded machine, which is
	// exactly when a restart is most likely to be happening.
	//
	// listenerSet answers the question instead of approximating it. It is also
	// faster in the ordinary case: a listener closes in microseconds, so this
	// returns at once rather than always costing two seconds.
	s.listeners.wait(s.parentctx)

	// The whole tunnel may have been shut down while this restart was waiting —
	// on a reload, or on the process going down. Rebuilding from a finished
	// parent context would bind the listener only to close it again.
	if s.parentctx.Err() != nil {
		// The level was turned down to hide the timeouts a teardown produces;
		// leaving it there would silence the shutdown itself.
		s.logger.SetLevel(level)
		// Abandoning is not a reason to keep claiming a peer.
		//
		// This branch used to return before the two lines below, which sit on
		// the path that carries on — so a restart that gave up left the status
		// reading "Connected" and left the peer published in the metrics
		// snapshot. The process usually exits straight afterwards and the
		// snapshot goes stale, which is why this was invisible; with a
		// transport fallback chain it is not, because the chain cancels a
		// candidate's context and the *process keeps running*. The snapshot
		// then carries a fresh timestamp and a connected peer for a tunnel that
		// is mid-rotation with nothing connected at all, and the watchdog
		// reads that and calls it healthy.
		//
		// The run is over. Whatever ended it, there is no peer.
		s.status.set("")
		metrics.ClearPeer()
		s.logger.Debug("restart abandoned: the tunnel is shutting down")
		return
	}

	ctx, cancel := context.WithCancel(s.parentctx)
	s.run.set(ctx, cancel)

	// The next run's state, built here and handed straight to start(). It used
	// to be written onto the transport for start() to read back, which is a
	// value published by one goroutine and read by another with nothing
	// ordering them — the same shape as the ctx/cancel race the detector caught
	// on kcp.go, and present on every one of these fields. Passing it removes
	// the shared field rather than locking it.
	g := &quicGen{
		ctx:            ctx,
		tunnelChannel:  make(chan net.Conn, s.config.ChannelSize),
		localChannel:   make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan: make(chan struct{}, s.config.ChannelSize),
		usageMonitor:   web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx, s.config.SnifferLog, s.config.Sniffer, s.status.get, s.logger),
		bye:            newFarewell(),
	}

	// Re-initialise the per-run state.
	s.controlChannel.Clear()
	// The peer is gone until a new control channel arrives; a stale address would
	// be shown as if it were current.
	metrics.ClearPeer()
	s.status.set("")

	s.logger.SetLevel(level)

	go s.start(g)
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
func (s *QuicTransport) tunnelListener(g *quicGen, handshake chan<- net.Conn) {
	// Counted while this goroutine holds a listener, so Start can wait for the
	// port rather than sleeping and hoping. See listeners.go.
	s.listeners.hold()
	defer s.listeners.release()

	// The tunnel's own port: retried rather than fatal. See bindfail.go.
	var backoff listenBackoff
	var listener *network.QUICListener
	for {
		var err error
		listener, err = network.QUICListen(s.config.BindAddr, s.quicSettings)
		if err == nil {
			break
		}
		s.logger.Error(bindFailure("tunnel port", s.config.BindAddr, err))
		if !backoff.wait(g.ctx) {
			return
		}
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
		// which channelHandler writes. It is the one the client acts on by
		// itself; CONNECTION_CLOSE below is the second chance.
		if s.controlChannel.IsSet() {
			g.bye.wait(farewellWait)
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
func (s *QuicTransport) handleConn(g *quicGen, conn *quic.Conn, handshake chan<- net.Conn) {
	for {
		stream, err := conn.AcceptStream(g.ctx)
		if err != nil {
			s.logger.Debugf("quic connection from %s closed: %v", conn.RemoteAddr(), err)
			return
		}
		go s.acceptStream(g, conn, stream, handshake)
	}
}

// acceptStream completes the token handshake for one stream and routes it.
//
// The decision is made from the signal the peer sends, never from whether a
// control channel exists — a data stream that races in before the control
// stream is established just waits its turn on the channel.
func (s *QuicTransport) acceptStream(g *quicGen, conn *quic.Conn, stream *quic.Stream, handshake chan<- net.Conn) {
	wrapped := network.NewQUICStreamConn(stream, conn)

	if err := stream.SetReadDeadline(time.Now().Add(controlClaimTimeout)); err != nil {
		stream.Close()
		return
	}
	token, signal, err := utils.ReceiveBinaryTransportString(wrapped)
	if err != nil {
		s.logger.Debugf("no announcement from %s: %v", conn.RemoteAddr(), err)
		stream.Close()
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
				stream.Close()
				return
			}
			answer = proof
		}
		if err := utils.SendBinaryTransportString(wrapped, answer, utils.SG_Chan); err != nil {
			s.logger.Errorf("failed to send security token: %v", err)
			stream.Close()
			return
		}

		// A control claim while one is already established means the client
		// restarted on its own and re-dialed, while this run never noticed
		// because the old connection has not idled out yet. Now that the token
		// has proved the claim genuine, adopt the new client by rebuilding the
		// run — the listener it is retrying against comes back up as part of that
		// restart. The same fix the udp and kcp transports carry.
		if s.controlChannel.IsSet() {
			s.logger.Warn("a new control channel claim arrived; restarting to adopt the new client")
			stream.Close()
			go s.Restart()
			return
		}

		select {
		case handshake <- wrapped:
		default:
			s.logger.Warnf("control channel handshake already in progress, discarding duplicate")
			stream.Close()
		}

	case utils.SG_TCP:
		// A data stream is useless without a control channel to drive it.
		if !s.controlChannel.IsSet() {
			s.logger.Debugf("data stream from %s arrived before a control channel, discarding", conn.RemoteAddr())
			stream.Close()
			return
		}
		select {
		case g.tunnelChannel <- wrapped:
		default:
			s.logger.Warnf("tunnel channel is full, discarding data stream from %s", conn.RemoteAddr())
			stream.Close()
		}

	default:
		s.logger.Warnf("unexpected announcement signal %v from %s", signal, conn.RemoteAddr())
		stream.Close()
	}
}

func (s *QuicTransport) channelHandler(g *quicGen) {
	// Every way out releases the listener, including the ones that never say
	// goodbye. See farewell.
	defer g.bye.said()

	ticker := newLivenessTicker(s.config.Heartbeat)
	defer ticker.Stop()

	messageChan := make(chan byte, 1)

	go func() {
		for {
			select {
			case <-g.ctx.Done():
				return
			default:
				message, err := utils.ReceiveBinaryByte(s.controlChannel.Get())
				if err != nil {
					// A generation that has already been cancelled must not ask for a
					// restart. It used to test s.cancel != nil, which the constructor
					// makes true before this code can run — so the guard was always
					// open, and every goroutine dying during a teardown queued another
					// restart of a tunnel that was on its way down. Asking the
					// generation's own context is both the real question and a read
					// nobody else writes: Restart replaces s.cancel while these
					// goroutines are still running, which is the data race the CI
					// detector caught on this line.
					if g.ctx.Err() == nil {
						s.logger.Error("failed to read from channel connection. ", err)
						go s.Restart()
					}
					return
				}
				messageChan <- message
			}
		}
	}()

	for {
		select {
		case <-g.ctx.Done():
			// The listener holds the connection open until this has left.
			if utils.SendBinaryByteWithin(s.controlChannel.Get(), utils.SG_Closed, controlWriteTimeout) == nil {
				time.Sleep(quicFarewellFlush)
			}
			return

		case <-g.reqNewConnChan:
			if err := utils.SendBinaryByteWithin(s.controlChannel.Get(), utils.SG_Chan, controlWriteTimeout); err != nil {
				s.logger.Error("failed to send request new connection signal. ", err)
				go s.Restart()
				return
			}

		case <-ticker.C:
			if err := utils.SendBinaryByteWithin(s.controlChannel.Get(), utils.SG_HB, controlWriteTimeout); err != nil {
				s.logger.Error("failed to send heartbeat signal")
				go s.Restart()
				return
			}
			s.logger.Trace("heartbeat signal sent successfully")

		case message, ok := <-messageChan:
			if !ok {
				s.logger.Error("channel closed, likely due to an error in the control channel read")
				return
			}
			if message == utils.SG_Closed {
				s.logger.Warn("control channel has been closed by the client")
				go s.Restart()
				return
			}
		}
	}
}

func (s *QuicTransport) parsePortMappings(g *quicGen) {
	for _, portMapping := range s.config.Ports {
		parts := strings.Split(portMapping, "=")
		// One unreadable mapping is one mapping, not a reason to end the
		// process. See the same passage in tcp.go.
		if len(parts) > 2 {
			s.logger.Errorf("ignoring the port mapping %q: it has more than one '='", portMapping)
			continue
		}

		// The left-hand side may name a local address as well as a port or a
		// range, so one machine can serve different exposed ports on different
		// local IPs. See expandListenSpec.
		listens, err := expandListenSpec(parts[0])
		if err != nil {
			s.logger.Errorf("ignoring the port mapping %q: %v", portMapping, err)
			continue
		}

		var remoteAddr string
		if len(parts) == 2 {
			remoteAddr = strings.TrimSpace(parts[1])
		}

		for _, l := range listens {
			// A mapping that named no destination forwards each port to itself.
			target := remoteAddr
			if target == "" {
				target = l.port
			}
			go s.localListener(g, l.addr, target)
			if len(listens) > 1 {
				time.Sleep(1 * time.Millisecond) // for wide port ranges
			}
		}
	}
}

func (s *QuicTransport) localListener(g *quicGen, localAddr string, remoteAddr string) {
	// Counted while this goroutine holds a listener, so Start can wait for the
	// port rather than sleeping and hoping. See listeners.go.
	s.listeners.hold()
	defer s.listeners.release()

	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		// One forwarded port, not the tunnel. See bindfail.go.
		s.logger.Error(bindFailure("forwarded port", localAddr, err))
		return
	}

	defer listener.Close()

	s.logger.Infof("listener started successfully, listening on address: %s", listener.Addr().String())

	go s.acceptLocalConn(g, listener, remoteAddr)
	// The same forwarded port, carrying datagrams. A flow is handed over as a
	// net.Conn, so from here down it is paired with a tunnel connection, piped,
	// counted and torn down by exactly the code that does it for TCP.
	if s.config.AcceptUDP {
		go startUDPForward(g.ctx, s.logger, localAddr, remoteAddr,
			udpAdmitter(g.ctx, g.localChannel, g.reqNewConnChan, s.limits))
	}

	<-g.ctx.Done()
}

func (s *QuicTransport) acceptLocalConn(g *quicGen, listener net.Listener, remoteAddr string) {
	var backoff acceptBackoff
	for {
		select {
		case <-g.ctx.Done():
			return

		default:
			conn, err := listener.Accept()
			if err != nil {
				s.logger.Debugf("failed to accept connection on %s: %v", listener.Addr(), err)
				// One of these runs per forwarded port, so an instant retry on a
				// broken listener would pin a core per port. See acceptBackoff.
				if !backoff.Fail(g.ctx) {
					return
				}
				continue
			}
			backoff.OK()

			tcpConn, ok := conn.(*net.TCPConn)
			if !ok {
				s.logger.Warnf("discarded non-TCP connection from %s", conn.RemoteAddr().String())
				conn.Close()
				continue
			}

			// Local hops are short and latency-sensitive, so Nagle stays off.
			if err := tcpConn.SetNoDelay(true); err != nil {
				s.logger.Warnf("failed to set TCP_NODELAY for %s: %v", tcpConn.RemoteAddr().String(), err)
			}

			// Enforce the tunnel's limits before the connection costs anything.
			if !s.limits.acquire() {
				s.logger.Warnf("connection limit reached, refusing %s", conn.RemoteAddr())
				conn.Close()
				continue
			}
			conn = s.limits.wrap(g.ctx, conn)

			select {
			case g.localChannel <- LocalTCPConn{conn: conn, remoteAddr: remoteAddr, timeCreated: time.Now().UnixMilli()}:
				s.logger.Debugf("forwarded port: accepted a client from %s", tcpConn.RemoteAddr().String())
			default: // channel is full, discard the connection
				s.logger.Warnf("forwarded port: the queue is full, dropping a client from %s", tcpConn.RemoteAddr().String())
				s.limits.release()
				conn.Close()
			}
		}
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

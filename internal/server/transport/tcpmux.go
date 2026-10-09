package transport

import (
	"context"
	"net"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/backpack/backpack/internal/web"

	"github.com/sirupsen/logrus"
	"github.com/xtaci/smux"
)

// tcpMuxGen is the state of a single run of the transport: the context that ends
// when the run does, and the channels its goroutines pass work over. Restart
// builds a fresh set for the next run, so carrying them here keeps a goroutine
// that outlives its run from reaching into the run that replaced it.
type tcpMuxGen struct {
	ctx              context.Context
	tunnelChannel    chan *smux.Session
	handshakeChannel chan controlCandidate
	localChannel     chan LocalTCPConn
	reqNewConnChan   chan struct{}
	usageMonitor     *web.Usage
	// seat holds the client this generation serves; see clientSeat.
	seat clientSeat
}

type TcpMuxTransport struct {
	// The generations of this transport and what outlives them: see
	// lifecycle.go.
	lifecycle

	config *TcpMuxConfig
	// muxV1/muxV2 are both built up front so that settling the version costs
	// nothing per connection; muxVersion says which one this run agreed on.
	muxV1          *smux.Config
	muxV2          *smux.Config
	muxVersion     atomic.Int32
	controlChannel netControl
	streamCounter  int32
	sessionCounter int32
	limits         *limiter
	// poolNonce is what this run's pool connections must present. It is empty
	// while no control channel is up, and stays empty for a legacy client that
	// cannot present one — which is what keeps the source-address fallback
	// reachable. See network.PoolNonce.
	poolNonce network.PoolNonce
}

type TcpMuxConfig struct {
	BindAddr         string
	SnifferLog       string
	Token            string
	Ports            []string
	AcceptUDP        bool
	Nodelay          bool
	Sniffer          bool
	ChannelSize      int
	MuxCon           int
	MuxVersion       int
	MaxFrameSize     int
	MaxReceiveBuffer int
	MaxStreamBuffer  int
	WebPort          int
	KeepAlive        time.Duration
	Heartbeat        time.Duration // in seconds
	MSS              int
	SO_RCVBUF        int
	SO_SNDBUF        int
	ProxyProtocol    bool
	// MaxConnections caps simultaneous forwarded connections (0 = unlimited).
	MaxConnections int
	// BandwidthMbps caps total tunnel throughput (0 = unlimited).
	BandwidthMbps int
}

// setMuxVersion records the version this run agreed on. A legacy client cannot
// be told one, so it falls back to whatever the file configured — which is what
// both ends did before there was anything to agree about.
func (s *TcpMuxTransport) setMuxVersion(negotiated int) {
	if negotiated != 1 && negotiated != 2 {
		negotiated = network.ResolveMuxVersion(s.config.MuxVersion)
		if s.config.MuxVersion == network.MuxVersionAuto {
			// Nothing configured and nothing negotiated: the peer predates the
			// handshake, so it can only be speaking version 1.
			negotiated = 1
		}
	}
	s.muxVersion.Store(int32(negotiated))
}

// smuxCfg returns the session configuration for the version this run settled
// on.
func (s *TcpMuxTransport) smuxCfg() *smux.Config {
	if s.muxVersion.Load() == 2 {
		return s.muxV2
	}
	return s.muxV1
}

func NewTcpMuxServer(parentCtx context.Context, config *TcpMuxConfig, logger *logrus.Logger) *TcpMuxTransport {
	muxSettings := network.MuxSettings{
		MaxFrameSize:     config.MaxFrameSize,
		MaxReceiveBuffer: config.MaxReceiveBuffer,
		MaxStreamBuffer:  config.MaxStreamBuffer,
	}
	server := &TcpMuxTransport{
		muxV1:  network.SmuxConfig(1, muxSettings),
		muxV2:  network.SmuxConfig(2, muxSettings),
		config: config,
		lifecycle: lifecycle{
			parentctx: parentCtx,
			logger:    logger,
			usage:     usageSpec{webPort: config.WebPort, snifferLog: config.SnifferLog, sniffer: config.Sniffer},
		},
		streamCounter:  0,
		sessionCounter: 0,
		limits:         newLimiter(Limits{MaxConnections: config.MaxConnections, BandwidthMbps: config.BandwidthMbps}),
	}

	// The first run is installed the same way every later one is, so there is
	// only one path that ever writes it.
	server.firstGeneration()

	return server
}

// Start brings up the first run. Every later one comes from Restart, which
// builds its own generation and hands it straight to start — so the fields read
// here are written once, by the constructor, before any other goroutine exists.
func (s *TcpMuxTransport) Start() {
	s.start(s.newGen(s.run.context()))
}

// start runs one generation of the transport. Everything it needs is in g:
// nothing in here reaches back for a field that the next Restart is entitled to
// replace while this run is still using it.
func (s *TcpMuxTransport) start(g *tcpMuxGen) {
	go sweepTunnelConns(g.ctx, g.tunnelChannel)
	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}
	s.status.set("Disconnected (TCPMux)")

	go s.tunnelPort(g).serve()
	go handshakeSweep(g.ctx, g.handshakeChannel)

	first, ok := s.channelHandshake(g)
	if !ok {
		return
	}
	s.seatClient(g, first)
	s.serveGeneration(s.forwarder(g), func() { s.handleLoop(g) })
}

// seatClient makes candidate this generation's client, in place of whoever was.
func (s *TcpMuxTransport) seatClient(g *tcpMuxGen, candidate controlCandidate) {
	g.seat.sit(g.ctx,
		func() { s.vacate(g) },
		func() {
			// The control channel carries one byte at a time; it is sent at
			// once rather than held back for more.
			if tcpConn, ok := candidate.conn.(*net.TCPConn); ok {
				if err := tcpConn.SetNoDelay(true); err != nil {
					s.logger.Warnf("failed to set TCP_NODELAY for Control Channel %s: %v", tcpConn.RemoteAddr().String(), err)
				}
			}
			// Order matters: the nonce has to be in place before the control
			// channel is, or a pool connection racing in behind the handshake
			// would be checked against a nonce that is not there yet.
			s.poolNonce.Set(candidate.nonce)
			s.setMuxVersion(candidate.muxVersion)
			s.controlChannel.Set(candidate.conn)
			// The engine says whether it holds a control channel; the watchdog
			// reads it rather than the socket table. See
			// metrics.Snapshot.Connected.
			s.seated(candidate.conn.RemoteAddr().String())
			if candidate.nonce == "" {
				s.logger.Warn(legacyPoolWarning)
			}
			s.status.set("Connected (TCPMux)")
			s.logger.Infof("control channel successfully established (mux version %d).", s.muxVersion.Load())
		},
		func(ctx context.Context, lost func()) {
			loop := s.control(g, ctx, lost)
			// Bind to this claim even if its goroutine starts after replacement.
			loop.link = controlwire.Net(candidate.conn)
			loop.run()
		})
}

// vacate empties the seat: the client's channel is closed and forgotten, and
// the mux sessions it opened that are waiting in the pool are dropped. The
// tunnel port and the forwarded ports stay up for the next client.
func (s *TcpMuxTransport) vacate(g *tcpMuxGen) {
	s.controlChannel.Close()
	s.controlChannel.Clear()
	s.poolNonce.Clear()
	drainTunnelConns(g.tunnelChannel)
	s.status.set("Disconnected (TCPMux)")
	metrics.ClearPeer()
}
func (s *TcpMuxTransport) Restart() {
	s.restart(s.controlChannel.Close, func(ctx context.Context) {
		s.controlChannel.Clear()
		// The next run issues its own nonce and settles its own mux version.
		s.poolNonce.Clear()
		s.muxVersion.Store(0)
		s.counterMutex.Lock()
		atomic.StoreInt32(&s.streamCounter, 0)
		atomic.StoreInt32(&s.sessionCounter, 0)
		s.counterMutex.Unlock()
		go s.start(s.newGen(ctx))
	})
}

// newGen builds one generation's channels and usage monitor.
func (s *TcpMuxTransport) newGen(ctx context.Context) *tcpMuxGen {
	return &tcpMuxGen{
		ctx:              ctx,
		tunnelChannel:    make(chan *smux.Session, s.config.ChannelSize),
		handshakeChannel: make(chan controlCandidate, 1),
		localChannel:     make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan:   make(chan struct{}, s.config.ChannelSize),
		usageMonitor:     s.usageMonitor(ctx),
	}
}

// channelHandshake waits for a connection that has already proved it holds the
// token and asked to be the control channel.
//
// The proving happens on the accept path, in the candidate's own goroutine —
// see announce.go — so this only has to publish the winner.
func (s *TcpMuxTransport) channelHandshake(g *tcpMuxGen) (controlCandidate, bool) {
	select {
	case <-g.ctx.Done():
		return controlCandidate{}, false
	case candidate := <-g.handshakeChannel:
		return candidate, true
	}
}

// control is this generation's control loop, bound to the control channel
// the generation was started with. See controlLoop.
func (s *TcpMuxTransport) control(g *tcpMuxGen, ctx context.Context, lost func()) controlLoop {
	return controlLoop{
		ctx:      ctx,
		link:     controlwire.Net(s.controlChannel.Get()),
		beat:     s.config.Heartbeat,
		requests: g.reqNewConnChan,
		log:      s.logger,
		restart:  lost,
	}
}

// tunnelPort is this generation's tunnel port; see tcpTunnelPort.
func (s *TcpMuxTransport) tunnelPort(g *tcpMuxGen) tcpTunnelPort {
	return tcpTunnelPort{
		ctx:       g.ctx,
		addr:      s.config.BindAddr,
		rcvBuf:    s.config.SO_RCVBUF,
		sndBuf:    s.config.SO_SNDBUF,
		mss:       s.config.MSS,
		keepAlive: s.config.KeepAlive,
		nodelay:   s.config.Nodelay,
		listeners: &s.listeners,
		log:       s.logger,
		preauth:   &s.preauth,
		admit:     func(c net.Conn) { s.admitTunnelConn(g, c) },
	}
}

// admitTunnelConn takes one accepted connection through whatever it has to pass
// before it can be used, and files it as a control channel or a mux session for
// the pool.
func (s *TcpMuxTransport) admitTunnelConn(g *tcpMuxGen, conn net.Conn) {
	// A legacy client says nothing on a pool connection — it dials and opens
	// mux streams when the server asks — so there is no announcement to read
	// and the only thing separating it from a stranger's connection is the
	// source address. Reading here would deadlock against such a client, so
	// this branch stays exactly as it was, and is reachable only once a legacy
	// control channel has been established (which is what leaves the nonce
	// empty).
	if s.controlChannel.IsSet() && s.poolNonce.Get() == "" {
		// Read the peer address once: checking "is it set" and then asking for
		// the address separately leaves a window where the control channel is
		// cleared in between and the address comes back nil. Comparing through
		// sameHost also handles IPv6 peers correctly.
		if peer := s.controlChannel.RemoteAddr(); peer != nil && !sameHost(peer, conn.RemoteAddr()) {
			s.logger.Debugf("suspicious packet from %v. expected address: %v. discarding packet...", conn.RemoteAddr(), peer)
			conn.Close()
			return
		}
		s.deliverTunnelConn(g, conn, "")
		return
	}

	ann, err := readAnnouncement(conn)
	if err != nil {
		s.logger.Debugf("no announcement from %s: %v", conn.RemoteAddr(), err)
		conn.Close()
		return
	}

	switch {
	case isControlSignal(ann.signal):
		s.admitControlChannel(g, conn, ann)

	case ann.signal == utils.SG_Pool:
		// The nonce is this run's, so a connection carrying a previous run's —
		// or none at all — is refused here rather than joining the pool.
		if !s.poolNonce.Verify(ann.payload) {
			s.logger.Warnf("pool connection from %s presented an invalid nonce, discarding", conn.RemoteAddr())
			conn.Close()
			return
		}
		s.preauth.Prove(conn.RemoteAddr())
		s.deliverTunnelConn(g, conn, ann.payload)

	default:
		s.logger.Warnf("unexpected announcement %d from %s, discarding", ann.signal, conn.RemoteAddr())
		conn.Close()
	}
}

// admitControlChannel verifies a peer claiming the control channel, answers it,
// and offers it as the candidate for channelHandshake to publish.
func (s *TcpMuxTransport) admitControlChannel(g *tcpMuxGen, conn net.Conn, ann announcement) {
	if !tokenMatches(ann.payload, s.config.Token) {
		s.logger.Warnf("invalid security token received from %s — telling it so, rather than "+
			"closing without a word, which reads to the client exactly like an old server", conn.RemoteAddr())
		refuseControl(conn, utils.RefusedBadToken)
		return
	}
	s.preauth.Prove(conn.RemoteAddr())
	if endedClaim(g.ctx, conn) {
		return
	}

	ack, nonce, muxVersion, err := controlAck(ann.signal, s.config.Token, network.ResolveMuxVersion(s.config.MuxVersion))
	if err != nil {
		s.logger.Errorf("could not answer the control handshake: %v", err)
		conn.Close()
		return
	}
	if err := utils.SendBinaryTransportStringWithin(conn, ack, ann.signal, 10*time.Second); err != nil {
		s.logger.Errorf("failed to send security token: %v", err)
		conn.Close()
		return
	}

	// A claim while the generation is serving is a client that re-dialed; now
	// that the token has proved it genuine it takes the seat in place, and the
	// ports and their users stay up. See the same passage in tcp.go.
	if g.seat.serving() {
		s.logger.Warn("a new control channel claim arrived; adopting the new client in place")
		s.seatClient(g, controlCandidate{conn: conn, nonce: nonce, muxVersion: muxVersion})
		return
	}

	s.logger.Info("control channel not found, attempting to establish a new session")
	select {
	case g.handshakeChannel <- controlCandidate{conn: conn, nonce: nonce, muxVersion: muxVersion}:
	default:
		s.logger.Warnf("control channel handshake in progress...")
		conn.Close()
	}
}

// deliverTunnelConn wraps an admitted connection in a mux session and hands it
// to the pool, dropping it if the pool is full.
func (s *TcpMuxTransport) deliverTunnelConn(g *tcpMuxGen, conn net.Conn, nonce string) {
	accepted := g.seat.admit(g.ctx, func(ctx context.Context) bool {
		if nonce != "" {
			if !s.controlChannel.IsSet() || !s.poolNonce.Verify(nonce) {
				return false
			}
		} else {
			peer := s.controlChannel.RemoteAddr()
			if s.poolNonce.Get() != "" || peer == nil || !sameHost(peer, conn.RemoteAddr()) {
				return false
			}
		}
		session, err := smux.Client(conn, s.smuxCfg())
		if err != nil {
			s.logger.Errorf("failed to create MUX session: %v", err)
			return false
		}
		select {
		case g.tunnelChannel <- session:
			// The client identity is captured before the session can reach a worker.
			go func() {
				select {
				case <-ctx.Done():
					session.Close()
				case <-session.CloseChan():
				}
			}()
			return true
		default:
			session.Close()
			return false
		}
	})
	if !accepted {
		conn.Close()
	}
}

func (s *TcpMuxTransport) handleLoop(g *tcpMuxGen) {
	for {
		select {
		case <-g.ctx.Done():
			return

		case session := <-g.tunnelChannel:
			// +1 for session counter
			if !s.countGeneration(g.ctx, &s.sessionCounter, 1) {
				session.Close()
				continue
			}

			loop := s.session(g)
			go loop.run(session)
		}
	}
}

// session binds this transport's channels, counters and settings to the shared
// loop. It is the whole of what is transport-specific about running a session.
// Its context is the seated client's, so the session ends with the client that
// opened it rather than with the generation — which now outlives its clients.
func (s *TcpMuxTransport) session(g *tcpMuxGen) muxSession {
	return muxSession{
		ctx:           g.seat.client(),
		local:         g.localChannel,
		usage:         g.usageMonitor,
		reqNewConn:    g.reqNewConnChan,
		muxCon:        s.config.MuxCon,
		proxyProtocol: s.config.ProxyProtocol,
		sniffer:       s.config.Sniffer,
		limits:        s.limits,
		log:           s.logger,
		streams:       &s.streamCounter,
		sessions:      &s.sessionCounter,
		count: func(counter *int32, delta int32) {
			s.countGeneration(g.ctx, counter, delta)
		},
	}
}

// forwarder is this transport's forwarded ports for one generation.
func (s *TcpMuxTransport) forwarder(g *tcpMuxGen) portForwarder {
	return portForwarder{
		ctx: g.ctx, ports: s.config.Ports, acceptUDP: s.config.AcceptUDP,
		queue: g.localChannel, limits: s.limits, listeners: &s.listeners, log: s.logger,
		tune:   nodelayTune(s.config.Nodelay, s.logger),
		queued: muxRequestForGeneration(g.ctx, &s.counterMutex, &s.streamCounter, &s.sessionCounter, s.config.MuxCon, g.reqNewConnChan, s.logger),
	}
}

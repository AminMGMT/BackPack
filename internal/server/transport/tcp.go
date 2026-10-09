package transport

import (
	"context"
	"net"
	"time"

	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/backpack/backpack/internal/web"

	"github.com/sirupsen/logrus"
)

// tcpGen is the state of a single run of the transport: the context that ends
// when the run does, and the channels its goroutines pass work over. Restart
// builds a fresh set for the next run, so carrying them here keeps a goroutine
// that outlives its run from reaching into the run that replaced it.
type tcpGen struct {
	ctx              context.Context
	tunnelChannel    chan net.Conn
	localChannel     chan LocalTCPConn
	reqNewConnChan   chan struct{}
	handshakeChannel chan controlCandidate
	usageMonitor     *web.Usage
	// seat holds the client this generation serves; a new one replaces it
	// without the generation ending. See clientSeat.
	seat clientSeat
}

type TcpTransport struct {
	// The generations of this transport and what outlives them: see
	// lifecycle.go.
	lifecycle

	config *TcpConfig
	// The run's channels and its usage monitor are deliberately not fields:
	// they belong to one generation, and a field outlives the generation that
	// made it. See Start.
	controlChannel netControl
	limits         *limiter
	// poolNonce is what this run's pool connections must present. It is empty
	// while no control channel is up, and stays empty for a legacy client that
	// cannot present one — which is what keeps the source-address fallback
	// reachable. See network.PoolNonce.
	poolNonce network.PoolNonce
}

type TcpConfig struct {
	BindAddr      string
	Token         string
	SnifferLog    string
	Ports         []string
	Nodelay       bool
	Sniffer       bool
	KeepAlive     time.Duration
	Heartbeat     time.Duration // in seconds
	ChannelSize   int
	WebPort       int
	AcceptUDP     bool
	MSS           int
	SO_RCVBUF     int
	SO_SNDBUF     int
	ProxyProtocol bool
	// MaxConnections caps simultaneous forwarded connections (0 = unlimited).
	MaxConnections int
	// BandwidthMbps caps total tunnel throughput (0 = unlimited).
	BandwidthMbps int
	// Stealth wraps every accepted tunnel connection in the Noise record layer,
	// so the stream has no fingerprint for deep packet inspection to match.
	Stealth bool
}

func NewTCPServer(parentCtx context.Context, config *TcpConfig, logger *logrus.Logger) *TcpTransport {
	// Initialize the TcpTransport struct
	server := &TcpTransport{
		config: config,
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
// struct for the life of the process, and with them every connection still
// queued in them, none of which was ever closed. Measured with a pool of 64:
// 64 sockets still open after the client had gone and the run had been torn
// down, and a forced GC did not release them — an unreachable net.Conn is
// closed by its finalizer, but these were still reachable. Building the
// generation here leaves nothing behind to pin.
func (s *TcpTransport) Start() {
	s.start(s.newGen(s.run.context()))
}

// start runs one generation of the transport. Everything it needs is in g:
// nothing in here reaches back for a field that the next Restart is entitled to
// replace while this run is still using it.
func (s *TcpTransport) start(g *tcpGen) {
	go sweepTunnelConns(g.ctx, g.tunnelChannel)
	s.status.set("Disconnected (TCP)")

	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}

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
func (s *TcpTransport) seatClient(g *tcpGen, candidate controlCandidate) {
	g.seat.sit(g.ctx,
		func() { s.vacate(g) },
		func() {
			// Order matters: the nonce has to be in place before the control
			// channel is, or a pool connection racing in behind the handshake
			// would be checked against a nonce that is not there yet.
			s.poolNonce.Set(candidate.nonce)
			s.controlChannel.Set(candidate.conn)
			// The engine says whether it holds a control channel; the watchdog
			// reads it rather than the socket table, which shows a socket long
			// after the tunnel behind it has stopped working. See
			// metrics.Snapshot.Connected.
			s.seated(candidate.conn.RemoteAddr().String())
			if candidate.nonce == "" {
				s.logger.Warn(legacyPoolWarning)
			}
			s.status.set("Connected (TCP)")
			s.logger.Info("control channel successfully established.")
		},
		func(ctx context.Context, lost func()) {
			loop := s.control(g, ctx, lost)
			// Bind to this claim even if its goroutine starts after replacement.
			loop.link = controlwire.Net(candidate.conn)
			loop.run()
		})
}

// vacate empties the seat: the client's channel is closed and forgotten, and
// the pool connections it opened are dropped — they lead to a client that has
// gone. The tunnel port and the forwarded ports stay up for the next one.
func (s *TcpTransport) vacate(g *tcpGen) {
	s.controlChannel.Close()
	s.controlChannel.Clear()
	s.poolNonce.Clear()
	drainTunnelConns(g.tunnelChannel)
	s.status.set("Disconnected (TCP)")
	metrics.ClearPeer()
}
func (s *TcpTransport) Restart() {
	s.restart(s.controlChannel.Close, func(ctx context.Context) {
		s.controlChannel.Clear()
		// The next run issues its own nonce, so connections still carrying this
		// one must stop being accepted the moment the run ends.
		s.poolNonce.Clear()
		go s.start(s.newGen(ctx))
	})
}

// newGen builds one generation's channels and usage monitor.
func (s *TcpTransport) newGen(ctx context.Context) *tcpGen {
	return &tcpGen{
		ctx:              ctx,
		tunnelChannel:    make(chan net.Conn, s.config.ChannelSize),
		localChannel:     make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan:   make(chan struct{}, s.config.ChannelSize),
		handshakeChannel: make(chan controlCandidate, 1),
		usageMonitor:     s.usageMonitor(ctx),
	}
}

// channelHandshake waits for a connection that has already proved it holds the
// token and asked to be the control channel.
//
// The proving happens on the accept path, in the candidate's own goroutine —
// see announce.go — so this only has to publish the winner.
func (s *TcpTransport) channelHandshake(g *tcpGen) (controlCandidate, bool) {
	select {
	case <-g.ctx.Done():
		return controlCandidate{}, false
	case candidate := <-g.handshakeChannel:
		return candidate, true
	}
}

// control is this generation's control loop, bound to the control channel
// the generation was started with. See controlLoop.
func (s *TcpTransport) control(g *tcpGen, ctx context.Context, lost func()) controlLoop {
	return controlLoop{
		ctx:      ctx,
		link:     controlwire.Net(s.controlChannel.Get()),
		beat:     s.config.Heartbeat,
		requests: g.reqNewConnChan,
		log:      s.logger,
		restart:  lost,
		probeRTT: true,
	}
}

// tunnelPort is this generation's tunnel port; see tcpTunnelPort.
func (s *TcpTransport) tunnelPort(g *tcpGen) tcpTunnelPort {
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
// before it can be used, and files it as a control channel or a pool
// connection.
func (s *TcpTransport) admitTunnelConn(g *tcpGen, raw net.Conn) {
	conn := raw

	// In stealth mode the Noise handshake is completed first, so everything
	// after it — the announcement, the control channel, the data conns — reads
	// and writes through the encrypted record layer without knowing it is
	// there. A peer without the token fails here and gets no further.
	if s.config.Stealth {
		wrapped, err := network.NoiseServerConn(raw, s.config.Token, 15*time.Second)
		if err != nil {
			s.logger.Debugf("stealth handshake failed from %s: %v", raw.RemoteAddr(), err)
			raw.Close()
			return
		}
		conn = wrapped
		// The Noise handshake is keyed on the token: completing it proves it.
		s.preauth.Prove(raw.RemoteAddr())
	}

	// A legacy client says nothing on a pool connection — it dials and waits
	// for the server to name a destination — so there is no announcement to
	// read and the only thing separating it from a stranger's connection is
	// the source address. Reading here would deadlock against such a client,
	// so this branch stays exactly as it was, and is reachable only once a
	// legacy control channel has been established (which is what leaves the
	// nonce empty).
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
func (s *TcpTransport) admitControlChannel(g *tcpGen, conn net.Conn, ann announcement) {
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

	// Plain TCP carries no mux sessions, so there is no version to settle: 0
	// tells the client there is nothing to apply.
	ack, nonce, _, err := controlAck(ann.signal, s.config.Token, 0)
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

	// A control claim while the generation is serving means the client
	// restarted on its own and re-dialed, often while this side had not yet
	// noticed its old channel was dead. Now that the token has proved the
	// claim genuine, the new client takes the seat in place (see clientSeat):
	// the tunnel port, the forwarded ports and the users on them stay up.
	// This used to rebuild the whole run, cutting every user connection on
	// every re-dial.
	//
	// This used to refuse the claim with RefusedInUse and keep the old channel,
	// which is only right when there really are two clients. The far more
	// common case is one client whose path died silently: nothing on this side
	// reads the control channel with a deadline, and a heartbeat written into a
	// dead-but-unreset socket lands in the send buffer and reports success — so
	// the server can hold a channel that has been gone for a long time. The
	// client, which does keep a read deadline, notices in seconds and re-dials
	// into a refusal it can do nothing about. The operator sees "the server
	// already has a control channel from somebody else" for a tunnel that has
	// exactly one client, and the tunnel stays down until somebody restarts the
	// service by hand.
	//
	// The token is what makes this safe to do: a peer that cannot present it
	// gets no further than the check above, so this cannot be used to knock a
	// tunnel over from outside.
	if g.seat.serving() {
		s.logger.Warn("a new control channel claim arrived; adopting the new client in place")
		s.seatClient(g, controlCandidate{conn: conn, nonce: nonce})
		return
	}

	select {
	case g.handshakeChannel <- controlCandidate{conn: conn, nonce: nonce}:
	default:
		// channelHandshake has not begun reading in this run yet: a genuine
		// duplicate racing the first claim, rather than a re-dial.
		s.logger.Warn("control channel handshake already in progress, discarding duplicate")
		conn.Close()
	}
}

// deliverTunnelConn hands an admitted connection to the pool, dropping it if
// the pool is full.
func (s *TcpTransport) deliverTunnelConn(g *tcpGen, conn net.Conn, nonce string) {
	accepted := g.seat.admit(g.ctx, func(context.Context) bool {
		if !s.validPoolIdentity(conn, nonce) {
			return false
		}
		select {
		case g.tunnelChannel <- conn:
			return true
		default:
			return false
		}
	})
	if !accepted {
		conn.Close()
	}
}

func (s *TcpTransport) validPoolIdentity(conn net.Conn, nonce string) bool {
	if nonce != "" {
		return s.controlChannel.IsSet() && s.poolNonce.Verify(nonce)
	}
	peer := s.controlChannel.RemoteAddr()
	return s.poolNonce.Get() == "" && peer != nil && sameHost(peer, conn.RemoteAddr())
}

func (s *TcpTransport) handleLoop(g *tcpGen) {
	for {
		select {
		case <-g.ctx.Done():
			return
		case localConn := <-g.localChannel:
			// A connection that sat in the queue past its deadline is not worth
			// giving a fresh timer to.
			if expired(localConn) {
				drop(localConn, s.limits, s.logger)
				continue
			}
			pairing[net.Conn]{
				ctx: g.ctx, local: localConn, tunnel: g.tunnelChannel,
				limits: s.limits, log: s.logger,
				announce: func(c net.Conn, addr string) error {
					return utils.SendBinaryTransportStringWithin(c, addr, utils.SG_TCP, pairingWait(localConn.timeCreated))
				},
				request: requestAlways(g.reqNewConnChan, s.logger),
				discard: func(c net.Conn) { c.Close() },
				relay: func(c net.Conn, local LocalTCPConn) {
					go func() {
						// Free the connection slot once the transfer ends, or
						// the limit would fill up permanently.
						defer s.limits.release()
						handlers.TCPConnectionHandler(g.ctx,
							s.config.ProxyProtocol && !isUDPFlow(local.conn),
							local.conn, metrics.CountedConn(c), s.logger,
							g.usageMonitor, localForwardPort(local.conn), s.config.Sniffer)
					}()
				},
			}.run()
		}
	}
}

// forwarder is this transport's forwarded ports for one generation.
func (s *TcpTransport) forwarder(g *tcpGen) portForwarder {
	return portForwarder{
		ctx: g.ctx, ports: s.config.Ports, acceptUDP: s.config.AcceptUDP,
		queue: g.localChannel, limits: s.limits, listeners: &s.listeners, log: s.logger,
		tune:   nodelayTune(s.config.Nodelay, s.logger),
		queued: requestAlways(g.reqNewConnChan, s.logger),
	}
}

package transport

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/backpack/backpack/internal/web"

	"github.com/sirupsen/logrus"
	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
)

// kcpGen is the state of a single run of the transport: the context that ends
// when the run does, and the channels its goroutines pass work over. Restart
// builds a fresh set for the next run, so carrying them here keeps a goroutine
// that outlives its run from reaching into the run that replaced it.
type kcpGen struct {
	ctx              context.Context
	tunnelChannel    chan *smux.Session
	handshakeChannel chan net.Conn
	localChannel     chan LocalTCPConn
	reqNewConnChan   chan struct{}
	usageMonitor     *web.Usage
	// bye is said once the seated client has been told this run is ending;
	// each client gets its own, so the goodbye waited for at shutdown is the
	// current client's. See farewell.
	bye atomic.Pointer[farewell]
	// seat holds the client this generation serves; see clientSeat.
	seat clientSeat
}

// KcpTransport is the server side of the KCP transport: a reliable,
// retransmitting protocol carried inside UDP datagrams, with SMUX layered on
// top so many streams share one session.
//
// Compared to the TCP transports this one keeps working on links where TCP
// stalls — heavy packet loss, aggressive throttling of long-lived TCP flows,
// or a path where the return route is asymmetric. Forward error correction
// repairs losses without waiting a full round trip for a retransmit.
type KcpTransport struct {
	// The generations of this transport and what outlives them: see
	// lifecycle.go.
	lifecycle

	config         *KcpConfig
	smuxConfig     *smux.Config
	kcpSettings    network.KCPSettings
	controlChannel netControl
	streamCounter  int32
	sessionCounter int32
	limits         *limiter
}

type KcpConfig struct {
	BindAddr         string
	SnifferLog       string
	Token            string
	Ports            []string
	AcceptUDP        bool
	Sniffer          bool
	ChannelSize      int
	MuxCon           int
	MuxVersion       int
	MaxFrameSize     int
	MaxReceiveBuffer int
	MaxStreamBuffer  int
	WebPort          int
	Heartbeat        time.Duration
	SO_RCVBUF        int
	SO_SNDBUF        int
	ProxyProtocol    bool
	// MaxConnections caps simultaneous forwarded connections (0 = unlimited).
	MaxConnections int
	// BandwidthMbps caps total tunnel throughput (0 = unlimited).
	BandwidthMbps int

	// KCP tuning, filled from the tunnel's performance preset.
	MTU          int
	Interval     int
	Resend       int
	NoDelay      int
	NoCongestion int
	SndWnd       int
	RcvWnd       int
	AckNoDelay   bool
	DataShards   int
	ParityShards int
	// UseICMP carries the session inside ICMP echo (the xdi transport) rather
	// than UDP. Only the packet layer differs; everything here is unchanged.
	UseICMP bool
	// UsePck carries the session inside TCP segments built and read through a
	// packet socket (the pck transport). Only the packet layer differs; nothing
	// is forged. See settings().
	UsePck        bool
	PckInterface  string
	PckGatewayMAC string
	PckFlags      []string
}

// transportLabel is what the panel and logs call this transport — XDI when it
// rides in ICMP echo, SPOOF when it rides in forged raw IP, KCP when it rides in
// UDP. They are the same protocol above the packet layer.
func (s *KcpTransport) transportLabel() string {
	if s.config.UseICMP {
		return "XDI"
	}
	if s.config.UsePck {
		return "PCK"
	}
	return "KCP"
}

func (c *KcpConfig) settings() network.KCPSettings {
	s := network.KCPSettings{
		MTU:          c.MTU,
		Interval:     c.Interval,
		Resend:       c.Resend,
		NoDelay:      c.NoDelay,
		NoCongestion: c.NoCongestion,
		SndWnd:       c.SndWnd,
		RcvWnd:       c.RcvWnd,
		AckNoDelay:   c.AckNoDelay,
		DataShards:   c.DataShards,
		ParityShards: c.ParityShards,
		SO_RCVBUF:    c.SO_RCVBUF,
		SO_SNDBUF:    c.SO_SNDBUF,
		UseICMP:      c.UseICMP,
	}
	if c.UsePck {
		// The flag cycle is validated at load time (checkPck); an unparseable
		// one here falls back to the default rather than killing the tunnel.
		flags, err := network.ParseTCPFlagList(c.PckFlags)
		if err != nil {
			flags, _ = network.ParseTCPFlagList(nil)
		}
		s.Pck = &network.PcapCarrier{
			Interface:  c.PckInterface,
			GatewayMAC: c.PckGatewayMAC,
			Flags:      flags,
		}
	}
	return s
}

func NewKcpServer(parentCtx context.Context, config *KcpConfig, logger *logrus.Logger) *KcpTransport {
	server := &KcpTransport{
		smuxConfig: &smux.Config{
			Version:           network.ResolveStaticMuxVersion(config.MuxVersion),
			KeepAliveInterval: 20 * time.Second,
			KeepAliveTimeout:  40 * time.Second,
			MaxFrameSize:      config.MaxFrameSize,
			MaxReceiveBuffer:  config.MaxReceiveBuffer,
			MaxStreamBuffer:   config.MaxStreamBuffer,
		},
		config:      config,
		kcpSettings: config.settings(),
		lifecycle: lifecycle{
			parentctx: parentCtx,
			logger:    logger,
			usage:     usageSpec{webPort: config.WebPort, snifferLog: config.SnifferLog, sniffer: config.Sniffer},
		},
		limits: newLimiter(Limits{MaxConnections: config.MaxConnections, BandwidthMbps: config.BandwidthMbps}),
	}
	// Route the carrier's startup diagnostics (effective FEC/MTU, and for pck the
	// discovered egress and RST-guard status) into the tunnel log, so a tunnel
	// that never connects says why instead of staying silent.
	server.kcpSettings.Logf = logger.Infof

	// The first run is installed the same way every later one is, so there is
	// only one path that ever writes it.
	server.firstGeneration()

	return server
}

// Start brings up the first run. Every later one comes from Restart, which
// builds its own generation and hands it straight to start — so the fields read
// here are written once, by the constructor, before any other goroutine exists.
func (s *KcpTransport) Start() {
	s.start(s.newGen(s.run.context()))
}

// start runs one generation of the transport. Everything it needs is in g:
// nothing in here reaches back for a field that the next Restart is entitled to
// replace while this run is still using it.
func (s *KcpTransport) start(g *kcpGen) {
	go sweepTunnelConns(g.ctx, g.tunnelChannel)
	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}
	s.status.set("Disconnected (" + s.transportLabel() + ")")

	go s.tunnelListener(g)

	first, ok := s.channelHandshake(g)
	if !ok {
		return
	}
	s.seatClient(g, first)
	s.serveGeneration(s.forwarder(g), func() { s.handleLoop(g) })
}

// seatClient makes control this generation's client, in place of whoever was.
func (s *KcpTransport) seatClient(g *kcpGen, control net.Conn) {
	g.seat.sit(g.ctx,
		func() { s.vacate(g) },
		func() {
			g.bye.Store(newFarewell())
			s.controlChannel.Set(control)
			// A KCP listener is one unconnected socket, so the socket table can
			// never say who is on the other end. Recording it here is what lets
			// the panel show the peer's ping and location for a KCP tunnel
			// instead of leaving them blank.
			s.seated(control.RemoteAddr().String())
			s.status.set("Connected (" + s.transportLabel() + ")")
			s.logger.Info("control channel successfully established.")
		},
		func(ctx context.Context, lost func()) {
			loop := s.control(g, ctx, lost)
			// Bind to this claim even if its goroutine starts after replacement.
			loop.link = controlwire.Net(control)
			loop.run()
		})
}

// vacate empties the seat: the client's session is closed and forgotten, and
// the mux sessions it opened that wait in the pool are dropped. The listener
// and the forwarded ports stay up for the next client.
func (s *KcpTransport) vacate(g *kcpGen) {
	s.controlChannel.Close()
	s.controlChannel.Clear()
	drainTunnelConns(g.tunnelChannel)
	s.status.set("Disconnected (" + s.transportLabel() + ")")
	metrics.ClearPeer()
}

func (s *KcpTransport) Restart() {
	s.restart(s.controlChannel.Close, func(ctx context.Context) {
		s.controlChannel.Clear()
		s.counterMutex.Lock()
		atomic.StoreInt32(&s.streamCounter, 0)
		atomic.StoreInt32(&s.sessionCounter, 0)
		s.counterMutex.Unlock()
		go s.start(s.newGen(ctx))
	})
}

// newGen builds one generation's channels and usage monitor.
func (s *KcpTransport) newGen(ctx context.Context) *kcpGen {
	g := &kcpGen{
		ctx:              ctx,
		tunnelChannel:    make(chan *smux.Session, s.config.ChannelSize),
		handshakeChannel: make(chan net.Conn),
		localChannel:     make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan:   make(chan struct{}, s.config.ChannelSize),
		usageMonitor:     s.usageMonitor(ctx),
	}
	g.bye.Store(newFarewell())
	return g
}

// channelHandshake waits for a session that has already proved it holds the
// token and asked to be the control channel.
func (s *KcpTransport) channelHandshake(g *kcpGen) (net.Conn, bool) {
	select {
	case <-g.ctx.Done():
		return nil, false
	case conn := <-g.handshakeChannel:
		return conn, true
	}
}

// kcpFarewellFlush is how long the goodbye is given to leave before the
// session is closed: many KCP update intervals, which is when a queued segment
// is sent, and still far below anything an operator would notice in a stop.
// 150ms lost the goodbye about one stop in six under the race detector, which
// is what a heavily loaded machine looks like; 300ms has not.
const kcpFarewellFlush = 300 * time.Millisecond

// control is this generation's control loop, bound to the control channel
// the generation was started with. See controlLoop.
func (s *KcpTransport) control(g *kcpGen, ctx context.Context, lost func()) controlLoop {
	return controlLoop{
		ctx:           ctx,
		link:          controlwire.Net(s.controlChannel.Get()),
		beat:          s.config.Heartbeat,
		requests:      g.reqNewConnChan,
		log:           s.logger,
		restart:       lost,
		farewellFlush: kcpFarewellFlush,
		bye:           g.bye.Load(),
	}
}

func (s *KcpTransport) tunnelListener(g *kcpGen) {
	// Counted while this goroutine holds a listener, so Start can wait for the
	// port rather than sleeping and hoping. See listeners.go.
	s.listeners.hold()
	defer s.listeners.release()

	// The tunnel's own port: retried rather than fatal. See bindfail.go.
	var carrier io.Closer
	listener, ok := bindTunnelPort(g.ctx, s.logger, s.config.BindAddr, func() (*kcp.Listener, error) {
		l, c, err := network.KCPListen(s.config.BindAddr, s.config.Token, s.kcpSettings)
		carrier = c
		return l, err
	})
	if !ok {
		return
	}

	// Both are closed on the way out. Closing the listener alone leaves the
	// carrier's raw socket bound — the leak that made a restart fail to bind —
	// so the carrier is closed too; for plain UDP it is a no-op. The carrier
	// goes last so nothing is still reading the socket when it is pulled.
	defer carrier.Close()
	defer listener.Close()

	if s.config.DataShards > 0 {
		s.logger.Infof("server started successfully, listening on address: %s (KCP, FEC %d:%d)",
			listener.Addr().String(), s.config.DataShards, s.config.ParityShards)
	} else {
		s.logger.Infof("server started successfully, listening on address: %s (KCP, FEC off)",
			listener.Addr().String())
	}

	go s.acceptTunnelConn(g, listener)

	<-g.ctx.Done()
	// The socket stays open until the client has been told. See farewell.
	if s.controlChannel.IsSet() {
		g.bye.Load().wait(farewellWait)
	}
}

func (s *KcpTransport) acceptTunnelConn(g *kcpGen, listener *kcp.Listener) {
	var backoff acceptBackoff
	for {
		select {
		case <-g.ctx.Done():
			return
		default:
			session, err := listener.AcceptKCP()
			if err != nil {
				s.logger.Debugf("failed to accept tunnel connection on %s: %v", listener.Addr(), err)
				// Back off rather than retry instantly: a closed listener fails
				// immediately and forever, and `continue` would pin a core.
				if !backoff.Fail(g.ctx) {
					return
				}
				continue
			}
			backoff.OK()

			// Sessions used to be dropped unless they came from the same
			// address as the control channel. That was already redundant here —
			// a KCP session has to decrypt under a key derived from the token
			// and then announce itself with the token again, in acceptSession
			// below, so it proves what it knows before it is filed anywhere —
			// and it cost every client that dials out from more than one
			// address, behind carrier-grade NAT or a SNAT pool or on a
			// multi-homed host, its entire pool. The control channel came up,
			// every data session was discarded, and the tunnel carried nothing.
			network.ApplyKCPSettings(session, s.kcpSettings)

			// Every session announces what it is, and the announcement is read
			// off the accept path so that a peer which never sends one cannot
			// stall the sessions queued behind it.
			go s.acceptSession(g, session)
		}
	}
}

// acceptSession completes the handshake for one incoming KCP session and files
// it as either the control channel or a pool connection.
//
// The decision is made from the signal the peer sends, never from whether a
// control channel currently exists. That distinction matters after a server
// restart: the client still has pool connections in flight, and routing those
// into the control-channel handshake — which expects a different signal — made
// the server reject them forever while it waited for a control channel that
// the client had no reason to re-open.
func (s *KcpTransport) acceptSession(g *kcpGen, session *kcp.UDPSession) {
	if err := session.SetReadDeadline(time.Now().Add(controlClaimTimeout)); err != nil {
		session.Close()
		return
	}
	token, signal, err := utils.ReceiveBinaryTransportString(session)
	if err != nil {
		s.logger.Debugf("no announcement from %s: %v", session.RemoteAddr(), err)
		session.Close()
		return
	}
	session.SetReadDeadline(time.Time{})

	if !tokenMatches(token, s.config.Token) {
		s.logger.Warnf("invalid security token received from %s — telling it so, rather than "+
			"closing without a word, which reads to the client exactly like an old server", session.RemoteAddr())
		refuseControl(session, utils.RefusedBadToken)
		return
	}

	switch signal {
	case utils.SG_Chan:
		// A claim reaching a generation that has ended is not answered: a KCP
		// close tells the peer nothing, so an answered one would leave the
		// client connected to nobody until its keepalive ran out.
		if endedClaim(g.ctx, session) {
			return
		}
		// A peer claiming the control channel. Answering with the token is what
		// proves to the client that this server knows the secret too.
		if err := utils.SendBinaryTransportStringWithin(session, s.config.Token, utils.SG_Chan, 10*time.Second); err != nil {
			s.logger.Errorf("failed to send security token: %v", err)
			session.Close()
			return
		}
		// The control channel carries small, latency-critical signals.
		session.SetACKNoDelay(true)
		// Between heartbeats it idles like a pool session (kcpidle.go).
		control := network.IdleAwareKCP(session, s.kcpSettings, true)

		// A claim while the generation is serving is a client that re-dialed,
		// often before this side noticed its old session was dead: it takes the
		// seat in place, and the ports and their users stay up. It used to be
		// told the server was restarting for it, and the whole run was rebuilt.
		if g.seat.serving() {
			s.logger.Warn("a new control channel claim arrived; adopting the new client in place")
			s.seatClient(g, control)
			return
		}
		select {
		case g.handshakeChannel <- control: // ok
		default:
			// channelHandshake has not begun reading in this run yet: a genuine
			// duplicate racing the first claim, rather than a re-dial.
			s.logger.Warnf("control channel handshake already in progress, discarding duplicate")
			control.Close()
		}

	case utils.SG_TCP:
		// A data connection is useless without a control channel to drive it.
		if !s.controlChannel.IsSet() {
			s.logger.Debugf("tunnel connection from %s arrived before a control channel, discarding",
				session.RemoteAddr())
			session.Close()
			return
		}
		// From here on the session is closed through conn, so that the idle
		// governor lets go of it.
		conn := network.IdleAwareKCP(session, s.kcpSettings, s.kcpSettings.AckNoDelay)
		muxSession, err := smux.Client(conn, s.smuxConfig)
		if err != nil {
			s.logger.Errorf("failed to create MUX session for connection %s: %v", session.RemoteAddr(), err)
			conn.Close()
			return
		}
		select {
		case g.tunnelChannel <- muxSession: // ok
		default:
			s.logger.Warnf("tunnel listener channel is full, discarding KCP session from %s", session.RemoteAddr())
			muxSession.Close()
		}

	default:
		s.logger.Warnf("unexpected announcement signal %v from %s", signal, session.RemoteAddr())
		session.Close()
	}
}

func (s *KcpTransport) handleLoop(g *kcpGen) {
	for {
		select {
		case <-g.ctx.Done():
			return

		case session := <-g.tunnelChannel:
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
func (s *KcpTransport) session(g *kcpGen) muxSession {
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
func (s *KcpTransport) forwarder(g *kcpGen) portForwarder {
	return portForwarder{
		ctx: g.ctx, ports: s.config.Ports, acceptUDP: s.config.AcceptUDP,
		queue: g.localChannel, limits: s.limits, listeners: &s.listeners, log: s.logger,
		tune:   alwaysNodelay(s.logger),
		queued: muxRequestForGeneration(g.ctx, &s.counterMutex, &s.streamCounter, &s.sessionCounter, s.config.MuxCon, g.reqNewConnChan, s.logger),
	}
}

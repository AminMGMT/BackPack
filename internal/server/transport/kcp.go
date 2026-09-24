package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	// bye is said once the client has been told this run is ending. See
	// farewell.
	bye *farewell
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
	// The listeners this transport is holding right now. Start waits on it, so
	// "Start returned" means "the ports are free". See listeners.go.
	listeners listenerSet

	// The status shown in the panel. Behind a lock because the run being
	// replaced and the run replacing it both write it. See tunnelStatus.
	status      tunnelStatus
	config      *KcpConfig
	smuxConfig  *smux.Config
	kcpSettings network.KCPSettings
	parentctx   context.Context
	// The current run. Replaced by Restart while the previous run's
	// goroutines are still reading it, so it lives behind a lock.
	run              runState
	logger           *logrus.Logger
	tunnelChannel    chan *smux.Session
	handshakeChannel chan net.Conn
	localChannel     chan LocalTCPConn
	reqNewConnChan   chan struct{}
	controlChannel   netControl
	usageMonitor     *web.Usage
	restartMutex     sync.Mutex
	streamCounter    int32
	sessionCounter   int32
	limits           *limiter
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
	ctx, cancel := context.WithCancel(parentCtx)

	server := &KcpTransport{
		smuxConfig: &smux.Config{
			Version:           network.ResolveStaticMuxVersion(config.MuxVersion),
			KeepAliveInterval: 20 * time.Second,
			KeepAliveTimeout:  40 * time.Second,
			MaxFrameSize:      config.MaxFrameSize,
			MaxReceiveBuffer:  config.MaxReceiveBuffer,
			MaxStreamBuffer:   config.MaxStreamBuffer,
		},
		config:           config,
		kcpSettings:      config.settings(),
		parentctx:        parentCtx,
		logger:           logger,
		tunnelChannel:    make(chan *smux.Session, config.ChannelSize),
		handshakeChannel: make(chan net.Conn),
		localChannel:     make(chan LocalTCPConn, config.ChannelSize),
		reqNewConnChan:   make(chan struct{}, config.ChannelSize),
		limits:           newLimiter(Limits{MaxConnections: config.MaxConnections, BandwidthMbps: config.BandwidthMbps}),
	}
	// Route the carrier's startup diagnostics (effective FEC/MTU, and for pck the
	// discovered egress and RST-guard status) into the tunnel log, so a tunnel
	// that never connects says why instead of staying silent.
	server.kcpSettings.Logf = logger.Infof
	// Built after the transport exists, because it needs a getter for the
	// status rather than a pointer into it.
	server.usageMonitor = web.NewDataStore(fmt.Sprintf(":%v", config.WebPort), ctx, config.SnifferLog, config.Sniffer, server.status.get, logger)

	// The first run is installed the same way every later one is, so there is
	// only one path that ever writes it.
	server.run.set(ctx, cancel)

	return server
}

// Start brings up the first run. Every later one comes from Restart, which
// builds its own generation and hands it straight to start — so the fields read
// here are written once, by the constructor, before any other goroutine exists.
func (s *KcpTransport) Start() {
	s.start(&kcpGen{
		ctx:              s.run.context(),
		tunnelChannel:    s.tunnelChannel,
		handshakeChannel: s.handshakeChannel,
		localChannel:     s.localChannel,
		reqNewConnChan:   s.reqNewConnChan,
		usageMonitor:     s.usageMonitor,
		bye:              newFarewell(),
	})
}

// start runs one generation of the transport. Everything it needs is in g:
// nothing in here reaches back for a field that the next Restart is entitled to
// replace while this run is still using it.
func (s *KcpTransport) start(g *kcpGen) {
	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}
	s.status.set("Disconnected (" + s.transportLabel() + ")")

	go s.tunnelListener(g)

	s.channelHandshake(g)

	if s.controlChannel.IsSet() {
		s.status.set("Connected (" + s.transportLabel() + ")")

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
}

func (s *KcpTransport) Restart() {
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
	// on a reload, or on the process going down. Rebuilding the run from a
	// parent context that is already finished would bind the listeners again
	// only to close them, and on a reload that means fighting the run that is
	// replacing this one for its own ports. Nothing here is worth starting.
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
	g := &kcpGen{
		ctx:              ctx,
		tunnelChannel:    make(chan *smux.Session, s.config.ChannelSize),
		handshakeChannel: make(chan net.Conn),
		localChannel:     make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan:   make(chan struct{}, s.config.ChannelSize),
		usageMonitor:     web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx, s.config.SnifferLog, s.config.Sniffer, s.status.get, s.logger),
		bye:              newFarewell(),
	}

	// Re-initialize variables
	s.controlChannel.Clear()
	// The peer is gone until a new control channel arrives; a stale address
	// would be shown as if it were current.
	metrics.ClearPeer()
	s.status.set("")
	// Stored atomically, like every other access: the goroutines of the run
	// being replaced may still be counting while this resets them.
	atomic.StoreInt32(&s.streamCounter, 0)
	atomic.StoreInt32(&s.sessionCounter, 0)

	s.logger.SetLevel(level)

	go s.start(g)
}

// channelHandshake waits for a session that has already proved it holds the
// token and asked to be the control channel.
func (s *KcpTransport) channelHandshake(g *kcpGen) {
	for {
		select {
		case <-g.ctx.Done():
			return
		case conn := <-g.handshakeChannel:
			s.controlChannel.Set(conn)
			// A KCP listener is one unconnected socket, so the socket table can
			// never say who is on the other end. Recording it here is what lets
			// the panel show the peer's ping and location for a KCP tunnel
			// instead of leaving them blank.
			metrics.ReportPeer(conn.RemoteAddr().String())
			s.logger.Info("control channel successfully established.")
			return
		}
	}
}

// kcpFarewellFlush is how long the goodbye is given to leave before the
// session is closed: many KCP update intervals, which is when a queued segment
// is sent, and still far below anything an operator would notice in a stop.
// 150ms lost the goodbye about one stop in six under the race detector, which
// is what a heavily loaded machine looks like; 300ms has not.
const kcpFarewellFlush = 300 * time.Millisecond

func (s *KcpTransport) channelHandler(g *kcpGen) {
	// Every way out of here releases the listener, including the ones that
	// never say goodbye.
	defer g.bye.said()

	ticker := newLivenessTicker(s.config.Heartbeat)
	defer ticker.Stop()

	messageChan := make(chan byte, 1)

	go func() {
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
	}()

	for {
		select {
		case <-g.ctx.Done():
			// Written here while the listener is still holding the socket for
			// it: SG_Closed is what lets the client redial at once instead of
			// waiting out its deadline. See farewell.
			//
			// Then a moment before the close. A KCP write only hands the
			// segment to the session's sender goroutine, and kcp-go's Close
			// marks the session dead before its final flush — so a close
			// straight after the write drops the very segment it was meant to
			// flush. That was measured, not guessed: with the close right
			// behind the write the client still waited out its full deadline.
			if control := s.controlChannel.Get(); control != nil {
				if utils.SendBinaryByteWithin(control, utils.SG_Closed, controlWriteTimeout) == nil {
					time.Sleep(kcpFarewellFlush)
				}
				_ = control.Close()
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

func (s *KcpTransport) tunnelListener(g *kcpGen) {
	// Counted while this goroutine holds a listener, so Start can wait for the
	// port rather than sleeping and hoping. See listeners.go.
	s.listeners.hold()
	defer s.listeners.release()

	// The tunnel's own port: retried rather than fatal. See bindfail.go.
	var backoff listenBackoff
	var listener *kcp.Listener
	var carrier io.Closer
	for {
		var err error
		listener, carrier, err = network.KCPListen(s.config.BindAddr, s.config.Token, s.kcpSettings)
		if err == nil {
			break
		}
		s.logger.Error(bindFailure("tunnel port", s.config.BindAddr, err))
		if !backoff.wait(g.ctx) {
			return
		}
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
		g.bye.wait(farewellWait)
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
		// A control claim while one is already established means the client
		// restarted on its own and re-dialed, while this run never noticed
		// because the old session has not failed a read yet. channelHandshake
		// only reads one claim per run, so without this the re-dial would be
		// discarded, leaving the tunnel dead until the server was restarted by
		// hand. Restart to adopt the new client.
		//
		// Decided before answering. Answering the claim as
		// granted and then dropping it is, over KCP, a drop the client never
		// hears: it believed itself connected and waited out its whole control
		// deadline (116 seconds after a crash, measured). So it is told the
		// server is restarting for it, and claims again once that is done.
		if s.controlChannel.IsSet() {
			s.logger.Warn("a new control channel claim arrived; restarting to adopt the new client")
			if utils.SendBinaryTransportString(session, utils.RefusedRestarting, utils.SG_Refused) == nil {
				time.Sleep(kcpFarewellFlush) // see kcpFarewellFlush: a close straight after drops it
			}
			session.Close()
			go s.Restart()
			return
		}
		// A peer claiming the control channel. Answering with the token is what
		// proves to the client that this server knows the secret too.
		if err := utils.SendBinaryTransportString(session, s.config.Token, utils.SG_Chan); err != nil {
			s.logger.Errorf("failed to send security token: %v", err)
			session.Close()
			return
		}
		// The control channel carries small, latency-critical signals.
		session.SetACKNoDelay(true)
		// Between heartbeats it idles like a pool session (kcpidle.go).
		control := network.IdleAwareKCP(session, s.kcpSettings, true)

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

func (s *KcpTransport) parsePortMappings(g *kcpGen) {
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

func (s *KcpTransport) localListener(g *kcpGen, localAddr string, remoteAddr string) {
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

func (s *KcpTransport) acceptLocalConn(g *kcpGen, listener net.Listener, remoteAddr string) {
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

			// discard any non-tcp connection
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

			// Enforce the tunnel's limits before the connection costs anything:
			// a refused connection should be refused here, not after it has
			// taken a slot in the pool.
			if !s.limits.acquire() {
				s.logger.Warnf("connection limit reached, refusing %s", conn.RemoteAddr())
				conn.Close()
				continue
			}
			conn = s.limits.wrap(g.ctx, conn)

			select {
			case g.localChannel <- LocalTCPConn{conn: conn, remoteAddr: remoteAddr, timeCreated: time.Now().UnixMilli()}:
				s.logger.Debugf("forwarded port: accepted a client from %s", tcpConn.RemoteAddr().String())

				atomic.AddInt32(&s.streamCounter, 1)

				if atomic.LoadInt32(&s.streamCounter) >= atomic.LoadInt32(&s.sessionCounter)*int32(s.config.MuxCon) {
					s.logger.Tracef("stream counter: %v, session counter: %v", atomic.LoadInt32(&s.streamCounter), atomic.LoadInt32(&s.sessionCounter))

					select { // Attempt to request a new connection
					case g.reqNewConnChan <- struct{}{}:
					default:
						s.logger.Warn("failed to request new connection. channel is full")
					}
				}

			default: // channel is full, discard the connection
				s.logger.Warnf("forwarded port: the queue is full, dropping a client from %s", tcpConn.RemoteAddr().String())
				s.limits.release()
				conn.Close()
			}
		}
	}
}

func (s *KcpTransport) handleLoop(g *kcpGen) {
	for {
		select {
		case <-g.ctx.Done():
			return

		case session := <-g.tunnelChannel:
			atomic.AddInt32(&s.sessionCounter, 1)

			go s.handleSession(g, session)
		}
	}
}

// handleSession carries connections over one session. The state machine is
// muxSession's, shared with the other two mux transports — see muxsession.go.
func (s *KcpTransport) handleSession(g *kcpGen, session *smux.Session) {
	s.session(g).run(session)
}

// session binds this transport's channels, counters and settings to the shared
// loop. It is the whole of what is transport-specific about running a session.
func (s *KcpTransport) session(g *kcpGen) muxSession {
	return muxSession{
		ctx:           g.ctx,
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
	}
}

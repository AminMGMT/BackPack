package transport

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
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
	config           *KcpConfig
	smuxConfig       *smux.Config
	kcpSettings      network.KCPSettings
	parentctx        context.Context
	ctx              context.Context
	cancel           context.CancelFunc
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
	TunnelStatus     string
	SnifferLog       string
	Token            string
	Ports            []string
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
	// UseSpoof carries the session inside raw IPv4 packets with a forged source
	// (the spoof transport). Only the packet layer differs. See spoofSettings.
	UseSpoof       bool
	SpoofProfile   string
	SpoofSrcIP     string
	SpoofInterface string
}

// transportLabel is what the panel and logs call this transport — XDI when it
// rides in ICMP echo, SPOOF when it rides in forged raw IP, KCP when it rides in
// UDP. They are the same protocol above the packet layer.
func (s *KcpTransport) transportLabel() string {
	if s.config.UseICMP {
		return "XDI"
	}
	if s.config.UseSpoof {
		return "SPOOF"
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
	if c.UseSpoof {
		// Profile is validated at load time (checkSpoof); default to udp here.
		profile, _ := network.ParseSpoofProfile(c.SpoofProfile)
		s.Spoof = &network.SpoofCarrier{
			Profile:   profile,
			SrcIP:     c.SpoofSrcIP,
			Interface: c.SpoofInterface,
		}
	}
	return s
}

func NewKcpServer(parentCtx context.Context, config *KcpConfig, logger *logrus.Logger) *KcpTransport {
	ctx, cancel := context.WithCancel(parentCtx)

	return &KcpTransport{
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
		ctx:              ctx,
		cancel:           cancel,
		logger:           logger,
		tunnelChannel:    make(chan *smux.Session, config.ChannelSize),
		handshakeChannel: make(chan net.Conn),
		localChannel:     make(chan LocalTCPConn, config.ChannelSize),
		reqNewConnChan:   make(chan struct{}, config.ChannelSize),
		usageMonitor:     web.NewDataStore(fmt.Sprintf(":%v", config.WebPort), ctx, config.SnifferLog, config.Sniffer, &config.TunnelStatus, logger),
		limits:           newLimiter(Limits{MaxConnections: config.MaxConnections, BandwidthMbps: config.BandwidthMbps}),
	}
}

func (s *KcpTransport) Start() {
	// The state of this run. Restart replaces these fields for the next
	// one, so they are read once here and carried from goroutine to
	// goroutine; a goroutine that outlives its run keeps what it started
	// with instead of reading whatever the next run has installed.
	g := &kcpGen{
		ctx:              s.ctx,
		tunnelChannel:    s.tunnelChannel,
		handshakeChannel: s.handshakeChannel,
		localChannel:     s.localChannel,
		reqNewConnChan:   s.reqNewConnChan,
		usageMonitor:     s.usageMonitor,
	}

	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}
	s.config.TunnelStatus = "Disconnected (" + s.transportLabel() + ")"

	go s.tunnelListener(g)

	s.channelHandshake(g)

	if s.controlChannel.IsSet() {
		s.config.TunnelStatus = "Connected (" + s.transportLabel() + ")"

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
	if s.cancel != nil {
		s.cancel()
	}

	// for removing timeout logs
	level := s.logger.Level
	s.logger.SetLevel(logrus.FatalLevel)

	if s.controlChannel.IsSet() {
		s.controlChannel.Close()
	}

	time.Sleep(2 * time.Second)

	// The whole tunnel may have been shut down while this restart was waiting —
	// on a reload, or on the process going down. Rebuilding the run from a
	// parent context that is already finished would bind the listeners again
	// only to close them, and on a reload that means fighting the run that is
	// replacing this one for its own ports. Nothing here is worth starting.
	if s.parentctx.Err() != nil {
		// The level was turned down to hide the timeouts a teardown produces;
		// leaving it there would silence the shutdown itself.
		s.logger.SetLevel(level)
		s.logger.Debug("restart abandoned: the tunnel is shutting down")
		return
	}

	ctx, cancel := context.WithCancel(s.parentctx)
	s.ctx = ctx
	s.cancel = cancel

	// Re-initialize variables
	s.tunnelChannel = make(chan *smux.Session, s.config.ChannelSize)
	s.localChannel = make(chan LocalTCPConn, s.config.ChannelSize)
	s.reqNewConnChan = make(chan struct{}, s.config.ChannelSize)
	s.handshakeChannel = make(chan net.Conn)
	s.controlChannel.Clear()
	// The peer is gone until a new control channel arrives; a stale address
	// would be shown as if it were current.
	metrics.ClearPeer()
	s.usageMonitor = web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx, s.config.SnifferLog, s.config.Sniffer, &s.config.TunnelStatus, s.logger)
	s.config.TunnelStatus = ""
	// Stored atomically, like every other access: the goroutines of the run
	// being replaced may still be counting while this resets them.
	atomic.StoreInt32(&s.streamCounter, 0)
	atomic.StoreInt32(&s.sessionCounter, 0)

	s.logger.SetLevel(level)

	go s.Start()
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

func (s *KcpTransport) channelHandler(g *kcpGen) {
	ticker := time.NewTicker(s.config.Heartbeat)
	defer ticker.Stop()

	messageChan := make(chan byte, 1)

	go func() {
		message, err := utils.ReceiveBinaryByte(s.controlChannel.Get())
		if err != nil {
			if s.cancel != nil {
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
			_ = utils.SendBinaryByte(s.controlChannel.Get(), utils.SG_Closed)
			return

		case <-g.reqNewConnChan:
			if err := utils.SendBinaryByte(s.controlChannel.Get(), utils.SG_Chan); err != nil {
				s.logger.Error("failed to send request new connection signal. ", err)
				go s.Restart()
				return
			}

		case <-ticker.C:
			if err := utils.SendBinaryByte(s.controlChannel.Get(), utils.SG_HB); err != nil {
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
	listener, err := network.KCPListen(s.config.BindAddr, s.config.Token, s.kcpSettings)
	if err != nil {
		s.logger.Fatalf("failed to start listener on %s: %v", s.config.BindAddr, err)
		return
	}

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
}

func (s *KcpTransport) acceptTunnelConn(g *kcpGen, listener *kcp.Listener) {
	for {
		select {
		case <-g.ctx.Done():
			return
		default:
			s.logger.Debugf("waiting for accept incoming tunnel connection on %s", listener.Addr().String())
			session, err := listener.AcceptKCP()
			if err != nil {
				s.logger.Debugf("failed to accept tunnel connection on %s: %v", listener.Addr().String(), err)
				continue
			}

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
	if err := session.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
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

	if token != s.config.Token {
		s.logger.Warnf("invalid security token received from %s", session.RemoteAddr())
		session.Close()
		return
	}

	switch signal {
	case utils.SG_Chan:
		// A peer claiming the control channel. Answering with the token is what
		// proves to the client that this server knows the secret too.
		if err := utils.SendBinaryTransportString(session, s.config.Token, utils.SG_Chan); err != nil {
			s.logger.Errorf("failed to send security token: %v", err)
			session.Close()
			return
		}
		// The control channel carries small, latency-critical signals.
		session.SetACKNoDelay(true)

		// A control claim while one is already established means the client
		// restarted on its own and re-dialed, while this run never noticed
		// because the old session has not failed a read yet. channelHandshake
		// only reads one claim per run, so without this the re-dial would fall
		// through to the default below and be discarded, leaving the tunnel dead
		// until the server was restarted by hand. Restart to adopt the new
		// client — the listener it is retrying against comes back up as part of
		// that restart.
		if s.controlChannel.IsSet() {
			s.logger.Warn("a new control channel claim arrived; restarting to adopt the new client")
			session.Close()
			go s.Restart()
			return
		}

		select {
		case g.handshakeChannel <- session: // ok
		default:
			// channelHandshake has not begun reading in this run yet: a genuine
			// duplicate racing the first claim, rather than a re-dial.
			s.logger.Warnf("control channel handshake already in progress, discarding duplicate")
			session.Close()
		}

	case utils.SG_TCP:
		// A data connection is useless without a control channel to drive it.
		if !s.controlChannel.IsSet() {
			s.logger.Debugf("tunnel connection from %s arrived before a control channel, discarding",
				session.RemoteAddr())
			session.Close()
			return
		}
		muxSession, err := smux.Client(session, s.smuxConfig)
		if err != nil {
			s.logger.Errorf("failed to create MUX session for connection %s: %v", session.RemoteAddr(), err)
			session.Close()
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

		var localAddr, remoteAddr string

		// Check if only a single port or a port range is provided (no "=" present)
		if len(parts) == 1 {
			localPortOrRange := strings.TrimSpace(parts[0])
			remoteAddr = localPortOrRange

			if strings.Contains(localPortOrRange, "-") {
				rangeParts := strings.Split(localPortOrRange, "-")
				if len(rangeParts) != 2 {
					s.logger.Fatalf("invalid port range format: %s", localPortOrRange)
				}

				startPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
				if err != nil || startPort < 1 || startPort > 65535 {
					s.logger.Fatalf("invalid start port in range: %s", rangeParts[0])
				}

				endPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
				if err != nil || endPort < 1 || endPort > 65535 || endPort < startPort {
					s.logger.Fatalf("invalid end port in range: %s", rangeParts[1])
				}

				for port := startPort; port <= endPort; port++ {
					localAddr = fmt.Sprintf(":%d", port)
					go s.localListener(g, localAddr, strconv.Itoa(port))
					time.Sleep(1 * time.Millisecond) // for wide port ranges
				}
				continue
			}

			port, err := strconv.Atoi(localPortOrRange)
			if err != nil || port < 1 || port > 65535 {
				s.logger.Fatalf("invalid port format: %s", localPortOrRange)
			}
			localAddr = fmt.Sprintf(":%d", port)
		} else if len(parts) == 2 {
			localPortOrRange := strings.TrimSpace(parts[0])
			remoteAddr = strings.TrimSpace(parts[1])

			if strings.Contains(localPortOrRange, "-") {
				rangeParts := strings.Split(localPortOrRange, "-")
				if len(rangeParts) != 2 {
					s.logger.Fatalf("invalid port range format: %s", localPortOrRange)
				}

				startPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
				if err != nil || startPort < 1 || startPort > 65535 {
					s.logger.Fatalf("invalid start port in range: %s", rangeParts[0])
				}

				endPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
				if err != nil || endPort < 1 || endPort > 65535 || endPort < startPort {
					s.logger.Fatalf("invalid end port in range: %s", rangeParts[1])
				}

				for port := startPort; port <= endPort; port++ {
					localAddr = fmt.Sprintf(":%d", port)
					go s.localListener(g, localAddr, remoteAddr)
					time.Sleep(1 * time.Millisecond) // for wide port ranges
				}
				continue
			}

			port, err := strconv.Atoi(localPortOrRange)
			if err == nil && port > 1 && port < 65535 { // format port=remoteAddress
				localAddr = fmt.Sprintf(":%d", port)
			} else {
				localAddr = localPortOrRange // format ip:port=remoteAddress
			}
		} else {
			s.logger.Fatalf("invalid port mapping format: %s", portMapping)
		}

		go s.localListener(g, localAddr, remoteAddr)
	}
}

func (s *KcpTransport) localListener(g *kcpGen, localAddr string, remoteAddr string) {
	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		s.logger.Fatalf("failed to start listener on %s: %v", localAddr, err)
		return
	}

	defer listener.Close()

	s.logger.Infof("listener started successfully, listening on address: %s", listener.Addr().String())

	go s.acceptLocalConn(g, listener, remoteAddr)

	<-g.ctx.Done()
}

func (s *KcpTransport) acceptLocalConn(g *kcpGen, listener net.Listener, remoteAddr string) {
	for {
		select {
		case <-g.ctx.Done():
			return

		default:
			conn, err := listener.Accept()
			if err != nil {
				s.logger.Debugf("failed to accept connection on %s: %v", listener.Addr().String(), err)
				continue
			}

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
			conn = s.limits.wrap(conn)

			select {
			case g.localChannel <- LocalTCPConn{conn: conn, remoteAddr: remoteAddr, timeCreated: time.Now().UnixMilli()}:
				s.logger.Debugf("accepted incoming TCP connection from %s", tcpConn.RemoteAddr().String())

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
				s.logger.Warnf("local listener channel is full, discarding TCP connection from %s", tcpConn.LocalAddr().String())
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

func (s *KcpTransport) handleSession(g *kcpGen, session *smux.Session) {
	counter := make(chan struct{}, s.config.MuxCon)
	defer session.Close()
	defer close(counter)

	for {
		// +1 for mux connection counter
		counter <- struct{}{}

		select {
		case <-g.ctx.Done():
			return

		case incomingConn := <-g.localChannel:
			if time.Now().UnixMilli()-incomingConn.timeCreated > 3000 { // 3000ms
				s.logger.Debugf("timeouted local connection: %d ms", time.Now().UnixMilli()-incomingConn.timeCreated)
				incomingConn.conn.Close()

				atomic.AddInt32(&s.streamCounter, -1)
				<-counter
				continue
			}

			stream, err := session.OpenStream()
			if err != nil {
				s.handleSessionError(g, &incomingConn, err)
				return
			}

			// Send the target port over the tunnel connection
			if err := utils.SendBinaryString(stream, incomingConn.remoteAddr); err != nil {
				s.logger.Tracef("failed to send address over stream: %v", err)
				// Put local connection back to local channel
				g.localChannel <- incomingConn
				continue
			}

			// Handle data exchange between connections
			go func() {
				// Free the connection slot once the transfer ends, or the
				// limit would fill up permanently.
				defer s.limits.release()
				handlers.TCPConnectionHandler(g.ctx, s.config.ProxyProtocol, incomingConn.conn, metrics.CountedConn(stream), s.logger, g.usageMonitor, incomingConn.conn.LocalAddr().(*net.TCPAddr).Port, s.config.Sniffer)
				atomic.AddInt32(&s.streamCounter, -1)
				<-counter // read signal from the channel
			}()
		}
	}
}

func (s *KcpTransport) handleSessionError(g *kcpGen, incomingConn *LocalTCPConn, err error) {
	s.logger.Tracef("failed to handle session: %v", err)

	atomic.AddInt32(&s.sessionCounter, -1)

	// Put local connection back to local channel
	g.localChannel <- *incomingConn

	select {
	case g.reqNewConnChan <- struct{}{}:
	default:
		s.logger.Warn("request new connection channel is full")
	}
}

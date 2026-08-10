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
}

type TcpMuxTransport struct {
	config *TcpMuxConfig
	// muxV1/muxV2 are both built up front so that settling the version costs
	// nothing per connection; muxVersion says which one this run agreed on.
	muxV1            *smux.Config
	muxV2            *smux.Config
	muxVersion       atomic.Int32
	parentctx        context.Context
	ctx              context.Context
	cancel           context.CancelFunc
	logger           *logrus.Logger
	tunnelChannel    chan *smux.Session
	handshakeChannel chan controlCandidate
	localChannel     chan LocalTCPConn
	reqNewConnChan   chan struct{}
	controlChannel   netControl
	usageMonitor     *web.Usage
	restartMutex     sync.Mutex
	streamCounter    int32
	sessionCounter   int32
	limits           *limiter
	// poolNonce is what this run's pool connections must present. It is empty
	// while no control channel is up, and stays empty for a legacy client that
	// cannot present one — which is what keeps the source-address fallback
	// reachable. See network.PoolNonce.
	poolNonce network.PoolNonce
}

type TcpMuxConfig struct {
	BindAddr         string
	TunnelStatus     string
	SnifferLog       string
	Token            string
	Ports            []string
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
	// Create a derived context from the parent context
	ctx, cancel := context.WithCancel(parentCtx)

	// Initialize the TcpTransport struct
	muxSettings := network.MuxSettings{
		MaxFrameSize:     config.MaxFrameSize,
		MaxReceiveBuffer: config.MaxReceiveBuffer,
		MaxStreamBuffer:  config.MaxStreamBuffer,
	}
	server := &TcpMuxTransport{
		muxV1:            network.SmuxConfig(1, muxSettings),
		muxV2:            network.SmuxConfig(2, muxSettings),
		config:           config,
		parentctx:        parentCtx,
		ctx:              ctx,
		cancel:           cancel,
		logger:           logger,
		tunnelChannel:    make(chan *smux.Session, config.ChannelSize),
		handshakeChannel: make(chan controlCandidate, 1),
		localChannel:     make(chan LocalTCPConn, config.ChannelSize),
		reqNewConnChan:   make(chan struct{}, config.ChannelSize),
		streamCounter:    0,
		sessionCounter:   0,
		usageMonitor:     web.NewDataStore(fmt.Sprintf(":%v", config.WebPort), ctx, config.SnifferLog, config.Sniffer, &config.TunnelStatus, logger),
		limits:           newLimiter(Limits{MaxConnections: config.MaxConnections, BandwidthMbps: config.BandwidthMbps}),
	}

	return server
}

func (s *TcpMuxTransport) Start() {
	// The state of this run. Restart replaces these fields for the next
	// one, so they are read once here and carried from goroutine to
	// goroutine; a goroutine that outlives its run keeps what it started
	// with instead of reading whatever the next run has installed.
	g := &tcpMuxGen{
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
	s.config.TunnelStatus = "Disconnected (TCPMux)"

	go s.tunnelListener(g)

	s.channelHandshake(g)

	if s.controlChannel.IsSet() {
		s.config.TunnelStatus = "Connected (TCPMux)"

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
func (s *TcpMuxTransport) Restart() {
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

	// Close any open connections in the tunnel channel.
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
	s.handshakeChannel = make(chan controlCandidate, 1)
	s.controlChannel.Clear()
	// The next run issues its own nonce, so connections still carrying this
	// one must stop being accepted the moment the run ends. The mux version is
	// settled again by the next handshake, with a peer that may not be the same
	// one or the same build.
	s.poolNonce.Clear()
	s.muxVersion.Store(0)
	s.usageMonitor = web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx, s.config.SnifferLog, s.config.Sniffer, &s.config.TunnelStatus, s.logger)
	s.config.TunnelStatus = ""
	// Stored atomically, like every other access: the goroutines of the run
	// being replaced may still be counting while this resets them.
	atomic.StoreInt32(&s.streamCounter, 0)
	atomic.StoreInt32(&s.sessionCounter, 0)

	// set the log level again
	s.logger.SetLevel(level)

	go s.Start()
}

// channelHandshake waits for a connection that has already proved it holds the
// token and asked to be the control channel.
//
// The proving happens on the accept path, in the candidate's own goroutine —
// see announce.go — so this only has to publish the winner.
func (s *TcpMuxTransport) channelHandshake(g *tcpMuxGen) {
	select {
	case <-g.ctx.Done():
		return
	case candidate := <-g.handshakeChannel:
		//FORCE CONTROL CHANNEL TO BE TCP_NODELAY
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

		if candidate.nonce == "" {
			s.logger.Warn(legacyPoolWarning)
		}
		s.logger.Infof("control channel successfully established (mux version %d).", s.muxVersion.Load())

		return
	}
}

func (s *TcpMuxTransport) channelHandler(g *tcpMuxGen) {
	ticker := time.NewTicker(s.config.Heartbeat)
	defer ticker.Stop()

	// Channel to receive the message or error
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
			err := utils.SendBinaryByte(s.controlChannel.Get(), utils.SG_Chan)
			if err != nil {
				s.logger.Error("failed to send request new connection signal. ", err)
				go s.Restart()
				return
			}

		case <-ticker.C:
			err := utils.SendBinaryByte(s.controlChannel.Get(), utils.SG_HB)
			if err != nil {
				s.logger.Error("failed to send heartbeat signal")
				go s.Restart()
				return
			}
			s.logger.Trace("heartbeat signal sent successfully")

		case message, ok := <-messageChan:
			if !ok {
				s.logger.Error("channel closed, likely due to an error in TCP read")
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

func (s *TcpMuxTransport) tunnelListener(g *tcpMuxGen) {
	listener, err := network.ListenWithBuffers(
		"tcp",
		s.config.BindAddr,
		s.config.SO_RCVBUF,
		s.config.SO_SNDBUF,
		s.config.MSS,
		s.config.KeepAlive,
		!s.config.Nodelay,
	)
	if err != nil {
		s.logger.Fatalf("failed to start listener on %s: %v", s.config.BindAddr, err)
		return
	}

	defer listener.Close()

	s.logger.Infof("server started successfully, listening on address: %s", listener.Addr().String())

	go s.acceptTunnelConn(g, listener)

	<-g.ctx.Done()
}

func (s *TcpMuxTransport) acceptTunnelConn(g *tcpMuxGen, listener net.Listener) {
	for {
		select {
		case <-g.ctx.Done():
			return
		default:
			s.logger.Debugf("waiting for accept incoming tunnel connection on %s", listener.Addr().String())
			conn, err := listener.Accept()
			if err != nil {
				s.logger.Debugf("failed to accept tunnel connection on %s: %v", listener.Addr().String(), err)
				continue
			}

			//discard any non tcp connection
			tcpConn, ok := conn.(*net.TCPConn)
			if !ok {
				s.logger.Warnf("disarded non-TCP tunnel connection from %s", conn.RemoteAddr().String())
				conn.Close()
				continue
			}

			// trying to set tcpnodelay
			if !s.config.Nodelay {
				if err := tcpConn.SetNoDelay(s.config.Nodelay); err != nil {
					s.logger.Warnf("failed to set TCP_NODELAY for %s: %v", tcpConn.RemoteAddr().String(), err)
				} else {
					s.logger.Tracef("TCP_NODELAY disabled for %s", tcpConn.RemoteAddr().String())
				}
			}

			// Set keep-alive settings
			if err := tcpConn.SetKeepAlive(true); err != nil {
				s.logger.Warnf("failed to enable TCP keep-alive for %s: %v", tcpConn.RemoteAddr().String(), err)
			} else {
				s.logger.Tracef("TCP keep-alive enabled for %s", tcpConn.RemoteAddr().String())
			}
			if err := tcpConn.SetKeepAlivePeriod(s.config.KeepAlive); err != nil {
				s.logger.Warnf("failed to set TCP keep-alive period for %s: %v", tcpConn.RemoteAddr().String(), err)
			}

			// Everything from here — the announcement, the token or nonce
			// check, building the mux session — happens in this connection's
			// own goroutine, so a peer that connects and then says nothing
			// costs one goroutine and never delays the connections behind it.
			go s.admitTunnelConn(g, conn)
		}
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
		s.deliverTunnelConn(g, conn)
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
		s.deliverTunnelConn(g, conn)

	default:
		s.logger.Warnf("unexpected announcement %d from %s, discarding", ann.signal, conn.RemoteAddr())
		conn.Close()
	}
}

// admitControlChannel verifies a peer claiming the control channel, answers it,
// and offers it as the candidate for channelHandshake to publish.
func (s *TcpMuxTransport) admitControlChannel(g *tcpMuxGen, conn net.Conn, ann announcement) {
	// One control channel per run. Without this a second claimant would be
	// buffered on a channel nobody reads any more, holding its connection open
	// until the next restart.
	if s.controlChannel.IsSet() {
		s.logger.Debugf("a control channel is already established, discarding the claim from %s", conn.RemoteAddr())
		conn.Close()
		return
	}

	if !tokenMatches(ann.payload, s.config.Token) {
		s.logger.Warnf("invalid security token received from %s", conn.RemoteAddr())
		conn.Close()
		return
	}

	ack, nonce, muxVersion, err := controlAck(ann.signal, s.config.Token, network.ResolveMuxVersion(s.config.MuxVersion))
	if err != nil {
		s.logger.Errorf("could not answer the control handshake: %v", err)
		conn.Close()
		return
	}
	if err := utils.SendBinaryTransportString(conn, ack, ann.signal); err != nil {
		s.logger.Errorf("failed to send security token: %v", err)
		conn.Close()
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
func (s *TcpMuxTransport) deliverTunnelConn(g *tcpMuxGen, conn net.Conn) {
	session, err := smux.Client(conn, s.smuxCfg())
	if err != nil {
		s.logger.Errorf("failed to create MUX session for connection %s: %v", conn.RemoteAddr().String(), err)
		conn.Close()
		return
	}

	select {
	case <-g.ctx.Done():
		session.Close()
	case g.tunnelChannel <- session: // ok
	default:
		s.logger.Warnf("tunnel listener channel is full, discarding TCP connection from %s", conn.LocalAddr().String())
		session.Close()
	}
}

func (s *TcpMuxTransport) parsePortMappings(g *tcpMuxGen) {
	for _, portMapping := range s.config.Ports {
		parts := strings.Split(portMapping, "=")

		var localAddr, remoteAddr string

		// Check if only a single port or a port range is provided (no "=" present)
		if len(parts) == 1 {
			localPortOrRange := strings.TrimSpace(parts[0])
			remoteAddr = localPortOrRange // If no remote addr is provided, use the local port as the remote port

			// Check if it's a port range
			if strings.Contains(localPortOrRange, "-") {
				rangeParts := strings.Split(localPortOrRange, "-")
				if len(rangeParts) != 2 {
					s.logger.Fatalf("invalid port range format: %s", localPortOrRange)
				}

				// Parse and validate start and end ports
				startPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
				if err != nil || startPort < 1 || startPort > 65535 {
					s.logger.Fatalf("invalid start port in range: %s", rangeParts[0])
				}

				endPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
				if err != nil || endPort < 1 || endPort > 65535 || endPort < startPort {
					s.logger.Fatalf("invalid end port in range: %s", rangeParts[1])
				}

				// Create listeners for all ports in the range
				for port := startPort; port <= endPort; port++ {
					localAddr = fmt.Sprintf(":%d", port)
					go s.localListener(g, localAddr, strconv.Itoa(port)) // Use port as the remoteAddr
					time.Sleep(1 * time.Millisecond)                     // for wide port ranges
				}
				continue
			} else {
				// Handle single port case
				port, err := strconv.Atoi(localPortOrRange)
				if err != nil || port < 1 || port > 65535 {
					s.logger.Fatalf("invalid port format: %s", localPortOrRange)
				}
				localAddr = fmt.Sprintf(":%d", port)
			}
		} else if len(parts) == 2 {
			// Handle "local=remote" format
			localPortOrRange := strings.TrimSpace(parts[0])
			remoteAddr = strings.TrimSpace(parts[1])

			// Check if local port is a range
			if strings.Contains(localPortOrRange, "-") {
				rangeParts := strings.Split(localPortOrRange, "-")
				if len(rangeParts) != 2 {
					s.logger.Fatalf("invalid port range format: %s", localPortOrRange)
				}

				// Parse and validate start and end ports
				startPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
				if err != nil || startPort < 1 || startPort > 65535 {
					s.logger.Fatalf("invalid start port in range: %s", rangeParts[0])
				}

				endPort, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
				if err != nil || endPort < 1 || endPort > 65535 || endPort < startPort {
					s.logger.Fatalf("invalid end port in range: %s", rangeParts[1])
				}

				// Create listeners for all ports in the range
				for port := startPort; port <= endPort; port++ {
					localAddr = fmt.Sprintf(":%d", port)
					go s.localListener(g, localAddr, remoteAddr)
					time.Sleep(1 * time.Millisecond) // for wide port ranges
				}
				continue
			} else {
				// Handle single local port case
				port, err := strconv.Atoi(localPortOrRange)
				if err == nil && port > 1 && port < 65535 { // format port=remoteAddress
					localAddr = fmt.Sprintf(":%d", port)
				} else {
					localAddr = localPortOrRange // format ip:port=remoteAddress
				}
			}
		} else {
			s.logger.Fatalf("invalid port mapping format: %s", portMapping)
		}
		// Start listeners for single port
		go s.localListener(g, localAddr, remoteAddr)
	}
}

func (s *TcpMuxTransport) localListener(g *tcpMuxGen, localAddr string, remoteAddr string) {
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

func (s *TcpMuxTransport) acceptLocalConn(g *tcpMuxGen, listener net.Listener, remoteAddr string) {
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
				s.logger.Warnf("disarded non-TCP connection from %s", conn.RemoteAddr().String())
				conn.Close()
				continue
			}

			// trying to disable tcpnodelay
			if !s.config.Nodelay {
				if err := tcpConn.SetNoDelay(s.config.Nodelay); err != nil {
					s.logger.Warnf("failed to set TCP_NODELAY for %s: %v", tcpConn.RemoteAddr().String(), err)
				} else {
					s.logger.Tracef("TCP_NODELAY disabled for %s", tcpConn.RemoteAddr().String())
				}
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

			atomic.AddInt32(&s.streamCounter, 1)
			incoming := newCountedLocalTCPConn(conn, remoteAddr, s.limits, func() {
				atomic.AddInt32(&s.streamCounter, -1)
			})
			select {
			case g.localChannel <- incoming:
				s.logger.Debugf("accepted incoming TCP connection from %s", tcpConn.RemoteAddr().String())

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
				incoming.closeAndRelease()
			}

		}
	}

}

func (s *TcpMuxTransport) handleLoop(g *tcpMuxGen) {
	defer drainLocalTCP(g.localChannel)
	defer func() {
		for {
			select {
			case session := <-g.tunnelChannel:
				session.Close()
			default:
				return
			}
		}
	}()

	for {
		select {
		case <-g.ctx.Done():
			return

		case session := <-g.tunnelChannel:
			// +1 for session counter
			atomic.AddInt32(&s.sessionCounter, 1)

			go s.handleSession(g, session)
		}
	}
}

func (s *TcpMuxTransport) handleSession(g *tcpMuxGen, session *smux.Session) {
	counter := make(chan struct{}, s.config.MuxCon)
	defer session.Close()

	for {
		select {
		case counter <- struct{}{}:
		case <-g.ctx.Done():
			return
		}

		select {
		case <-g.ctx.Done():
			<-counter
			return

		case incomingConn := <-g.localChannel:
			if !incomingConn.claim() {
				<-counter
				continue
			}

			stream, err := session.OpenStream()
			if err != nil {
				incomingConn.closeAndRelease()
				s.handleSessionError(g, err)
				return
			}

			// Send the target port over the tunnel connection
			if err := utils.SendBinaryString(stream, incomingConn.remoteAddr); err != nil {
				s.logger.Tracef("failed to send address over stream: %v", err)
				stream.Close()
				incomingConn.closeAndRelease()
				<-counter
				continue
			}

			// Handle data exchange between connections
			go func(incomingConn LocalTCPConn, stream net.Conn) {
				// Free the connection slot once the transfer ends, or the
				// limit would fill up permanently.
				defer incomingConn.closeAndRelease()
				handlers.TCPConnectionHandler(g.ctx, s.config.ProxyProtocol, incomingConn.conn, metrics.CountedConn(stream), s.logger, g.usageMonitor, incomingConn.conn.LocalAddr().(*net.TCPAddr).Port, s.config.Sniffer)
				<-counter // read signal from the channel
			}(incomingConn, stream)
		}
	}
}

func (s *TcpMuxTransport) handleSessionError(g *tcpMuxGen, err error) {
	s.logger.Tracef("failed to handle session: %v", err)

	// decrease session value
	atomic.AddInt32(&s.sessionCounter, -1)

	// Attempt to request a new connection
	select {
	case g.reqNewConnChan <- struct{}{}:
	default:
		s.logger.Warn("request new connection channel is full")
	}
}

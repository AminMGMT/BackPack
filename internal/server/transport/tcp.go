package transport

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

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
}

type TcpTransport struct {
	// The status shown in the panel. Behind a lock because the run being
	// replaced and the run replacing it both write it. See tunnelStatus.
	status    tunnelStatus
	config    *TcpConfig
	parentctx context.Context
	// The current run. Replaced by Restart while the previous run's
	// goroutines are still reading it, so it lives behind a lock.
	run              runState
	logger           *logrus.Logger
	tunnelChannel    chan net.Conn
	localChannel     chan LocalTCPConn
	reqNewConnChan   chan struct{}
	handshakeChannel chan controlCandidate
	controlChannel   netControl
	restartMutex     sync.Mutex
	usageMonitor     *web.Usage
	rtt              int64 // in ms, for UDP
	limits           *limiter
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
	// Create a derived context from the parent context
	ctx, cancel := context.WithCancel(parentCtx)

	// Initialize the TcpTransport struct
	server := &TcpTransport{
		config:         config,
		parentctx:      parentCtx,
		logger:         logger,
		tunnelChannel:  make(chan net.Conn, config.ChannelSize),
		localChannel:   make(chan LocalTCPConn, config.ChannelSize),
		reqNewConnChan: make(chan struct{}, config.ChannelSize),
		// Buffered by one so a control channel that arrives in the moment
		// between the listener starting and channelHandshake reaching its
		// select is held rather than dropped.
		handshakeChannel: make(chan controlCandidate, 1),
		limits:           newLimiter(Limits{MaxConnections: config.MaxConnections, BandwidthMbps: config.BandwidthMbps}),
		rtt:              0,
	}

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
func (s *TcpTransport) Start() {
	s.start(&tcpGen{
		ctx:              s.run.context(),
		tunnelChannel:    s.tunnelChannel,
		localChannel:     s.localChannel,
		reqNewConnChan:   s.reqNewConnChan,
		handshakeChannel: s.handshakeChannel,
		usageMonitor:     s.usageMonitor,
	})
}

// start runs one generation of the transport. Everything it needs is in g:
// nothing in here reaches back for a field that the next Restart is entitled to
// replace while this run is still using it.
func (s *TcpTransport) start(g *tcpGen) {
	s.status.set("Disconnected (TCP)")

	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}

	go s.tunnelListener(g)

	s.channelHandshake(g)

	if s.controlChannel.IsSet() {
		s.status.set("Connected (TCP)")

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
func (s *TcpTransport) Restart() {
	if !s.restartMutex.TryLock() {
		s.logger.Warn("server restart already in progress, skipping restart attempt")
		return
	}
	defer s.restartMutex.Unlock()

	s.logger.Info("restarting server...")

	// for removing timeout logs
	level := s.logger.Level
	s.logger.SetLevel(logrus.FatalLevel)

	s.run.stop()

	// Close open connection
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
	s.run.set(ctx, cancel)

	// The next run's state, built here and handed straight to start(). It used
	// to be written onto the transport for start() to read back, which is a
	// value published by one goroutine and read by another with nothing
	// ordering them — the same shape as the ctx/cancel race the detector caught
	// on kcp.go, and present on every one of these fields. Passing it removes
	// the shared field rather than locking it.
	g := &tcpGen{
		ctx:              ctx,
		tunnelChannel:    make(chan net.Conn, s.config.ChannelSize),
		localChannel:     make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan:   make(chan struct{}, s.config.ChannelSize),
		handshakeChannel: make(chan controlCandidate, 1),
		usageMonitor:     web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx, s.config.SnifferLog, s.config.Sniffer, s.status.get, s.logger),
	}

	// Re-initialize variables
	s.status.set("")
	s.controlChannel.Clear()
	// The next run issues its own nonce, so connections still carrying this
	// one must stop being accepted the moment the run ends.
	s.poolNonce.Clear()

	// set the log level again
	s.logger.SetLevel(level)

	go s.start(g)
}

// channelHandshake waits for a connection that has already proved it holds the
// token and asked to be the control channel.
//
// The proving happens on the accept path, in the candidate's own goroutine —
// see announce.go — so this only has to publish the winner.
func (s *TcpTransport) channelHandshake(g *tcpGen) {
	select {
	case <-g.ctx.Done():
		return
	case candidate := <-g.handshakeChannel:
		// Order matters: the nonce has to be in place before the control
		// channel is, or a pool connection racing in behind the handshake
		// would be checked against a nonce that is not there yet.
		s.poolNonce.Set(candidate.nonce)
		s.controlChannel.Set(candidate.conn)

		if candidate.nonce == "" {
			s.logger.Warn(legacyPoolWarning)
		}
		s.logger.Info("control channel successfully established.")
		return
	}
}

func (s *TcpTransport) channelHandler(g *tcpGen) {
	ticker := time.NewTicker(s.config.Heartbeat)
	defer ticker.Stop()

	// Channel to receive the message or error
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

	// RTT measurment
	rtt := time.Now()
	err := utils.SendBinaryByte(s.controlChannel.Get(), utils.SG_RTT)
	if err != nil {
		s.logger.Error("failed to send RTT signal, attempting to restart server...")
		go s.Restart()
		return
	}

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

			} else if message == utils.SG_RTT {
				measureRTT := time.Since(rtt)
				s.rtt = measureRTT.Milliseconds()
				s.logger.Infof("Round Trip Time (RTT): %d ms", s.rtt)
			}
		}
	}
}

func (s *TcpTransport) tunnelListener(g *tcpGen) {
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

func (s *TcpTransport) acceptTunnelConn(g *tcpGen, listener net.Listener) {
	var backoff acceptBackoff
	for {
		select {
		case <-g.ctx.Done():
			return
		default:
			conn, err := listener.Accept()
			if err != nil {
				s.logger.Debugf("failed to accept tunnel connection on %s: %v", listener.Addr(), err)
				// Back off rather than retry instantly: a closed listener fails
				// immediately and forever, and `continue` would pin a core.
				if !backoff.fail(g.ctx) {
					return
				}
				continue
			}
			backoff.ok()

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

			// Everything from here — the stealth handshake, the announcement,
			// the token or nonce check — happens in this connection's own
			// goroutine, so a peer that connects and then says nothing costs
			// one goroutine and never delays the connections behind it.
			go s.admitTunnelConn(g, conn)
		}
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
func (s *TcpTransport) admitControlChannel(g *tcpGen, conn net.Conn, ann announcement) {
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

	// Plain TCP carries no mux sessions, so there is no version to settle: 0
	// tells the client there is nothing to apply.
	ack, nonce, _, err := controlAck(ann.signal, s.config.Token, 0)
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

	select {
	case g.handshakeChannel <- controlCandidate{conn: conn, nonce: nonce}:
	default:
		s.logger.Warn("a control channel is already established, discarding duplicate")
		conn.Close()
	}
}

// deliverTunnelConn hands an admitted connection to the pool, dropping it if
// the pool is full.
func (s *TcpTransport) deliverTunnelConn(g *tcpGen, conn net.Conn) {
	select {
	case g.tunnelChannel <- conn:
	default: // The channel is full, do nothing
		s.logger.Warnf("tunnel listener channel is full, discarding TCP connection from %s", conn.LocalAddr().String())
		conn.Close()
	}
}

func (s *TcpTransport) parsePortMappings(g *tcpGen) {
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
					go s.startListeners(g, localAddr, strconv.Itoa(port)) // Use port as the remoteAddr
					time.Sleep(1 * time.Millisecond)                      // for wide port ranges
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
					go s.startListeners(g, localAddr, remoteAddr)
					time.Sleep(1 * time.Millisecond) // for wide port ranges
				}
				continue
			} else {
				// Handle single local port case
				localAddr = listenAddrFor(localPortOrRange)
			}
		} else {
			s.logger.Fatalf("invalid port mapping format: %s", portMapping)
		}
		// Start listeners for single port
		go s.startListeners(g, localAddr, remoteAddr)
	}
}

func (s *TcpTransport) startListeners(g *tcpGen, localAddr, remoteAddr string) {
	// Start TCP listener
	go s.localListener(g, localAddr, remoteAddr)

	s.logger.Debugf("Started listening on %s, forwarding to %s", localAddr, remoteAddr)
}

func (s *TcpTransport) localListener(g *tcpGen, localAddr string, remoteAddr string) {
	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		s.logger.Fatalf("failed to listen on %s: %v", localAddr, err)
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
			udpAdmitter(g.localChannel, g.reqNewConnChan, s.limits))
	}

	<-g.ctx.Done()
}

func (s *TcpTransport) acceptLocalConn(g *tcpGen, listener net.Listener, remoteAddr string) {
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
				if !backoff.fail(g.ctx) {
					return
				}
				continue
			}
			backoff.ok()

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

			select {
			case g.localChannel <- LocalTCPConn{conn: conn, remoteAddr: remoteAddr, timeCreated: time.Now().UnixMilli()}:

				select {
				case g.reqNewConnChan <- struct{}{}:
					// Successfully requested a new connection
				default:
					// The channel is full, do nothing
					s.logger.Warn("channel is full, cannot request a new connection")
				}

				s.logger.Debugf("accepted incoming TCP connection from %s", tcpConn.RemoteAddr().String())

			default: // channel is full, discard the connection
				s.logger.Warnf("channel with listener %s is full, discarding TCP connection from %s", listener.Addr().String(), tcpConn.LocalAddr().String())
				s.limits.release()
				conn.Close()
			}
		}
	}
}

func (s *TcpTransport) handleLoop(g *tcpGen) {
	for {
		select {
		case <-g.ctx.Done():
			return
		case localConn := <-g.localChannel:
		loop:
			for {
				if time.Now().UnixMilli()-localConn.timeCreated > 3000 { // 3000ms
					s.logger.Debugf("timeouted local connection: %d ms", time.Now().UnixMilli()-localConn.timeCreated)
					localConn.conn.Close()
					// The slot this connection took on accept is only freed by the
					// handler goroutine, which never runs when the connection times
					// out waiting to be paired. Without this a tunnel with a
					// connection limit loses a slot for every timeout until it can
					// accept nothing at all.
					s.limits.release()
					break loop
				}

				select {
				case <-g.ctx.Done():
					return

				case tunnelConn := <-g.tunnelChannel:
					// Send the target addr over the connection
					if err := utils.SendBinaryTransportString(tunnelConn, localConn.remoteAddr, utils.SG_TCP); err != nil {
						s.logger.Errorf("%v", err)
						tunnelConn.Close()
						continue loop
					}

					// Handle data exchange between connections
					go func(localConn LocalTCPConn, tunnelConn net.Conn) {
						// Free the connection slot once the transfer ends, or
						// the limit would fill up permanently.
						defer s.limits.release()
						handlers.TCPConnectionHandler(g.ctx, s.config.ProxyProtocol && !isUDPFlow(localConn.conn), localConn.conn, metrics.CountedConn(tunnelConn), s.logger, g.usageMonitor, localForwardPort(localConn.conn), s.config.Sniffer)
					}(localConn, tunnelConn)
					break loop

				}
			}
		}
	}
}

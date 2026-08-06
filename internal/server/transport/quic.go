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
}

// QuicTransport is the server side of the QUIC transport. One QUIC connection
// from the client carries everything: a control stream for the signalling, and
// a stream per forwarded flow. QUIC brings its own TLS 1.3, stream multiplexing,
// congestion control and loss recovery, so there is no smux, no FEC and no
// hand-tuning here — the protocol does what KCP needed a stack of settings for.
type QuicTransport struct {
	config         *QuicConfig
	quicSettings   network.QUICSettings
	parentctx      context.Context
	ctx            context.Context
	cancel         context.CancelFunc
	logger         *logrus.Logger
	tunnelChannel  chan net.Conn
	localChannel   chan LocalTCPConn
	reqNewConnChan chan struct{}
	controlChannel netControl
	usageMonitor   *web.Usage
	restartMutex   sync.Mutex
	limits         *limiter
}

type QuicConfig struct {
	BindAddr      string
	TunnelStatus  string
	SnifferLog    string
	Token         string
	Ports         []string
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
	Forward       bool
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

	return &QuicTransport{
		config:         config,
		quicSettings:   config.settings(),
		parentctx:      parentCtx,
		ctx:            ctx,
		cancel:         cancel,
		logger:         logger,
		tunnelChannel:  make(chan net.Conn, config.ChannelSize),
		localChannel:   make(chan LocalTCPConn, config.ChannelSize),
		reqNewConnChan: make(chan struct{}, config.ChannelSize),
		usageMonitor:   web.NewDataStore(fmt.Sprintf(":%v", config.WebPort), ctx, config.SnifferLog, config.Sniffer, &config.TunnelStatus, logger),
		limits:         newLimiter(Limits{MaxConnections: config.MaxConnections, BandwidthMbps: config.BandwidthMbps}),
	}
}

func (s *QuicTransport) Start() {
	g := &quicGen{
		ctx:            s.ctx,
		tunnelChannel:  s.tunnelChannel,
		localChannel:   s.localChannel,
		reqNewConnChan: s.reqNewConnChan,
		usageMonitor:   s.usageMonitor,
	}

	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}
	s.config.TunnelStatus = "Disconnected (QUIC)"

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

	s.config.TunnelStatus = "Connected (QUIC)"
	go s.channelHandler(g)
	if s.config.Forward {
		return
	}

	numCPU := runtime.NumCPU()
	if numCPU > 4 {
		numCPU = 4 // Max allowed handler is 4
	}

	go s.parsePortMappings(g)
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
	// on a reload, or on the process going down. Rebuilding from a finished
	// parent context would bind the listener only to close it again.
	if s.parentctx.Err() != nil {
		s.logger.SetLevel(level)
		s.logger.Debug("restart abandoned: the tunnel is shutting down")
		return
	}

	ctx, cancel := context.WithCancel(s.parentctx)
	s.ctx = ctx
	s.cancel = cancel

	// Re-initialise the per-run state.
	s.tunnelChannel = make(chan net.Conn, s.config.ChannelSize)
	s.localChannel = make(chan LocalTCPConn, s.config.ChannelSize)
	s.reqNewConnChan = make(chan struct{}, s.config.ChannelSize)
	s.controlChannel.Clear()
	// The peer is gone until a new control channel arrives; a stale address would
	// be shown as if it were current.
	metrics.ClearPeer()
	s.usageMonitor = web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx, s.config.SnifferLog, s.config.Sniffer, &s.config.TunnelStatus, s.logger)
	s.config.TunnelStatus = ""

	s.logger.SetLevel(level)

	go s.Start()
}

// tunnelListener accepts QUIC connections for the whole run and hands each to
// handleConn, which sorts its streams into the control stream and data streams.
// The re-adopt decision lives on the control stream instead of here, because a
// connection has proved nothing until its control stream passes the token — a
// peer that has not is not a reason to disturb the running tunnel.
func (s *QuicTransport) tunnelListener(g *quicGen, handshake chan<- net.Conn) {
	listener, err := network.QUICListen(s.config.BindAddr, s.quicSettings)
	if err != nil {
		s.logger.Fatalf("failed to start listener on %s: %v", s.config.BindAddr, err)
		return
	}

	s.logger.Infof("server started successfully, listening on address: %s (QUIC)", listener.Addr().String())

	defer listener.Close()
	go func() {
		<-g.ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept(g.ctx)
		if err != nil {
			if g.ctx.Err() != nil {
				return
			}
			s.logger.Debugf("failed to accept quic connection on %s: %v", listener.Addr().String(), err)
			continue
		}

		go s.handleConn(g, conn, handshake)
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

	if err := stream.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
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

	if token != s.config.Token {
		s.logger.Warnf("invalid security token received from %s", conn.RemoteAddr())
		stream.Close()
		return
	}

	switch signal {
	case utils.SG_Chan:
		// The control stream. Answering with the token proves to the client that
		// this server knows the secret too.
		if err := utils.SendBinaryTransportString(wrapped, s.config.Token, utils.SG_Chan); err != nil {
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

	case utils.SG_ForwardTCP:
		if !s.config.Forward || !s.controlChannel.IsSet() {
			s.logger.Warnf("invalid forward QUIC stream from %s", conn.RemoteAddr())
			stream.Close()
			return
		}
		go handleForwardStream(g.ctx, wrapped, s.config.KeepAlive, s.logger, g.usageMonitor, s.config.Sniffer)

	default:
		s.logger.Warnf("unexpected announcement signal %v from %s", signal, conn.RemoteAddr())
		stream.Close()
	}
}

func (s *QuicTransport) channelHandler(g *quicGen) {
	ticker := time.NewTicker(s.config.Heartbeat)
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
					if s.cancel != nil {
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

func (s *QuicTransport) parsePortMappings(g *quicGen) {
	for _, portMapping := range s.config.Ports {
		parts := strings.Split(portMapping, "=")

		var localAddr, remoteAddr string

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

			localAddr = mappingListenAddress(localPortOrRange)
		} else {
			s.logger.Fatalf("invalid port mapping format: %s", portMapping)
		}

		go s.localListener(g, localAddr, remoteAddr)
	}
}

func (s *QuicTransport) localListener(g *quicGen, localAddr string, remoteAddr string) {
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

func (s *QuicTransport) acceptLocalConn(g *quicGen, listener net.Listener, remoteAddr string) {
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
			conn = s.limits.wrap(conn)

			select {
			case g.localChannel <- LocalTCPConn{conn: conn, remoteAddr: remoteAddr, timeCreated: time.Now().UnixMilli()}:
				s.logger.Debugf("accepted incoming TCP connection from %s", tcpConn.RemoteAddr().String())
			default: // channel is full, discard the connection
				s.logger.Warnf("local listener channel is full, discarding TCP connection from %s", tcpConn.LocalAddr().String())
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
			if time.Now().UnixMilli()-localConn.timeCreated > 3000 { // 3000ms
				s.logger.Debugf("timeouted local connection: %d ms", time.Now().UnixMilli()-localConn.timeCreated)
				localConn.conn.Close()
				s.limits.release()
				continue
			}

			// Ask the client to open a fresh stream so the pool stays topped up;
			// a warm one already waiting is taken straight off the channel.
			select {
			case g.reqNewConnChan <- struct{}{}:
			default:
			}

			s.pairLocalConn(g, localConn)
		}
	}
}

// pairLocalConn takes data streams until one accepts the target address, then
// hands the pair to the connection handler.
func (s *QuicTransport) pairLocalConn(g *quicGen, localConn LocalTCPConn) {
	for {
		select {
		case <-g.ctx.Done():
			localConn.conn.Close()
			s.limits.release()
			return

		case stream := <-g.tunnelChannel:
			// Tell the client which backend this stream is for.
			if err := utils.SendBinaryString(stream, localConn.remoteAddr); err != nil {
				s.logger.Tracef("failed to send address over stream: %v", err)
				stream.Close()
				// That stream is gone; ask for another and try again.
				select {
				case g.reqNewConnChan <- struct{}{}:
				default:
				}
				continue
			}

			go func() {
				// Free the connection slot once the transfer ends, or the limit
				// would fill up permanently.
				defer s.limits.release()
				handlers.TCPConnectionHandler(g.ctx, s.config.ProxyProtocol, localConn.conn, metrics.CountedConn(stream), s.logger, g.usageMonitor, localConn.conn.LocalAddr().(*net.TCPAddr).Port, s.config.Sniffer)
			}()
			return
		}
	}
}

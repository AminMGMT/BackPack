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
}

func (c *KcpConfig) settings() network.KCPSettings {
	return network.KCPSettings{
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
	}
}

func NewKcpServer(parentCtx context.Context, config *KcpConfig, logger *logrus.Logger) *KcpTransport {
	ctx, cancel := context.WithCancel(parentCtx)

	return &KcpTransport{
		smuxConfig: &smux.Config{
			Version:           config.MuxVersion,
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
	if s.config.WebPort > 0 {
		go s.usageMonitor.Monitor()
	}
	s.config.TunnelStatus = "Disconnected (KCP)"

	go s.tunnelListener()

	s.channelHandshake()

	if s.controlChannel.IsSet() {
		s.config.TunnelStatus = "Connected (KCP)"

		numCPU := runtime.NumCPU()
		if numCPU > 4 {
			numCPU = 4 // Max allowed handler is 4
		}

		go s.parsePortMappings()
		go s.channelHandler()

		s.logger.Infof("starting %d handle loops on each CPU thread", numCPU)

		for i := 0; i < numCPU; i++ {
			go s.handleLoop()
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
	s.streamCounter = 0
	s.sessionCounter = 0

	s.logger.SetLevel(level)

	go s.Start()
}

// channelHandshake waits for a session that has already proved it holds the
// token and asked to be the control channel.
func (s *KcpTransport) channelHandshake() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case conn := <-s.handshakeChannel:
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

func (s *KcpTransport) channelHandler() {
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
		case <-s.ctx.Done():
			_ = utils.SendBinaryByte(s.controlChannel.Get(), utils.SG_Closed)
			return

		case <-s.reqNewConnChan:
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

func (s *KcpTransport) tunnelListener() {
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

	go s.acceptTunnelConn(listener)

	<-s.ctx.Done()
}

func (s *KcpTransport) acceptTunnelConn(listener *kcp.Listener) {
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
			s.logger.Debugf("waiting for accept incoming tunnel connection on %s", listener.Addr().String())
			session, err := listener.AcceptKCP()
			if err != nil {
				s.logger.Debugf("failed to accept tunnel connection on %s: %v", listener.Addr().String(), err)
				continue
			}

			// Drop sessions coming from anywhere other than the peer that owns
			// the control channel. A KCP packet that decrypts correctly already
			// proves knowledge of the token, but pinning the address keeps a
			// second, unexpected peer from joining the tunnel.
			// Read the peer address once: checking "is it set" and then asking
			// for the address separately leaves a window where the control
			// channel is cleared in between and the address comes back nil.
			if peer := s.controlChannel.RemoteAddr(); peer != nil && !sameHost(peer, session.RemoteAddr()) {
				s.logger.Debugf("suspicious session from %v. expected address: %v. discarding...",
					session.RemoteAddr(), peer)
				session.Close()
				continue
			}

			network.ApplyKCPSettings(session, s.kcpSettings)

			// Every session announces what it is, and the announcement is read
			// off the accept path so that a peer which never sends one cannot
			// stall the sessions queued behind it.
			go s.acceptSession(session)
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
func (s *KcpTransport) acceptSession(session *kcp.UDPSession) {
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
		select {
		case s.handshakeChannel <- session: // ok
		default:
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
		case s.tunnelChannel <- muxSession: // ok
		default:
			s.logger.Warnf("tunnel listener channel is full, discarding KCP session from %s", session.RemoteAddr())
			muxSession.Close()
		}

	default:
		s.logger.Warnf("unexpected announcement signal %v from %s", signal, session.RemoteAddr())
		session.Close()
	}
}

func (s *KcpTransport) parsePortMappings() {
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
					go s.localListener(localAddr, strconv.Itoa(port))
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
					go s.localListener(localAddr, remoteAddr)
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

		go s.localListener(localAddr, remoteAddr)
	}
}

func (s *KcpTransport) localListener(localAddr string, remoteAddr string) {
	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		s.logger.Fatalf("failed to start listener on %s: %v", localAddr, err)
		return
	}

	defer listener.Close()

	s.logger.Infof("listener started successfully, listening on address: %s", listener.Addr().String())

	go s.acceptLocalConn(listener, remoteAddr)

	<-s.ctx.Done()
}

func (s *KcpTransport) acceptLocalConn(listener net.Listener, remoteAddr string) {
	for {
		select {
		case <-s.ctx.Done():
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
			case s.localChannel <- LocalTCPConn{conn: conn, remoteAddr: remoteAddr, timeCreated: time.Now().UnixMilli()}:
				s.logger.Debugf("accepted incoming TCP connection from %s", tcpConn.RemoteAddr().String())

				atomic.AddInt32(&s.streamCounter, 1)

				if atomic.LoadInt32(&s.streamCounter) >= atomic.LoadInt32(&s.sessionCounter)*int32(s.config.MuxCon) {
					s.logger.Tracef("stream counter: %v, session counter: %v", atomic.LoadInt32(&s.streamCounter), atomic.LoadInt32(&s.sessionCounter))

					select { // Attempt to request a new connection
					case s.reqNewConnChan <- struct{}{}:
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

func (s *KcpTransport) handleLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return

		case session := <-s.tunnelChannel:
			atomic.AddInt32(&s.sessionCounter, 1)

			go s.handleSession(session)
		}
	}
}

func (s *KcpTransport) handleSession(session *smux.Session) {
	counter := make(chan struct{}, s.config.MuxCon)
	defer session.Close()
	defer close(counter)

	for {
		// +1 for mux connection counter
		counter <- struct{}{}

		select {
		case <-s.ctx.Done():
			return

		case incomingConn := <-s.localChannel:
			if time.Now().UnixMilli()-incomingConn.timeCreated > 3000 { // 3000ms
				s.logger.Debugf("timeouted local connection: %d ms", time.Now().UnixMilli()-incomingConn.timeCreated)
				incomingConn.conn.Close()

				atomic.AddInt32(&s.streamCounter, -1)
				<-counter
				continue
			}

			stream, err := session.OpenStream()
			if err != nil {
				s.handleSessionError(&incomingConn, err)
				return
			}

			// Send the target port over the tunnel connection
			if err := utils.SendBinaryString(stream, incomingConn.remoteAddr); err != nil {
				s.logger.Tracef("failed to send address over stream: %v", err)
				// Put local connection back to local channel
				s.localChannel <- incomingConn
				continue
			}

			// Handle data exchange between connections
			go func() {
				// Free the connection slot once the transfer ends, or the
				// limit would fill up permanently.
				defer s.limits.release()
				handlers.TCPConnectionHandler(s.ctx, s.config.ProxyProtocol, incomingConn.conn, metrics.CountedConn(stream), s.logger, s.usageMonitor, incomingConn.conn.LocalAddr().(*net.TCPAddr).Port, s.config.Sniffer)
				atomic.AddInt32(&s.streamCounter, -1)
				<-counter // read signal from the channel
			}()
		}
	}
}

func (s *KcpTransport) handleSessionError(incomingConn *LocalTCPConn, err error) {
	s.logger.Tracef("failed to handle session: %v", err)

	atomic.AddInt32(&s.sessionCounter, -1)

	// Put local connection back to local channel
	s.localChannel <- *incomingConn

	select {
	case s.reqNewConnChan <- struct{}{}:
	default:
		s.logger.Warn("request new connection channel is full")
	}
}

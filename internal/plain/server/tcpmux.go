package server

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/plain/core"
	"github.com/backpack/backpack/internal/web"

	"github.com/sirupsen/logrus"
	"github.com/xtaci/smux"
)

// TcpMuxTransport is the plain tcpmux server: the client's pool connections
// are smux sessions, and each user connection is a stream on one of them, at
// most MuxCon to a session.
type TcpMuxTransport struct {
	config       *TcpMuxConfig
	smuxConfig   *smux.Config
	parentctx    context.Context
	logger       *logrus.Logger
	restartMutex sync.Mutex
	gen          atomic.Pointer[tcpMuxGen]
	status       core.Status
	holds        core.Holds
}

type tcpMuxGen struct {
	ctx              context.Context
	cancel           context.CancelFunc
	tunnelChannel    chan *smux.Session
	handshakeChannel chan net.Conn
	localChannel     chan LocalTCPConn
	reqNewConnChan   chan struct{}
	controlChannel   netControl
	usageMonitor     *web.Usage
	streamCounter    int32
	sessionCounter   int32
}

type TcpMuxConfig struct {
	BindAddr         string
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
	// AcceptUDP and UDPOnly: see TcpConfig. A flow is a stream of its own.
	AcceptUDP bool
	UDPOnly   bool
	// OnLegacyClaim: see TcpConfig.OnLegacyClaim.
	OnLegacyClaim func()
	// Layer is the tunnel's encryption (Backpack); nil is none.
	Layer *core.Layer
}

func NewTcpMuxServer(parentCtx context.Context, config *TcpMuxConfig, logger *logrus.Logger) *TcpMuxTransport {
	server := &TcpMuxTransport{
		smuxConfig: &smux.Config{
			Version:           config.MuxVersion,
			KeepAliveInterval: 20 * time.Second,
			KeepAliveTimeout:  40 * time.Second,
			MaxFrameSize:      config.MaxFrameSize,
			MaxReceiveBuffer:  config.MaxReceiveBuffer,
			MaxStreamBuffer:   config.MaxStreamBuffer,
		},
		config:    config,
		parentctx: parentCtx,
		logger:    logger,
	}
	server.gen.Store(server.newGen())
	context.AfterFunc(parentCtx, core.Unseated)
	return server
}

func (s *TcpMuxTransport) newGen() *tcpMuxGen {
	ctx, cancel := context.WithCancel(s.parentctx)
	return &tcpMuxGen{
		ctx:              ctx,
		cancel:           cancel,
		tunnelChannel:    make(chan *smux.Session, s.config.ChannelSize),
		handshakeChannel: make(chan net.Conn),
		localChannel:     make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan:   make(chan struct{}, s.config.ChannelSize),
		usageMonitor:     web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx, s.config.SnifferLog, s.config.Sniffer, s.status.Get, s.logger),
	}
}

// Running reports whether a control channel is up.
func (s *TcpMuxTransport) Running() bool { return s.status.Running() }

// Wait blocks until the transport's listeners are closed, or ctx ends.
func (s *TcpMuxTransport) Wait(ctx context.Context) { s.holds.Wait(ctx) }

func (s *TcpMuxTransport) Start() { s.start(s.gen.Load()) }

func (s *TcpMuxTransport) start(g *tcpMuxGen) {
	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}
	s.status.Set("Disconnected (TCPMux)")

	go s.tunnelListener(g)

	s.channelHandshake(g)

	if control := g.controlChannel.Get(); control != nil {
		s.status.Set("Connected (TCPMux)")
		core.Seated(control.RemoteAddr().String())

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

	old := s.gen.Load()
	old.cancel()

	level := s.logger.Level
	s.logger.SetLevel(logrus.FatalLevel)

	if control := old.controlChannel.Get(); control != nil {
		control.Close()
	}
	core.Unseated()

	time.Sleep(2 * time.Second)

	if s.parentctx.Err() != nil {
		s.logger.SetLevel(level)
		return
	}

	g := s.newGen()
	s.gen.Store(g)
	s.status.Set("")

	s.logger.SetLevel(level)

	go s.start(g)
}

func (s *TcpMuxTransport) channelHandshake(g *tcpMuxGen) {
	for {
		select {
		case <-g.ctx.Done():
			return
		case conn := <-g.handshakeChannel:
			if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				s.logger.Errorf("failed to set read deadline: %v", err)
				conn.Close()
				continue
			}
			msg, transport, err := core.ReceiveBinaryTransportString(conn)
			if core.LegacyClaim(transport) && err == nil && s.config.OnLegacyClaim != nil {
				s.logger.Info("the kharej runs Backpack v2.0.0: serving this tunnel on its engine until it is updated")
				conn.Close()
				s.config.OnLegacyClaim()
				continue
			}
			if transport != core.SG_Chan {
				s.logger.Errorf("invalid signal received for channel, Discarding connection")
				conn.Close()
				continue
			} else if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					s.logger.Warn("timeout while waiting for control channel signal")
				} else {
					s.logger.Errorf("failed to receive control channel signal: %v", err)
				}
				conn.Close()
				continue
			}

			conn.SetReadDeadline(time.Time{})

			if msg != s.config.Token {
				s.logger.Warnf("invalid security token received: %s", msg)
				conn.Close()
				continue
			}

			err = core.SendBinaryTransportString(conn, s.config.Token, core.SG_Chan)
			if err != nil {
				s.logger.Errorf("failed to send security token: %v", err)
				conn.Close()
				continue
			}

			tcpConn, ok := core.TCPOf(conn)
			if !ok {
				conn.Close()
				continue
			}
			if err := tcpConn.SetNoDelay(true); err != nil {
				s.logger.Warnf("failed to set TCP_NODELAY for Control Channel %s: %v", tcpConn.RemoteAddr().String(), err)
			}

			g.controlChannel.Set(conn)

			s.logger.Info("control channel successfully established.")

			return
		}
	}
}

func (s *TcpMuxTransport) channelHandler(g *tcpMuxGen) {
	ticker := time.NewTicker(s.config.Heartbeat)
	defer ticker.Stop()

	control := g.controlChannel.Get()

	messageChan := make(chan byte, 1)

	// One read: the only thing a tcpmux client sends on
	// its control channel is SG_Closed, and a read error is the channel gone.
	go func() {
		message, err := core.ReceiveBinaryByte(control)
		if err != nil {
			s.logger.Error("failed to read from channel connection. ", err)
			go s.Restart()
			return
		}
		messageChan <- message
	}()

	for {
		select {
		case <-g.ctx.Done():
			_ = core.SendBinaryByte(control, core.SG_Closed)
			return

		case <-g.reqNewConnChan:
			err := core.SendBinaryByte(control, core.SG_Chan)
			if err != nil {
				s.logger.Error("failed to send request new connection signal. ", err)
				go s.Restart()
				return
			}

		case <-ticker.C:
			err := core.SendBinaryByte(control, core.SG_HB)
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

			if message == core.SG_Closed {
				s.logger.Warn("control channel has been closed by the client")
				go s.Restart()
				return
			}
		}
	}
}

func (s *TcpMuxTransport) tunnelListener(g *tcpMuxGen) {
	listener, err := core.ListenWithBuffers(
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

	// Backpack: the tunnel's encryption, run on every connection accepted.
	listener = s.config.Layer.Listener(listener, s.logger)

	defer s.holds.Hold()()
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

			tcpConn, ok := core.TCPOf(conn)
			if !ok {
				s.logger.Warnf("disarded non-TCP tunnel connection from %s", conn.RemoteAddr().String())
				conn.Close()
				continue
			}

			control := g.controlChannel.Get()
			if control != nil && control.RemoteAddr().(*net.TCPAddr).IP.String() != tcpConn.RemoteAddr().(*net.TCPAddr).IP.String() {
				s.logger.Debugf("suspicious packet from %v. expected address: %v. discarding packet...", tcpConn.RemoteAddr().(*net.TCPAddr).IP.String(), control.RemoteAddr().(*net.TCPAddr).IP.String())
				tcpConn.Close()
				continue
			}

			if !s.config.Nodelay {
				if err := tcpConn.SetNoDelay(s.config.Nodelay); err != nil {
					s.logger.Warnf("failed to set TCP_NODELAY for %s: %v", tcpConn.RemoteAddr().String(), err)
				} else {
					s.logger.Tracef("TCP_NODELAY disabled for %s", tcpConn.RemoteAddr().String())
				}
			}

			if err := tcpConn.SetKeepAlive(true); err != nil {
				s.logger.Warnf("failed to enable TCP keep-alive for %s: %v", tcpConn.RemoteAddr().String(), err)
			} else {
				s.logger.Tracef("TCP keep-alive enabled for %s", tcpConn.RemoteAddr().String())
			}
			if err := tcpConn.SetKeepAlivePeriod(s.config.KeepAlive); err != nil {
				s.logger.Warnf("failed to set TCP keep-alive period for %s: %v", tcpConn.RemoteAddr().String(), err)
			}

			// Speed edit: see core.MuxSocket.
			core.MuxSocket(conn)

			if control == nil {
				s.logger.Info("control channel not found, attempting to establish a new session")
				select {
				case g.handshakeChannel <- conn: // ok
				default:
					s.logger.Warnf("control channel handshake in progress...")
					conn.Close()
				}
				continue
			}

			session, err := smux.Client(conn, s.smuxConfig)
			if err != nil {
				s.logger.Errorf("failed to create MUX session for connection %s: %v", conn.RemoteAddr().String(), err)
				conn.Close()
				continue
			}

			select {
			case g.tunnelChannel <- session: // ok
			default:
				s.logger.Warnf("tunnel listener channel is full, discarding TCP connection from %s", conn.LocalAddr().String())
				session.Close()
			}
		}
	}
}

func (s *TcpMuxTransport) parsePortMappings(g *tcpMuxGen) {
	core.ParsePortMappings(s.config.Ports, s.logger, func(localAddr, remoteAddr string) {
		if s.config.AcceptUDP {
			go listenUDP(g.ctx, &s.holds, s.logger, localAddr, remoteAddr, func(flow *LocalAcceptUDPConn) bool {
				return s.queue(g, LocalTCPConn{udp: flow, remoteAddr: remoteAddr, timeCreated: time.Now().UnixMilli()})
			})
		}
		if !s.config.UDPOnly {
			s.localListener(g, localAddr, remoteAddr)
		}
	})
}

// queue hands c to the sessions, asking the kharej for another session when
// the ones open are full. It reports false when the queue itself is full.
func (s *TcpMuxTransport) queue(g *tcpMuxGen, c LocalTCPConn) bool {
	select {
	case g.localChannel <- c:
	default:
		return false
	}
	// +1 for stream counter
	atomic.AddInt32(&g.streamCounter, 1)

	if atomic.LoadInt32(&g.streamCounter) >= atomic.LoadInt32(&g.sessionCounter)*int32(s.config.MuxCon) {
		s.logger.Tracef("stream counter: %v, session counter: %v", atomic.LoadInt32(&g.streamCounter), atomic.LoadInt32(&g.sessionCounter))

		select { // Attempt to request a new connection
		case g.reqNewConnChan <- struct{}{}:
		default:
			s.logger.Warn("failed to request new connection. channel is full")
		}
	}
	return true
}

func (s *TcpMuxTransport) localListener(g *tcpMuxGen, localAddr string, remoteAddr string) {
	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		s.logger.Fatalf("failed to listen on %s: %v", localAddr, err)
		return
	}

	defer s.holds.Hold()()
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
			s.logger.Debugf("waiting for accept incoming connection on %s", listener.Addr().String())
			conn, err := listener.Accept()
			if err != nil {
				s.logger.Debugf("failed to accept connection on %s: %v", listener.Addr().String(), err)
				continue
			}

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

			if s.queue(g, LocalTCPConn{conn: conn, remoteAddr: remoteAddr, timeCreated: time.Now().UnixMilli()}) {
				s.logger.Debugf("accepted incoming TCP connection from %s", tcpConn.RemoteAddr().String())
			} else { // channel is full, discard the connection
				s.logger.Warnf("local listener channel is full, discarding TCP connection from %s", tcpConn.LocalAddr().String())
				conn.Close()
			}
		}
	}
}

func (s *TcpMuxTransport) handleLoop(g *tcpMuxGen) {
	for {
		select {
		case <-g.ctx.Done():
			return

		case session := <-g.tunnelChannel:
			// +1 for session counter
			atomic.AddInt32(&g.sessionCounter, 1)

			go s.handleSession(g, session)
		}
	}
}

func (s *TcpMuxTransport) handleSession(g *tcpMuxGen, session *smux.Session) {
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
				incomingConn.drop()

				// Decrement the counter
				atomic.AddInt32(&g.streamCounter, -1)
				<-counter
				continue
			}

			stream, err := session.OpenStream()
			if err != nil {
				s.handleSessionError(g, &incomingConn, err)
				return
			}

			// Send the target port over the tunnel connection
			if err := core.SendBinaryString(stream, incomingConn.target()); err != nil {
				s.logger.Tracef("failed to send address over stream: %v", err)
				// Put local connection back to local channel
				g.localChannel <- incomingConn
				continue
			}

			// Handle data exchange between connections
			go func() {
				incomingConn.relay(g.ctx, s.config.ProxyProtocol, stream, s.logger, g.usageMonitor, s.config.Sniffer)
				atomic.AddInt32(&g.streamCounter, -1)
				<-counter // read signal from the channel
			}()
		}
	}
}

func (s *TcpMuxTransport) handleSessionError(g *tcpMuxGen, incomingConn *LocalTCPConn, err error) {
	s.logger.Tracef("failed to handle session: %v", err)

	// decrease session value
	atomic.AddInt32(&g.sessionCounter, -1)

	// Put local connection back to local channel
	g.localChannel <- *incomingConn

	// Attempt to request a new connection
	select {
	case g.reqNewConnChan <- struct{}{}:
	default:
		s.logger.Warn("request new connection channel is full")
	}
}

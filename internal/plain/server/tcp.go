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
	"github.com/backpack/backpack/internal/utils/acceptloop"
	"github.com/backpack/backpack/internal/web"

	"github.com/sirupsen/logrus"
)

// TcpTransport is the plain tcp server: one control channel, and one tunnel
// connection per user connection, taken from the pool the client keeps
// dialled.
type TcpTransport struct {
	config       *TcpConfig
	parentctx    context.Context
	logger       *logrus.Logger
	restartMutex sync.Mutex
	gen          atomic.Pointer[tcpGen]
	status       core.Status
	holds        core.Holds
}

// tcpGen is one run: what a Restart re-initialises.
type tcpGen struct {
	ctx            context.Context
	cancel         context.CancelFunc
	tunnelChannel  chan net.Conn
	localChannel   chan LocalTCPConn
	reqNewConnChan chan struct{}
	controlChannel netControl
	usageMonitor   *web.Usage
	rtt            atomic.Int64 // in ms, for UDP
}

type TcpConfig struct {
	BindAddr    string
	Token       string
	SnifferLog  string
	Ports       []string
	Nodelay     bool
	Sniffer     bool
	KeepAlive   time.Duration
	Heartbeat   time.Duration // in seconds
	ChannelSize int
	WebPort     int
	AcceptUDP   bool
	// UDPOnly forwards the ports over UDP alone (port_protocol = "udp"): no
	// TCP listener is opened on them.
	UDPOnly       bool
	MSS           int
	SO_RCVBUF     int
	SO_SNDBUF     int
	ProxyProtocol bool
	// OnLegacyClaim is called when a client claims the control channel the
	// Backpack v2.0.0 way, so the tunnel can serve it on that engine.
	OnLegacyClaim func()
	// Admit takes a forwarded connection under the tunnel's limits
	// (max_connections, bandwidth_mbps) and returns the one to carry, or false
	// to refuse it. nil admits everything. Backpack.
	Admit func(ctx context.Context, conn net.Conn) (net.Conn, bool)
	// Layer is the tunnel's encryption (Backpack); nil is none.
	Layer *core.Layer
}

func NewTCPServer(parentCtx context.Context, config *TcpConfig, logger *logrus.Logger) *TcpTransport {
	server := &TcpTransport{
		config:    config,
		parentctx: parentCtx,
		logger:    logger,
	}
	server.gen.Store(server.newGen())
	// Backpack: the tunnel ending is the peer going away for the panel.
	context.AfterFunc(parentCtx, core.Unseated)
	return server
}

func (s *TcpTransport) newGen() *tcpGen {
	ctx, cancel := context.WithCancel(s.parentctx)
	return &tcpGen{
		ctx:            ctx,
		cancel:         cancel,
		tunnelChannel:  make(chan net.Conn, s.config.ChannelSize),
		localChannel:   make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan: make(chan struct{}, s.config.ChannelSize),
		usageMonitor:   web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx, s.config.SnifferLog, s.config.Sniffer, s.status.Get, s.logger),
	}
}

// Running reports whether a control channel is up.
func (s *TcpTransport) Running() bool { return s.status.Running() }

// Wait blocks until the transport's listeners are closed, or ctx ends.
func (s *TcpTransport) Wait(ctx context.Context) { s.holds.Wait(ctx) }

func (s *TcpTransport) Start() { s.start(s.gen.Load()) }

func (s *TcpTransport) start(g *tcpGen) {
	s.status.Set("Disconnected (TCP)")

	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}

	go s.tunnelListener(g)

	s.channelHandshake(g)

	if control := g.controlChannel.Get(); control != nil {
		s.status.Set("Connected (TCP)")
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

	old := s.gen.Load()
	old.cancel()

	if control := old.controlChannel.Get(); control != nil {
		control.Close()
	}
	core.Unseated()

	time.Sleep(2 * time.Second)

	// Backpack: by now the old generation's loops have returned.
	closeLeft(old.tunnelChannel, func(c net.Conn) { c.Close() })
	closeLeft(old.localChannel, LocalTCPConn.drop)

	// Backpack: a restart that finds the whole tunnel stopped builds nothing.
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

func (s *TcpTransport) channelHandshake(g *tcpGen) {
	for {
		select {
		case <-g.ctx.Done():
			return
		case conn := <-g.tunnelChannel:
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

			g.controlChannel.Set(conn)

			s.logger.Info("control channel successfully established.")
			return
		}
	}
}

func (s *TcpTransport) channelHandler(g *tcpGen) {
	ticker := time.NewTicker(s.config.Heartbeat)
	defer ticker.Stop()

	control := g.controlChannel.Get()

	messageChan := make(chan byte, 1)

	go func() {
		for {
			select {
			case <-g.ctx.Done():
				return
			default:
				message, err := core.ReceiveBinaryByte(control)
				if err != nil {
					s.logger.Error("failed to read from channel connection. ", err)
					go s.Restart()
					return
				}
				messageChan <- message
			}
		}
	}()

	// RTT measurment
	rtt := time.Now()
	err := core.SendBinaryByte(control, core.SG_RTT)
	if err != nil {
		s.logger.Error("failed to send RTT signal, attempting to restart server...")
		go s.Restart()
		return
	}

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

			} else if message == core.SG_RTT {
				measureRTT := time.Since(rtt)
				g.rtt.Store(measureRTT.Milliseconds())
				s.logger.Infof("Round Trip Time (RTT): %d ms", g.rtt.Load())
			}
		}
	}
}

func (s *TcpTransport) tunnelListener(g *tcpGen) {
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

func (s *TcpTransport) acceptTunnelConn(g *tcpGen, listener net.Listener) {
	var backoff acceptloop.Backoff
	for {
		select {
		case <-g.ctx.Done():
			return
		default:
			s.logger.Debugf("waiting for accept incoming tunnel connection on %s", listener.Addr().String())
			conn, err := listener.Accept()
			if err != nil {
				s.logger.Debugf("failed to accept tunnel connection on %s: %v", listener.Addr().String(), err)
				if !backoff.Fail(g.ctx) {
					return
				}
				continue
			}
			backoff = acceptloop.Backoff{}

			//discard any non tcp connection
			tcpConn, ok := core.TCPOf(conn)
			if !ok {
				s.logger.Warnf("disarded non-TCP tunnel connection from %s", conn.RemoteAddr().String())
				conn.Close()
				continue
			}

			// Drop all suspicious packets from other address rather than server
			if control := g.controlChannel.Get(); control != nil && control.RemoteAddr().(*net.TCPAddr).IP.String() != tcpConn.RemoteAddr().(*net.TCPAddr).IP.String() {
				s.logger.Debugf("suspicious packet from %v. expected address: %v. discarding packet...", tcpConn.RemoteAddr().(*net.TCPAddr).IP.String(), control.RemoteAddr().(*net.TCPAddr).IP.String())
				tcpConn.Close()
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

			select {
			case g.tunnelChannel <- conn:
			default: // The channel is full, do nothing
				s.logger.Warnf("tunnel listener channel is full, discarding TCP connection from %s", conn.LocalAddr().String())
				conn.Close()
			}
		}
	}
}

func (s *TcpTransport) parsePortMappings(g *tcpGen) {
	core.ParsePortMappings(s.config.Ports, s.logger, func(localAddr, remoteAddr string) {
		s.startListeners(g, localAddr, remoteAddr)
	})
}

func (s *TcpTransport) startListeners(g *tcpGen, localAddr, remoteAddr string) {
	if !s.config.UDPOnly {
		go s.localListener(g, localAddr, remoteAddr)
	}

	if s.config.AcceptUDP {
		go s.udpListener(g, localAddr, remoteAddr)
	}

	s.logger.Debugf("Started listening on %s, forwarding to %s", localAddr, remoteAddr)
}

func (s *TcpTransport) localListener(g *tcpGen, localAddr string, remoteAddr string) {
	// Backpack: a busy port is tried again, not the end of the process.
	listener := core.ListenPort(g.ctx, s.logger, localAddr)
	if listener == nil {
		return
	}

	defer s.holds.Hold()()
	defer listener.Close()

	s.logger.Infof("listener started successfully, listening on address: %s", listener.Addr().String())

	go s.acceptLocalConn(g, listener, remoteAddr)

	<-g.ctx.Done()
}

func (s *TcpTransport) acceptLocalConn(g *tcpGen, listener net.Listener, remoteAddr string) {
	var backoff acceptloop.Backoff
	for {
		select {
		case <-g.ctx.Done():
			return

		default:
			s.logger.Debugf("waiting for accept incoming connection on %s", listener.Addr().String())
			conn, err := listener.Accept()
			if err != nil {
				s.logger.Debugf("failed to accept connection on %s: %v", listener.Addr().String(), err)
				if !backoff.Fail(g.ctx) {
					return
				}
				continue
			}
			backoff = acceptloop.Backoff{}

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

			// Backpack: max_connections and bandwidth_mbps.
			if conn, ok = admit(s.config.Admit, g.ctx, conn, s.logger); !ok {
				continue
			}

			select {
			case g.localChannel <- LocalTCPConn{conn: conn, remoteAddr: remoteAddr, timeCreated: time.Now().UnixMilli()}:

				select {
				case g.reqNewConnChan <- struct{}{}:
					// Successfully requested a new connection
				default:
					s.logger.Warn("channel is full, cannot request a new connection")
				}

				s.logger.Debugf("accepted incoming TCP connection from %s", tcpConn.RemoteAddr().String())

			default: // channel is full, discard the connection
				s.logger.Warnf("channel with listener %s is full, discarding TCP connection from %s", listener.Addr().String(), tcpConn.LocalAddr().String())
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
					break loop
				}

				select {
				case <-g.ctx.Done():
					return

				case tunnelConn := <-g.tunnelChannel:
					// Send the target addr over the connection
					if err := core.SendBinaryTransportString(tunnelConn, localConn.remoteAddr, core.SG_TCP); err != nil {
						s.logger.Errorf("%v", err)
						tunnelConn.Close()
						continue loop
					}

					// Handle data exchange between connections
					go core.TCPConnectionHandler(g.ctx, s.config.ProxyProtocol, localConn.conn, core.Counted(tunnelConn), s.logger, g.usageMonitor, localConn.conn.LocalAddr().(*net.TCPAddr).Port, s.config.Sniffer)
					break loop
				}
			}
		}
	}
}

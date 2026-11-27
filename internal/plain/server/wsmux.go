package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/plain/core"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/backpack/backpack/internal/web"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
	"github.com/xtaci/smux"
)

// WsMuxTransport is the plain wsmux and wssmux server: /channel is the
// control channel, and each /tunnel websocket is an smux session carrying at
// most MuxCon user connections.
type WsMuxTransport struct {
	config       *WsMuxConfig
	smuxConfig   *smux.Config
	parentctx    context.Context
	logger       *logrus.Logger
	restartMutex sync.Mutex
	gen          atomic.Pointer[wsMuxGen]
	status       core.Status
	holds        core.Holds
}

type wsMuxGen struct {
	ctx            context.Context
	cancel         context.CancelFunc
	tunnelChannel  chan *smux.Session
	localChannel   chan LocalTCPConn
	reqNewConnChan chan struct{}
	controlChannel wsControl
	usageMonitor   *web.Usage
	streamCounter  int32
	sessionCounter int32
}

type WsMuxConfig struct {
	BindAddr    string
	SnifferLog  string
	TLSCertFile string // Path to the TLS certificate file
	TLSKeyFile  string // Path to the TLS key file
	Token       string
	Ports       []string
	Nodelay     bool
	Sniffer     bool
	KeepAlive   time.Duration
	Heartbeat   time.Duration // in seconds
	ChannelSize int
	WebPort     int
	Mode        config.TransportType // wsmux or wssmux

	MuxCon           int
	MuxVersion       int
	MaxFrameSize     int
	MaxReceiveBuffer int
	MaxStreamBuffer  int
	ProxyProtocol    bool
	// AcceptUDP and UDPOnly: see TcpConfig. A flow is a stream of its own.
	AcceptUDP bool
	UDPOnly   bool

	// Backpack: where a wss certificate comes from when no files are given
	// (Let's Encrypt, or one generated for the run). See network.TLSSettings.
	ACMEDomain   string
	ACMEEmail    string
	ACMECacheDir string

	// OnLegacyClaim: see WsConfig.OnLegacyClaim.
	OnLegacyClaim func()
	// Admit takes a forwarded connection under the tunnel's limits
	// (max_connections, bandwidth_mbps) and returns the one to carry, or false
	// to refuse it. nil admits everything. Backpack.
	Admit func(ctx context.Context, conn net.Conn) (net.Conn, bool)
	// Layer is the tunnel's encryption (Backpack); nil is none.
	Layer *core.Layer
}

func NewWSMuxServer(parentCtx context.Context, config *WsMuxConfig, logger *logrus.Logger) *WsMuxTransport {
	server := &WsMuxTransport{
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

func (s *WsMuxTransport) newGen() *wsMuxGen {
	ctx, cancel := context.WithCancel(s.parentctx)
	return &wsMuxGen{
		ctx:            ctx,
		cancel:         cancel,
		tunnelChannel:  make(chan *smux.Session, s.config.ChannelSize),
		localChannel:   make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan: make(chan struct{}, s.config.ChannelSize),
		usageMonitor:   web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx, s.config.SnifferLog, s.config.Sniffer, s.status.Get, s.logger),
	}
}

// Running reports whether a control channel is up.
func (s *WsMuxTransport) Running() bool { return s.status.Running() }

// Wait blocks until the transport's listeners are closed, or ctx ends.
func (s *WsMuxTransport) Wait(ctx context.Context) { s.holds.Wait(ctx) }

func (s *WsMuxTransport) Start() { s.start(s.gen.Load()) }

func (s *WsMuxTransport) start(g *wsMuxGen) {
	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}

	s.status.Set(fmt.Sprintf("Disconnected (%s)", s.config.Mode))

	go s.tunnelListener(g)
}

func (s *WsMuxTransport) Restart() {
	if !s.restartMutex.TryLock() {
		s.logger.Warn("server restart already in progress, skipping restart attempt")
		return
	}
	defer s.restartMutex.Unlock()

	s.logger.Info("restarting server...")

	level := s.logger.Level
	s.logger.SetLevel(logrus.FatalLevel)

	old := s.gen.Load()
	old.cancel()

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

func (s *WsMuxTransport) channelHandler(g *wsMuxGen) {
	ticker := time.NewTicker(s.config.Heartbeat)
	defer ticker.Stop()

	control := g.controlChannel.Get()

	messageChan := make(chan byte, 10)

	go func() {
		for {
			select {
			case <-g.ctx.Done():
				return

			default:
				_, msg, err := control.ReadMessage()
				if err != nil {
					s.logger.Error("failed to read from channel connection. ", err)
					go s.Restart()
					return
				}
				messageChan <- msg[0]
			}
		}
	}()

	for {
		select {
		case <-g.ctx.Done():
			_ = control.WriteMessage(websocket.BinaryMessage, []byte{core.SG_Closed})
			return
		case <-g.reqNewConnChan:
			err := control.WriteMessage(websocket.BinaryMessage, []byte{core.SG_Chan})
			if err != nil {
				s.logger.Error("failed to send request new connection signal. ", err)
				go s.Restart()
				return
			}

		case <-ticker.C:
			err := control.WriteMessage(websocket.BinaryMessage, []byte{core.SG_HB})
			if err != nil {
				s.logger.Errorf("failed to send heartbeat signal. Error: %v.", err)
				go s.Restart()
				return
			}
			s.logger.Debug("heartbeat signal sent successfully")

		case msg, ok := <-messageChan:
			if !ok {
				s.logger.Error("channel closed, likely due to an error in WebSocket read")
				return
			}
			switch msg {
			case core.SG_HB:
				s.logger.Trace("heartbeat signal received successfully")

			case core.SG_Closed:
				s.logger.Warn("control channel has been closed by the client")
				s.Restart()
				return

			default:
				s.logger.Errorf("unexpected response from channel: %v", msg)
				go s.Restart()
				return
			}
		}
	}
}

func (s *WsMuxTransport) tunnelListener(g *wsMuxGen) {
	addr := s.config.BindAddr
	upgrader := websocket.Upgrader{
		// Speed edit: 64 KiB pooled write buffers (see core.WSWriteBuffers).
		ReadBufferSize:   16 * 1024,
		WriteBufferSize:  64 * 1024,
		WriteBufferPool:  core.WSWriteBuffers,
		HandshakeTimeout: 45 * time.Second,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	server := &http.Server{
		Addr:        addr,
		IdleTimeout: -1,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.logger.Tracef("received http request from %s", r.RemoteAddr)

			authHeader := r.Header.Get("Authorization")
			if r.URL.Path == "/channel" && s.config.OnLegacyClaim != nil && legacyWSClaim(r, authHeader, s.config.Token) {
				s.logger.Info("the kharej runs Backpack v2.0.0: serving this tunnel on its engine until it is updated")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				s.config.OnLegacyClaim()
				return
			}
			if !s.config.Layer.BearerOK(authHeader, s.config.Token) {
				s.logger.Warnf("unauthorized request from %s, closing connection", r.RemoteAddr)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			raw, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				s.logger.Errorf("failed to upgrade connection from %s: %v", r.RemoteAddr, err)
				return
			}

			if r.URL.Path == "/channel" {
				// Backpack: the tunnel's encryption on the control messages.
				conn, err := s.config.Layer.WSServer(raw)
				if err != nil {
					s.logger.Debugf("%s handshake failed from %s: %v", s.config.Layer.Mode(), r.RemoteAddr, err)
					return
				}
				if control := g.controlChannel.Get(); control != nil {
					s.logger.Warn("new control channel requested.")
					control.Close()
					conn.Close()
					go s.Restart()
					return
				}
				g.controlChannel.Set(conn)

				s.logger.Info("control channel established successfully")
				core.Seated(conn.RemoteAddr().String())

				numCPU := runtime.NumCPU()
				if numCPU > 4 {
					numCPU = 4 // Max allowed handler is 4
				}

				go s.channelHandler(g)
				go s.parsePortMappings(g)

				s.logger.Infof("starting %d handle loops on each CPU thread", numCPU)

				for i := 0; i < numCPU; i++ {
					go s.handleLoop(g)
				}

				s.status.Set(fmt.Sprintf("Connected (%s)", s.config.Mode))

			} else if strings.HasPrefix(r.URL.Path, "/tunnel") {
				// Backpack: the tunnel's encryption on the raw stream smux runs on.
				stream, err := s.config.Layer.Server(raw.NetConn())
				if err != nil {
					s.logger.Debugf("%s handshake failed from %s: %v", s.config.Layer.Mode(), r.RemoteAddr, err)
					return
				}
				conn := raw
				session, err := smux.Client(stream, s.smuxConfig)
				if err != nil {
					s.logger.Errorf("failed to create MUX session for connection %s: %v", conn.RemoteAddr().String(), err)
					conn.Close()
					return
				}
				select {
				case g.tunnelChannel <- session: // ok
				default:
					s.logger.Warnf("tunnel listener channel is full, discarding TCP connection from %s", conn.LocalAddr().String())
					conn.Close()
				}
			}
		}),
	}

	release := s.holds.Hold()
	served := make(chan struct{})

	if s.config.Mode == config.WSMUX {
		go func() {
			defer close(served)
			s.logger.Infof("%s server starting, listening on %s", s.config.Mode, addr)
			if g.controlChannel.Get() == nil {
				s.logger.Infof("waiting for %s control channel connection", s.config.Mode)
			}
			// Speed edit: the listener carries the tunnel's socket bound
			// (core.Listen) where net/http opened a plain one.
			ln, err := core.Listen(addr, true)
			if err != nil {
				s.logger.Errorf("failed to listen on %s: %v", addr, err)
				return
			}
			if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
				s.logger.Errorf("failed to listen on %s: %v", addr, err)
			}
		}()
	} else {
		// Backpack: the certificate files when given;
		// otherwise Let's Encrypt or a generated one (see network.TLSSettings).
		tlsCfg, err := network.ServerTLSConfig(s.tlsSettings(), s.logger.Warnf)
		if err != nil {
			s.logger.Fatalf("failed to listen on %s: %v", addr, err)
		}
		server.TLSConfig = tlsCfg
		go func() {
			defer close(served)
			s.logger.Infof("%s server starting, listening on %s", s.config.Mode, addr)
			if g.controlChannel.Get() == nil {
				s.logger.Infof("waiting for %s control channel connection", s.config.Mode)
			}
			ln, err := core.Listen(addr, true)
			if err != nil {
				s.logger.Errorf("failed to listen on %s: %v", addr, err)
				return
			}
			if err := server.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
				s.logger.Errorf("failed to listen on %s: %v", addr, err)
			}
		}()
	}

	<-g.ctx.Done()

	if control := g.controlChannel.Get(); control != nil {
		control.Close()
	}

	s.logger.Infof("shutting down the websocket server on %s", addr)
	if err := server.Shutdown(context.Background()); err != nil {
		s.logger.Errorf("Failed to gracefully shutdown the server: %v", err)
	}

	<-served
	release()
}

// tlsSettings is where a wss listener's certificate comes from.
func (s *WsMuxTransport) tlsSettings() network.TLSSettings {
	return tlsSettings(s.config.BindAddr, s.config.TLSCertFile, s.config.TLSKeyFile, s.config.ACMEDomain, s.config.ACMEEmail, s.config.ACMECacheDir)
}

func (s *WsMuxTransport) parsePortMappings(g *wsMuxGen) {
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
func (s *WsMuxTransport) queue(g *wsMuxGen, c LocalTCPConn) bool {
	select {
	case g.localChannel <- c:
	default:
		return false
	}
	// +1 for stream counter
	atomic.AddInt32(&g.streamCounter, 1)

	if atomic.LoadInt32(&g.streamCounter) >= atomic.LoadInt32(&g.sessionCounter)*int32(s.config.MuxCon) {
		s.logger.Tracef("stream counter: %v, session counter: %v", atomic.LoadInt32(&g.streamCounter), atomic.LoadInt32(&g.sessionCounter))
		select {
		case g.reqNewConnChan <- struct{}{}:
		default:
			s.logger.Warn("failed to request new connection. channel is full")
		}
	}
	return true
}

func (s *WsMuxTransport) localListener(g *wsMuxGen, localAddr string, remoteAddr string) {
	// Backpack: a busy port is tried again, not the end of the process.
	portListener := core.ListenPort(g.ctx, s.logger, localAddr)
	if portListener == nil {
		return
	}

	defer s.holds.Hold()()
	defer portListener.Close()

	go s.acceptLocalConn(g, portListener, remoteAddr)

	s.logger.Infof("listener started successfully, listening on address: %s", portListener.Addr().String())

	<-g.ctx.Done()
}

func (s *WsMuxTransport) acceptLocalConn(g *wsMuxGen, listener net.Listener, remoteAddr string) {
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
				s.logger.Warnf("disarded non-TCP connection from %s", conn.RemoteAddr().String())
				conn.Close()
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

			// Backpack: max_connections and bandwidth_mbps.
			if conn, ok = admit(s.config.Admit, g.ctx, conn, s.logger); !ok {
				continue
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

func (s *WsMuxTransport) handleLoop(g *wsMuxGen) {
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

func (s *WsMuxTransport) handleSession(g *wsMuxGen, session *smux.Session) {
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

				atomic.AddInt32(&g.streamCounter, -1)
				<-counter
				continue
			}

			stream, err := session.OpenStream()
			if err != nil {
				s.handleSessionError(g, &incomingConn, err)
				return
			}

			if err := core.SendBinaryString(stream, incomingConn.target()); err != nil {
				s.logger.Tracef("failed to send address over stream: %v", err)
				g.localChannel <- incomingConn
				continue
			}

			go func() {
				incomingConn.relay(g.ctx, s.config.ProxyProtocol, stream, s.logger, g.usageMonitor, s.config.Sniffer)
				atomic.AddInt32(&g.streamCounter, -1)
				<-counter // read signal from the channel
			}()
		}
	}
}

func (s *WsMuxTransport) handleSessionError(g *wsMuxGen, incomingConn *LocalTCPConn, err error) {
	s.logger.Tracef("failed to handle session: %v", err)

	atomic.AddInt32(&g.sessionCounter, -1)

	g.localChannel <- *incomingConn

	select {
	case g.reqNewConnChan <- struct{}{}:
	default:
		s.logger.Warn("request new connection channel is full")
	}
}

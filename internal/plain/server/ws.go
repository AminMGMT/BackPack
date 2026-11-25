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
)

// WsTransport is the plain ws and wss server: /channel is the control
// channel, and each /tunnel websocket carries one user connection.
type WsTransport struct {
	config       *WsConfig
	parentctx    context.Context
	logger       *logrus.Logger
	restartMutex sync.Mutex
	gen          atomic.Pointer[wsGen]
	status       core.Status
	holds        core.Holds
}

type wsGen struct {
	ctx            context.Context
	cancel         context.CancelFunc
	tunnelChannel  chan TunnelChannel
	localChannel   chan LocalTCPConn
	reqNewConnChan chan struct{}
	controlChannel wsControl
	usageMonitor   *web.Usage
}

type WsConfig struct {
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
	Mode        config.TransportType // ws or wss

	// Backpack: where a wss certificate comes from when no files are given
	// (Let's Encrypt, or one generated for the run). See network.TLSSettings.
	ACMEDomain   string
	ACMEEmail    string
	ACMECacheDir string

	// OnLegacyClaim is called when a client claims /channel the Backpack
	// v2.0.0 way, so the tunnel can serve it on that engine.
	OnLegacyClaim func()
	// Layer is the tunnel's encryption (Backpack); nil is none.
	Layer *core.Layer
}

func NewWSServer(parentCtx context.Context, config *WsConfig, logger *logrus.Logger) *WsTransport {
	server := &WsTransport{
		config:    config,
		parentctx: parentCtx,
		logger:    logger,
	}
	server.gen.Store(server.newGen())
	context.AfterFunc(parentCtx, core.Unseated)
	return server
}

func (s *WsTransport) newGen() *wsGen {
	ctx, cancel := context.WithCancel(s.parentctx)
	return &wsGen{
		ctx:            ctx,
		cancel:         cancel,
		tunnelChannel:  make(chan TunnelChannel, s.config.ChannelSize),
		localChannel:   make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan: make(chan struct{}, s.config.ChannelSize),
		usageMonitor:   web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx, s.config.SnifferLog, s.config.Sniffer, s.status.Get, s.logger),
	}
}

// Running reports whether a control channel is up.
func (s *WsTransport) Running() bool { return s.status.Running() }

// Wait blocks until the transport's listeners are closed, or ctx ends.
func (s *WsTransport) Wait(ctx context.Context) { s.holds.Wait(ctx) }

func (s *WsTransport) Start() { s.start(s.gen.Load()) }

func (s *WsTransport) start(g *wsGen) {
	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}

	s.status.Set(fmt.Sprintf("Disconnected (%s)", s.config.Mode))

	go s.tunnelListener(g)
}

func (s *WsTransport) Restart() {
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

func (s *WsTransport) channelHandler(g *wsGen) {
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

func (s *WsTransport) tunnelListener(g *wsGen) {
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
			// Backpack: the tunnel's encryption on the websocket's messages.
			conn, err := s.config.Layer.WSServer(raw)
			if err != nil {
				s.logger.Debugf("%s handshake failed from %s: %v", s.config.Layer.Mode(), r.RemoteAddr, err)
				return
			}

			if r.URL.Path == "/channel" {
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
				wsConn := TunnelChannel{
					conn: conn,
					ping: make(chan struct{}),
					mu:   &sync.Mutex{},
				}
				select {
				case g.tunnelChannel <- wsConn:
					go s.keepAlive(g, &wsConn)
					s.logger.Debugf("websocket connection accepted from %s", conn.RemoteAddr().String())
				default:
					s.logger.Warnf("websocket tunnel channel is full, closing connection from %s", conn.RemoteAddr().String())
					conn.Close()
				}
			}
		}),
	}

	release := s.holds.Hold()
	served := make(chan struct{})

	if s.config.Mode == config.WS {
		go func() {
			defer close(served)
			s.logger.Infof("ws server starting, listening on %s", addr)
			if g.controlChannel.Get() == nil {
				s.logger.Info("waiting for ws control channel connection")
			}
			// Speed edit: the listener carries the tunnel's socket bound
			// (core.Listen) where net/http opened a plain one.
			ln, err := core.Listen(addr, false)
			if err != nil {
				s.logger.Fatalf("failed to listen on %s: %v", addr, err)
				return
			}
			if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
				s.logger.Fatalf("failed to listen on %s: %v", addr, err)
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
			s.logger.Infof("wss server starting, listening on %s", addr)
			if g.controlChannel.Get() == nil {
				s.logger.Info("waiting for wss control channel connection")
			}
			ln, err := core.Listen(addr, false)
			if err != nil {
				s.logger.Fatalf("failed to listen on %s: %v", addr, err)
				return
			}
			if err := server.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
				s.logger.Fatalf("failed to listen on %s: %v", addr, err)
			}
		}()
	}

	<-g.ctx.Done()

	s.logger.Infof("shutting down the webSocket server on %s", addr)
	if err := server.Shutdown(context.Background()); err != nil {
		s.logger.Errorf("Failed to gracefully shutdown the server: %v", err)
	}

	if control := g.controlChannel.Get(); control != nil {
		control.Close()
	}

	<-served
	release()
}

// tlsSettings is where a wss listener's certificate comes from.
func (s *WsTransport) tlsSettings() network.TLSSettings {
	return tlsSettings(s.config.BindAddr, s.config.TLSCertFile, s.config.TLSKeyFile, s.config.ACMEDomain, s.config.ACMEEmail, s.config.ACMECacheDir)
}

func (s *WsTransport) parsePortMappings(g *wsGen) {
	core.ParsePortMappings(s.config.Ports, s.logger, func(localAddr, remoteAddr string) {
		s.localListener(g, localAddr, remoteAddr)
	})
}

func (s *WsTransport) localListener(g *wsGen, localAddr string, remoteAddr string) {
	portListener, err := net.Listen("tcp", localAddr)
	if err != nil {
		s.logger.Fatalf("failed to start listener on %s: %v", localAddr, err)
		return
	}

	defer s.holds.Hold()()
	defer portListener.Close()

	s.logger.Infof("listener started successfully, listening on address: %s", portListener.Addr().String())

	go s.acceptLocalConn(g, portListener, remoteAddr)

	<-g.ctx.Done()
}

func (s *WsTransport) acceptLocalConn(g *wsGen, listener net.Listener, remoteAddr string) {
	for {
		select {
		case <-g.ctx.Done():
			return

		default:
			s.logger.Debugf("waiting to accept incoming connection on %s", listener.Addr().String())
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

			select {
			case g.localChannel <- LocalTCPConn{conn: conn, remoteAddr: remoteAddr, timeCreated: time.Now().UnixMilli()}:

				select {
				case g.reqNewConnChan <- struct{}{}:
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

func (s *WsTransport) handleLoop(g *wsGen) {
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
				case tunnelConnection := <-g.tunnelChannel:
					close(tunnelConnection.ping)
					tunnelConnection.mu.Lock()
					if err := tunnelConnection.conn.WriteMessage(websocket.TextMessage, []byte(localConn.remoteAddr)); err != nil {
						s.logger.Debugf("%v", err) // failed to send port number
						tunnelConnection.conn.Close()
						continue loop
					}
					go core.WSConnectionHandler(g.ctx, tunnelConnection.conn, localConn.conn, s.logger, g.usageMonitor, localConn.conn.LocalAddr().(*net.TCPAddr).Port, s.config.Sniffer)
					break loop
				}
			}
		}
	}
}

func (s *WsTransport) keepAlive(g *wsGen, conn *TunnelChannel) {
	ticker := time.NewTicker(s.config.Heartbeat) // Send periodic pings to the client

	defer ticker.Stop()

	for {
		select {
		case <-g.ctx.Done():
			conn.conn.Close()
			return
		case <-conn.ping:
			s.logger.Trace("ping channel closed")
			return
		case <-ticker.C:
			locked := conn.mu.TryLock()
			if !locked {
				s.logger.Trace("write operation in progress, stopping pingSender")
				return
			}

			if err := conn.conn.WriteMessage(websocket.BinaryMessage, []byte{core.SG_Ping}); err != nil {
				conn.mu.Unlock()
				conn.conn.Close()
				return
			}
			conn.mu.Unlock()
			s.logger.Trace("ping sent to the client")
		}
	}
}

// tlsSettings describes a wss listener's certificate source.
func tlsSettings(bindAddr, certFile, keyFile, acmeDomain, acmeEmail, acmeCacheDir string) network.TLSSettings {
	return network.TLSSettings{
		CertFile:       certFile,
		KeyFile:        keyFile,
		ACMEDomain:     acmeDomain,
		ACMEEmail:      acmeEmail,
		ACMECacheDir:   acmeCacheDir,
		SelfSignedHost: certHost(bindAddr),
	}
}

// certHost names a generated certificate after the address it is served on.
func certHost(bindAddr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(bindAddr))
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		return "localhost"
	}
	return host
}

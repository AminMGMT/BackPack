package transport

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/backpack/backpack/internal/web"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// wsGen is the state of a single run of the transport: the context that ends
// when the run does, and the channels its goroutines pass work over. Restart
// builds a fresh set for the next run, so carrying them here keeps a goroutine
// that outlives its run from reaching into the run that replaced it.
type wsGen struct {
	ctx            context.Context
	tunnelChannel  chan TunnelChannel
	localChannel   chan LocalTCPConn
	reqNewConnChan chan struct{}
	usageMonitor   *web.Usage
}

type WsTransport struct {
	// The status shown in the panel. Behind a lock because the run being
	// replaced and the run replacing it both write it. See tunnelStatus.
	status    tunnelStatus
	config    *WsConfig
	parentctx context.Context
	// The current run. Replaced by Restart while the previous run's
	// goroutines are still reading it, so it lives behind a lock.
	run            runState
	logger         *logrus.Logger
	tunnelChannel  chan TunnelChannel
	localChannel   chan LocalTCPConn
	reqNewConnChan chan struct{}
	controlChannel wsControl
	restartMutex   sync.Mutex
	usageMonitor   *web.Usage
	limits         *limiter
}

type WsConfig struct {
	BindAddr     string
	SnifferLog   string
	TLSCertFile  string // Path to the TLS certificate file
	TLSKeyFile   string // Path to the TLS key file
	ACMEDomain   string // non-empty switches to Let's Encrypt for this domain
	ACMEEmail    string
	ACMECacheDir string
	Token        string
	SimpleAuth   bool
	Ports        []string
	AcceptUDP    bool
	Nodelay      bool
	Sniffer      bool
	KeepAlive    time.Duration
	Heartbeat    time.Duration // in seconds
	ChannelSize  int
	WebPort      int
	Mode         config.TransportType // ws or wss

	// MSS caps the largest TCP segment the accepted tunnel connections send.
	// Zero leaves it to the kernel, which is the default; it is set where the
	// path silently drops full-sized packets. See manage.SetMSS.
	MSS int
	// MaxConnections caps simultaneous forwarded connections (0 = unlimited).
	MaxConnections int
	// BandwidthMbps caps total tunnel throughput (0 = unlimited).
	BandwidthMbps int
}

func NewWSServer(parentCtx context.Context, config *WsConfig, logger *logrus.Logger) *WsTransport {
	// Create a derived context from the parent context
	ctx, cancel := context.WithCancel(parentCtx)

	// Initialize the TcpTransport struct
	server := &WsTransport{
		config:         config,
		parentctx:      parentCtx,
		logger:         logger,
		tunnelChannel:  make(chan TunnelChannel, config.ChannelSize),
		localChannel:   make(chan LocalTCPConn, config.ChannelSize),
		reqNewConnChan: make(chan struct{}, config.ChannelSize),
		limits:         newLimiter(Limits{MaxConnections: config.MaxConnections, BandwidthMbps: config.BandwidthMbps}),
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
func (s *WsTransport) Start() {
	s.start(&wsGen{
		ctx:            s.run.context(),
		tunnelChannel:  s.tunnelChannel,
		localChannel:   s.localChannel,
		reqNewConnChan: s.reqNewConnChan,
		usageMonitor:   s.usageMonitor,
	})
}

// start runs one generation of the transport. Everything it needs is in g:
// nothing in here reaches back for a field that the next Restart is entitled to
// replace while this run is still using it.
func (s *WsTransport) start(g *wsGen) {
	// for  webui
	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}

	s.status.set(fmt.Sprintf("Disconnected (%s)", s.config.Mode))

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

	s.run.stop()

	// Close control channel connection
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
	g := &wsGen{
		ctx:            ctx,
		tunnelChannel:  make(chan TunnelChannel, s.config.ChannelSize),
		localChannel:   make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan: make(chan struct{}, s.config.ChannelSize),
		usageMonitor:   web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx, s.config.SnifferLog, s.config.Sniffer, s.status.get, s.logger),
	}

	// Re-initialize variables
	s.controlChannel.Clear()
	s.status.set("")

	// set the log level again
	s.logger.SetLevel(level)

	go s.start(g)
}

func (s *WsTransport) channelHandler(g *wsGen) {
	ticker := time.NewTicker(s.config.Heartbeat)
	defer ticker.Stop()

	// Channel to receive the message or error
	messageChan := make(chan byte, 10)

	// Separate goroutine to continuously listen for messages
	go func() {
		for {
			select {
			case <-g.ctx.Done():
				return

			default:
				messageType, msg, err := s.controlChannel.Get().ReadMessage()
				// Exit if there's an error
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
				signal, ok := utils.WebSocketSignal(messageType, msg)
				if !ok {
					s.logger.Warnf("ignoring a malformed control frame (type %d, %d bytes)", messageType, len(msg))
					continue
				}
				messageChan <- signal
			}
		}
	}()

	for {
		select {
		case <-g.ctx.Done():
			_ = s.controlChannel.Get().WriteMessage(websocket.BinaryMessage, []byte{utils.SG_Closed})
			return
		case <-g.reqNewConnChan:
			err := s.controlChannel.Get().WriteMessage(websocket.BinaryMessage, []byte{utils.SG_Chan})
			if err != nil {
				s.logger.Error("failed to send request new connection signal. ", err)
				go s.Restart()
				return
			}

		case <-ticker.C:
			err := s.controlChannel.Get().WriteMessage(websocket.BinaryMessage, []byte{utils.SG_HB})
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
			case utils.SG_HB:
				s.logger.Trace("heartbeat signal received successfully")

			case utils.SG_Closed:
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
		ReadBufferSize:   16 * 1024,
		WriteBufferSize:  16 * 1024,
		HandshakeTimeout: 45 * time.Second,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	// Create an HTTP server
	server := &http.Server{
		Addr:        addr,
		IdleTimeout: -1,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.logger.Tracef("received http request from %s", r.RemoteAddr)

			// Only a genuine tunnel connection (websocket upgrade, tunnel path,
			// valid credential) is served as a tunnel. Everything else — a
			// browser, a scanner, a probe with the wrong token — gets the decoy
			// website, so on 443 this looks like an ordinary HTTPS site rather
			// than a tunnel that answers with 401.
			if !isTunnelRequest(r, s.config.Token, s.config.SimpleAuth) {
				serveDecoy(w)
				return
			}

			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				s.logger.Errorf("failed to upgrade connection from %s: %v", r.RemoteAddr, err)
				return
			}

			if r.URL.Path == "/channel" {
				if s.controlChannel.IsSet() {
					s.logger.Warn("new control channel requested.")
					s.controlChannel.Close()
					conn.Close()
					go s.Restart()
					return
				}
				s.controlChannel.Set(conn)

				s.logger.Info("control channel established successfully")

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

				s.status.set(fmt.Sprintf("Connected (%s)", s.config.Mode))

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

	// The listener is built here rather than left to ListenAndServe, which
	// opens a plain socket with none of the tunnel's options on it. That is
	// what made the MSS clamp a no-op on this transport: the config carried it,
	// the config file showed it, and nothing ever put it on a socket — so a
	// path that drops full-sized packets stayed broken after the operator had
	// applied the fix the diagnostics asked for.
	ln, err := network.ListenWithBuffers(
		"tcp",
		addr,
		0, // the websocket transports have never pinned the socket buffers
		0,
		s.config.MSS,
		s.config.KeepAlive,
		!s.config.Nodelay,
	)
	if err != nil {
		s.logger.Fatalf("failed to listen on %s: %v", addr, err)
		return
	}

	if s.config.Mode == config.WS {
		go func() {
			s.logger.Infof("ws server starting, listening on %s", addr)
			if !s.controlChannel.IsSet() {
				s.logger.Info("waiting for ws control channel connection")
			}
			if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
				s.logger.Fatalf("failed to listen on %s: %v", addr, err)
			}
		}()
	} else {
		// Built before the goroutine starts so a bad certificate or an
		// unwritable ACME cache is reported here, at startup, instead of
		// surfacing later as handshake failures on a listener that is up.
		tlsCfg, err := network.ServerTLSConfig(s.tlsSettings(), s.logger.Warnf)
		if err != nil {
			ln.Close()
			s.logger.Fatalf("failed to set up TLS on %s: %v", addr, err)
		}
		server.TLSConfig = tlsCfg

		go func() {
			s.logger.Infof("wss server starting, listening on %s", addr)
			if !s.controlChannel.IsSet() {
				s.logger.Info("waiting for wss control channel connection")
			}
			// Empty paths: the certificate comes from TLSConfig.GetCertificate,
			// which is what allows a renewed certificate to be picked up
			// without restarting the tunnel.
			if err := server.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
				s.logger.Fatalf("failed to listen on %s: %v", addr, err)
			}
		}()
	}

	<-g.ctx.Done()

	// Gracefully shutdown the server
	s.logger.Infof("shutting down the webSocket server on %s", addr)
	if err := server.Shutdown(context.Background()); err != nil {
		s.logger.Errorf("Failed to gracefully shutdown the server: %v", err)
	}

	if s.controlChannel.IsSet() {
		s.controlChannel.Close()
	}

}

func (s *WsTransport) parsePortMappings(g *wsGen) {
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
				localAddr = listenAddrFor(localPortOrRange)
			}
		} else {
			s.logger.Fatalf("invalid port mapping format: %s", portMapping)
		}
		// Start listeners for single port
		go s.localListener(g, localAddr, remoteAddr)
	}
}

func (s *WsTransport) localListener(g *wsGen, localAddr string, remoteAddr string) {
	portListener, err := net.Listen("tcp", localAddr)
	if err != nil {
		s.logger.Fatalf("failed to start listener on %s: %v", localAddr, err)
		return
	}

	//close local listener after context cancellation
	defer portListener.Close()

	s.logger.Infof("listener started successfully, listening on address: %s", portListener.Addr().String())

	go s.acceptLocalConn(g, portListener, remoteAddr)
	// The same forwarded port, carrying datagrams. A flow is handed over as a
	// net.Conn, so from here down it is paired with a tunnel connection, piped,
	// counted and torn down by exactly the code that does it for TCP.
	if s.config.AcceptUDP {
		go startUDPForward(g.ctx, s.logger, localAddr, remoteAddr,
			udpAdmitter(g.localChannel, g.reqNewConnChan, s.limits))
	}

	<-g.ctx.Done()
}

func (s *WsTransport) acceptLocalConn(g *wsGen, listener net.Listener, remoteAddr string) {
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

			// trying to enable tcpnodelay
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
					// Handle data exchange between connections
					go func(tunnelConn TunnelChannel, localConn LocalTCPConn) {
						// Free the connection slot once the transfer ends, or
						// the limit would fill up permanently.
						defer s.limits.release()
						handlers.WSConnectionHandler(g.ctx, tunnelConn.conn, localConn.conn, s.logger, g.usageMonitor, localForwardPort(localConn.conn), s.config.Sniffer)
					}(tunnelConnection, localConn)
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
			// Try to acquire the lock without blocking
			locked := conn.mu.TryLock()
			if !locked {
				// If the lock is held by another operation, stop the pingSender
				s.logger.Trace("write operation in progress, stopping pingSender")
				return
			}

			if err := conn.conn.WriteMessage(websocket.BinaryMessage, []byte{utils.SG_Ping}); err != nil {
				conn.mu.Unlock()
				conn.conn.Close()
				return
			}
			conn.mu.Unlock()
			s.logger.Trace("ping sent to the client")
		}
	}
}

// tlsSettings describes how this listener should obtain its certificate:
// Let's Encrypt when a domain is configured, otherwise the PEM pair on disk.
func (s *WsTransport) tlsSettings() network.TLSSettings {
	return network.TLSSettings{
		CertFile:     s.config.TLSCertFile,
		KeyFile:      s.config.TLSKeyFile,
		ACMEDomain:   s.config.ACMEDomain,
		ACMEEmail:    s.config.ACMEEmail,
		ACMECacheDir: s.config.ACMECacheDir,
	}
}

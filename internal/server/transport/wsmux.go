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
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/config" // for mode
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/backpack/backpack/internal/web"
	"github.com/xtaci/smux"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// wsMuxGen is the state of a single run of the transport: the context that ends
// when the run does, and the channels its goroutines pass work over. Restart
// builds a fresh set for the next run, so carrying them here keeps a goroutine
// that outlives its run from reaching into the run that replaced it.
type wsMuxGen struct {
	ctx            context.Context
	tunnelChannel  chan *smux.Session
	localChannel   chan LocalTCPConn
	reqNewConnChan chan struct{}
	usageMonitor   *web.Usage
}

type WsMuxTransport struct {
	// The status shown in the panel. Behind a lock because the run being
	// replaced and the run replacing it both write it. See tunnelStatus.
	status     tunnelStatus
	config     *WsMuxConfig
	smuxConfig *smux.Config
	parentctx  context.Context
	// The current run. Replaced by Restart while the previous run's
	// goroutines are still reading it, so it lives behind a lock.
	run            runState
	logger         *logrus.Logger
	tunnelChannel  chan *smux.Session
	localChannel   chan LocalTCPConn
	reqNewConnChan chan struct{}
	controlChannel wsControl
	usageMonitor   *web.Usage
	restartMutex   sync.Mutex
	streamCounter  int32
	sessionCounter int32
	limits         *limiter
}

type WsMuxConfig struct {
	BindAddr         string
	Token            string
	SimpleAuth       bool
	SnifferLog       string
	TLSCertFile      string // Path to the TLS certificate file
	TLSKeyFile       string // Path to the TLS key file
	ACMEDomain       string // non-empty switches to Let's Encrypt for this domain
	ACMEEmail        string
	ACMECacheDir     string
	Ports            []string
	AcceptUDP        bool
	Nodelay          bool
	Sniffer          bool
	KeepAlive        time.Duration
	Heartbeat        time.Duration // in seconds
	ChannelSize      int
	MuxCon           int
	MuxVersion       int
	MaxFrameSize     int
	MaxReceiveBuffer int
	MaxStreamBuffer  int
	WebPort          int
	Mode             config.TransportType // ws or wss
	ProxyProtocol    bool
	// MSS caps the largest TCP segment the accepted tunnel connections send.
	// Zero leaves it to the kernel, which is the default; it is set where the
	// path silently drops full-sized packets. See manage.SetMSS.
	MSS int
	// MaxConnections caps simultaneous forwarded connections (0 = unlimited).
	MaxConnections int
	// BandwidthMbps caps total tunnel throughput (0 = unlimited).
	BandwidthMbps int
}

func NewWSMuxServer(parentCtx context.Context, config *WsMuxConfig, logger *logrus.Logger) *WsMuxTransport {
	// Create a derived context from the parent context
	ctx, cancel := context.WithCancel(parentCtx)

	// Initialize the TcpTransport struct
	server := &WsMuxTransport{
		smuxConfig: &smux.Config{
			Version:           network.ResolveStaticMuxVersion(config.MuxVersion),
			KeepAliveInterval: 20 * time.Second,
			KeepAliveTimeout:  40 * time.Second,
			MaxFrameSize:      config.MaxFrameSize,
			MaxReceiveBuffer:  config.MaxReceiveBuffer,
			MaxStreamBuffer:   config.MaxStreamBuffer,
		},
		config:         config,
		parentctx:      parentCtx,
		logger:         logger,
		tunnelChannel:  make(chan *smux.Session, config.ChannelSize),
		localChannel:   make(chan LocalTCPConn, config.ChannelSize),
		reqNewConnChan: make(chan struct{}, config.ChannelSize),
		streamCounter:  0,
		sessionCounter: 0,
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
func (s *WsMuxTransport) Start() {
	s.start(&wsMuxGen{
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
func (s *WsMuxTransport) start(g *wsMuxGen) {
	// for  webui
	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}

	s.status.set(fmt.Sprintf("Disconnected (%s)", s.config.Mode))

	go s.tunnelListener(g)

}

func (s *WsMuxTransport) Restart() {
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
	g := &wsMuxGen{
		ctx:            ctx,
		tunnelChannel:  make(chan *smux.Session, s.config.ChannelSize),
		localChannel:   make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan: make(chan struct{}, s.config.ChannelSize),
		usageMonitor:   web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx, s.config.SnifferLog, s.config.Sniffer, s.status.get, s.logger),
	}

	// Re-initialize variables
	s.controlChannel.Clear()
	s.status.set("")
	// Stored atomically, like every other access: the goroutines of the run
	// being replaced may still be counting while this resets them.
	atomic.StoreInt32(&s.streamCounter, 0)
	atomic.StoreInt32(&s.sessionCounter, 0)

	// set the log level again
	s.logger.SetLevel(level)

	go s.start(g)
}

func (s *WsMuxTransport) channelHandler(g *wsMuxGen) {
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
				_, msg, err := s.controlChannel.Get().ReadMessage()
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
				messageChan <- msg[0]
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

func (s *WsMuxTransport) tunnelListener(g *wsMuxGen) {
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
				session, err := smux.Client(conn.NetConn(), s.smuxConfig)
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

	// Built here rather than left to ListenAndServe, for the reason spelled out
	// in the ws transport: the socket that call opens carries none of the
	// tunnel's options, which is what made the MSS clamp a setting this
	// transport accepted and then ignored.
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

	if s.config.Mode == config.WSMUX {
		go func() {
			s.logger.Infof("%s server starting, listening on %s", s.config.Mode, addr)
			if !s.controlChannel.IsSet() {
				s.logger.Infof("waiting for %s control channel connection", s.config.Mode)
			}
			if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
				s.logger.Fatalf("failed to listen on %s: %v", addr, err)
			}
		}()
	} else {
		// Built up front so a certificate problem fails at startup rather than
		// per-handshake on a listener that is already accepting.
		tlsCfg, err := network.ServerTLSConfig(s.tlsSettings(), s.logger.Warnf)
		if err != nil {
			ln.Close()
			s.logger.Fatalf("failed to set up TLS on %s: %v", addr, err)
		}
		server.TLSConfig = tlsCfg

		go func() {
			s.logger.Infof("%s server starting, listening on %s", s.config.Mode, addr)
			if !s.controlChannel.IsSet() {
				s.logger.Infof("waiting for %s control channel connection", s.config.Mode)
			}
			// Empty paths: the certificate comes from TLSConfig.GetCertificate,
			// so renewal needs no restart.
			if err := server.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
				s.logger.Fatalf("failed to listen on %s: %v", addr, err)
			}
		}()
	}

	<-g.ctx.Done()

	// close connection
	if s.controlChannel.IsSet() {
		s.controlChannel.Close()
	}

	// Gracefully shutdown the server
	s.logger.Infof("shutting down the websocket server on %s", addr)
	if err := server.Shutdown(context.Background()); err != nil {
		s.logger.Errorf("Failed to gracefully shutdown the server: %v", err)
	}
}

func (s *WsMuxTransport) parsePortMappings(g *wsMuxGen) {
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

func (s *WsMuxTransport) localListener(g *wsMuxGen, localAddr string, remoteAddr string) {
	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		s.logger.Fatalf("failed to start listener on %s: %v", localAddr, err)
		return
	}

	//close local listener after context cancellation
	defer listener.Close()

	go s.acceptLocalConn(g, listener, remoteAddr)
	// The same forwarded port, carrying datagrams. A flow is handed over as a
	// net.Conn, so from here down it is paired with a tunnel connection, piped,
	// counted and torn down by exactly the code that does it for TCP.
	if s.config.AcceptUDP {
		go startUDPForward(g.ctx, s.logger, localAddr, remoteAddr,
			udpAdmitter(g.localChannel, g.reqNewConnChan, s.limits))
	}

	s.logger.Infof("listener started successfully, listening on address: %s", listener.Addr().String())

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
				s.logger.Debugf("accepted incoming TCP connection from %s", tcpConn.RemoteAddr().String())

				// +1 for stream counter
				atomic.AddInt32(&s.streamCounter, 1)

				if atomic.LoadInt32(&s.streamCounter) >= atomic.LoadInt32(&s.sessionCounter)*int32(s.config.MuxCon) {
					s.logger.Tracef("stream counter: %v, session counter: %v", atomic.LoadInt32(&s.streamCounter), atomic.LoadInt32(&s.sessionCounter))
					// Attempt to request a new connection
					select {
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

func (s *WsMuxTransport) handleLoop(g *wsMuxGen) {
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
				incomingConn.conn.Close()

				// Decrement the counter
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
				handlers.TCPConnectionHandler(g.ctx, s.config.ProxyProtocol && !isUDPFlow(incomingConn.conn), incomingConn.conn, metrics.CountedConn(stream), s.logger, g.usageMonitor, localForwardPort(incomingConn.conn), s.config.Sniffer)
				atomic.AddInt32(&s.streamCounter, -1)
				<-counter // read signal from the channel
			}()
		}
	}
}

func (s *WsMuxTransport) handleSessionError(g *wsMuxGen, incomingConn *LocalTCPConn, err error) {
	s.logger.Tracef("failed to handle session: %v", err)

	// decrease session value
	atomic.AddInt32(&s.sessionCounter, -1)

	// Put local connection back to local channel
	g.localChannel <- *incomingConn

	// Attempt to request a new connection
	select {
	case g.reqNewConnChan <- struct{}{}:
	default:
		s.logger.Warn("request new connection channel is full")
	}
}

// tlsSettings describes how this listener should obtain its certificate:
// Let's Encrypt when a domain is configured, otherwise the PEM pair on disk.
func (s *WsMuxTransport) tlsSettings() network.TLSSettings {
	return network.TLSSettings{
		CertFile:     s.config.TLSCertFile,
		KeyFile:      s.config.TLSKeyFile,
		ACMEDomain:   s.config.ACMEDomain,
		ACMEEmail:    s.config.ACMEEmail,
		ACMECacheDir: s.config.ACMECacheDir,
	}
}

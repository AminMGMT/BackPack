package transport

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/config" // for mode
	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/metrics"
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
	// seat holds the client this generation serves, and serving starts what
	// outlives any one client — once, whichever claim comes first. See
	// clientSeat.
	seat    clientSeat
	serving sync.Once
}

type WsMuxTransport struct {
	// The generations of this transport and what outlives them: see
	// lifecycle.go.
	lifecycle

	config         *WsMuxConfig
	smuxConfig     *smux.Config
	controlChannel wsControl
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
		config: config,
		lifecycle: lifecycle{
			parentctx: parentCtx,
			logger:    logger,
			usage:     usageSpec{webPort: config.WebPort, snifferLog: config.SnifferLog, sniffer: config.Sniffer},
		},
		streamCounter:  0,
		sessionCounter: 0,
		limits:         newLimiter(Limits{MaxConnections: config.MaxConnections, BandwidthMbps: config.BandwidthMbps}),
	}

	// The first run is installed the same way every later one is, so there is
	// only one path that ever writes it.
	server.firstGeneration()

	return server
}

// Start brings up the first run. Every later one comes from Restart, which
// builds its own generation and hands it straight to start — so the fields read
// here are written once, by the constructor, before any other goroutine exists.
func (s *WsMuxTransport) Start() {
	s.start(s.newGen(s.run.context()))
}

// start runs one generation of the transport. Everything it needs is in g:
// nothing in here reaches back for a field that the next Restart is entitled to
// replace while this run is still using it.
func (s *WsMuxTransport) start(g *wsMuxGen) {
	go sweepTunnelConns(g.ctx, g.tunnelChannel)
	// for  webui
	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}

	s.status.set(fmt.Sprintf("Disconnected (%s)", s.config.Mode))

	go s.tunnelListener(g)

}

func (s *WsMuxTransport) Restart() {
	s.restart(s.controlChannel.Close, func(ctx context.Context) {
		s.controlChannel.Clear()
		s.counterMutex.Lock()
		atomic.StoreInt32(&s.streamCounter, 0)
		atomic.StoreInt32(&s.sessionCounter, 0)
		s.counterMutex.Unlock()
		go s.start(s.newGen(ctx))
	})
}

// newGen builds one generation's channels and usage monitor.
func (s *WsMuxTransport) newGen(ctx context.Context) *wsMuxGen {
	return &wsMuxGen{
		ctx:            ctx,
		tunnelChannel:  make(chan *smux.Session, s.config.ChannelSize),
		localChannel:   make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan: make(chan struct{}, s.config.ChannelSize),
		usageMonitor:   s.usageMonitor(ctx),
	}
}

// control is this generation's control loop, bound to the control channel
// the generation was started with. See controlLoop.
func (s *WsMuxTransport) control(g *wsMuxGen, ctx context.Context, lost func()) controlLoop {
	return controlLoop{
		ctx:      ctx,
		link:     controlwire.WS(s.controlChannel.Get()),
		beat:     s.config.Heartbeat,
		requests: g.reqNewConnChan,
		log:      s.logger,
		restart:  lost,
	}
}

// seatClient makes conn this generation's client, in place of whoever was.
func (s *WsMuxTransport) seatClient(g *wsMuxGen, conn *websocket.Conn) {
	g.seat.sit(g.ctx,
		func() { s.vacate(g) },
		func() {
			s.controlChannel.Set(conn)
			// See metrics.Snapshot.Connected: the watchdog asks the engine, not
			// the socket table.
			s.seated(conn.RemoteAddr().String())
			s.status.set(fmt.Sprintf("Connected (%s)", s.config.Mode))
			s.logger.Info("control channel established successfully")
		},
		func(ctx context.Context, lost func()) {
			loop := s.control(g, ctx, lost)
			// Bind to this claim even if its goroutine starts after replacement.
			loop.link = controlwire.WS(conn)
			loop.run()
		})
}

// vacate empties the seat: the client's channel is closed and forgotten, and
// the mux sessions it opened that wait in the pool are dropped. The listener
// and the forwarded ports stay up for the next client.
func (s *WsMuxTransport) vacate(g *wsMuxGen) {
	s.controlChannel.Close()
	s.controlChannel.Clear()
	drainTunnelConns(g.tunnelChannel)
	s.status.set(fmt.Sprintf("Disconnected (%s)", s.config.Mode))
	metrics.ClearPeer()
}

func (s *WsMuxTransport) tunnelListener(g *wsMuxGen) {
	// Counted while this goroutine holds a listener, so Start can wait for the
	// port rather than sleeping and hoping. See listeners.go.
	s.listeners.hold()
	defer s.listeners.release()

	addr := s.config.BindAddr
	upgrader := websocket.Upgrader{
		ReadBufferSize:   wsReadBufferSize,
		WriteBufferSize:  wsWriteBufferSize,
		WriteBufferPool:  wsWriteBuffers,
		HandshakeTimeout: 45 * time.Second,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	// The decoy's identity is derived once here rather than per request: it is
	// fixed for the life of this server, and hashing the token on every probe
	// would be work an attacker could ask for.
	decoy := newDecoyProfile(s.config.Token)

	// Create an HTTP server
	server := &http.Server{
		Addr:              addr,
		IdleTimeout:       -1,
		ReadHeaderTimeout: tunnelHeaderTimeout,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.logger.Tracef("received http request from %s", r.RemoteAddr)

			// Only a genuine tunnel connection (websocket upgrade, tunnel path,
			// valid credential) is served as a tunnel. Everything else — a
			// browser, a scanner, a probe with the wrong token — gets the decoy
			// website, so on 443 this looks like an ordinary HTTPS site rather
			// than a tunnel that answers with 401.
			if !isTunnelRequest(r, s.config.Token, s.config.SimpleAuth) {
				decoy.serve(w, r)
				return
			}

			conn, err := upgrader.Upgrade(w, r, serverProof(r, s.config.Token, s.config.SimpleAuth))
			if err != nil {
				s.logger.Errorf("failed to upgrade connection from %s: %v", r.RemoteAddr, err)
				return
			}

			if r.URL.Path == "/channel" {
				if endedClaim(g.ctx, conn) {
					return
				}
				// A claim while a client is seated is that client re-dialing: it
				// takes the seat in place, and the ports and their users stay
				// up. It used to close both and rebuild the whole run. See
				// clientSeat.
				if g.seat.serving() {
					s.logger.Warn("a new control channel claim arrived; adopting the new client in place")
				}
				s.seatClient(g, conn)
				g.serving.Do(func() { s.serveGeneration(s.forwarder(g), func() { s.handleLoop(g) }) })

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
					s.logger.Warnf("forwarded port: the queue is full, dropping a client from %s", conn.RemoteAddr().String())
					conn.Close()
				}
			}
		}),
	}

	// Built here rather than left to ListenAndServe, for the reason spelled out
	// in the ws transport: the socket that call opens carries none of the
	// tunnel's options, which is what made the MSS clamp a setting this
	// transport accepted and then ignored.
	// The tunnel's own port: retried rather than fatal. See bindfail.go.
	ln, ok := bindTunnelPort(g.ctx, s.logger, addr, func() (net.Listener, error) {
		// The websocket transports have never pinned the socket buffers.
		return network.ListenWithBuffers("tcp", addr, 0, 0, s.config.MSS, s.config.KeepAlive, !s.config.Nodelay)
	})
	if !ok {
		return
	}

	if s.config.Mode == config.WSMUX {
		go func() {
			s.logger.Infof("%s server starting, listening on %s", s.config.Mode, addr)
			if !s.controlChannel.IsSet() {
				s.logger.Infof("waiting for %s control channel connection", s.config.Mode)
			}
			// The bind already succeeded, so this is the HTTP server itself
			// stopping. Ending the run is right; ending the process is not.
			if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
				s.logger.Errorf("%s server on %s stopped: %v", s.config.Mode, addr, err)
			}
		}()
	} else {
		// Built up front so a certificate problem fails at startup rather than
		// per-handshake on a listener that is already accepting.
		tlsCfg, err := network.ServerTLSConfig(s.tlsSettings(), s.logger.Warnf)
		if err != nil {
			ln.Close()
			// Reported and stopped, not fatal. See the same passage in ws.go.
			s.logger.Errorf("%s on %s cannot start: its TLS certificate could not be "+
				"set up: %v", s.config.Mode, addr, err)
			return
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
				s.logger.Errorf("%s server on %s stopped: %v", s.config.Mode, addr, err)
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

func (s *WsMuxTransport) handleLoop(g *wsMuxGen) {
	for {
		select {
		case <-g.ctx.Done():
			return

		case session := <-g.tunnelChannel:
			// +1 for session counter
			if !s.countGeneration(g.ctx, &s.sessionCounter, 1) {
				session.Close()
				continue
			}

			loop := s.session(g)
			go loop.run(session)
		}
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
		// Only used when no certificate was configured at all, and cosmetic
		// even then — but a generated certificate that names the address it is
		// served from reads as a certificate rather than as a mistake.
		SelfSignedHost: certHost(s.config.BindAddr),
	}
}

// session binds this transport's channels, counters and settings to the shared
// loop. It is the whole of what is transport-specific about running a session.
// Its context is the seated client's, so the session ends with the client that
// opened it rather than with the generation — which now outlives its clients.
func (s *WsMuxTransport) session(g *wsMuxGen) muxSession {
	return muxSession{
		ctx:           g.seat.client(),
		local:         g.localChannel,
		usage:         g.usageMonitor,
		reqNewConn:    g.reqNewConnChan,
		muxCon:        s.config.MuxCon,
		proxyProtocol: s.config.ProxyProtocol,
		sniffer:       s.config.Sniffer,
		limits:        s.limits,
		log:           s.logger,
		streams:       &s.streamCounter,
		sessions:      &s.sessionCounter,
		count: func(counter *int32, delta int32) {
			s.countGeneration(g.ctx, counter, delta)
		},
	}
}

// forwarder is this transport's forwarded ports for one generation.
func (s *WsMuxTransport) forwarder(g *wsMuxGen) portForwarder {
	return portForwarder{
		ctx: g.ctx, ports: s.config.Ports, acceptUDP: s.config.AcceptUDP,
		queue: g.localChannel, limits: s.limits, listeners: &s.listeners, log: s.logger,
		tune:   s.tuneUserConn,
		queued: muxRequestForGeneration(g.ctx, &s.counterMutex, &s.streamCounter, &s.sessionCounter, s.config.MuxCon, g.reqNewConnChan, s.logger),
	}
}

// tuneUserConn is the same as the ws transport's; see wsTune.
func (s *WsMuxTransport) tuneUserConn(c *net.TCPConn) {
	wsTune(c, s.config.Nodelay, s.config.KeepAlive, s.logger)
}

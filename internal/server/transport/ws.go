package transport

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/backpack/backpack/internal/web"

	"github.com/backpack/backpack/internal/metrics"
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
	// seat holds the client this generation serves, and serving starts what
	// outlives any one client — once, whichever claim comes first. See
	// clientSeat.
	seat    clientSeat
	serving sync.Once
}

type WsTransport struct {
	// The generations of this transport and what outlives them: see
	// lifecycle.go.
	lifecycle

	config *WsConfig
	// The run's channels and its usage monitor are deliberately not fields:
	// they belong to one generation, and a field outlives the generation that
	// made it. See Start.
	controlChannel wsControl
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
	// Initialize the TcpTransport struct
	server := &WsTransport{
		config: config,
		lifecycle: lifecycle{
			parentctx: parentCtx,
			logger:    logger,
			usage:     usageSpec{webPort: config.WebPort, snifferLog: config.SnifferLog, sniffer: config.Sniffer},
		},
		limits: newLimiter(Limits{MaxConnections: config.MaxConnections, BandwidthMbps: config.BandwidthMbps}),
	}

	// The first run is installed the same way every later one is, so there is
	// only one path that ever writes it.
	server.firstGeneration()

	return server
}

// Start brings up the first run, building its generation exactly the way
// Restart builds every later one.
//
// It used to take the first generation's channels from fields on the transport,
// and Restart never replaced those fields — it only built fresh channels for
// the new generation. So the first run's channels stayed reachable from the
// struct for the life of the process, and with them every connection still
// queued in them, none of which was ever closed. The same shape was measured on
// the plain TCP transport: with a pool of 64, 64 sockets were still open after
// the client had gone and the run had been torn down, and a forced GC did not
// release them — an unreachable connection is closed by its finalizer, but
// these were still reachable. Building the generation here leaves nothing
// behind to pin.
func (s *WsTransport) Start() {
	s.start(s.newGen(s.run.context()))
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
	s.restart(s.controlChannel.Close, func(ctx context.Context) {
		s.controlChannel.Clear()
		go s.start(s.newGen(ctx))
	})
}

// newGen builds one generation's channels and usage monitor.
func (s *WsTransport) newGen(ctx context.Context) *wsGen {
	return &wsGen{
		ctx:            ctx,
		tunnelChannel:  make(chan TunnelChannel, s.config.ChannelSize),
		localChannel:   make(chan LocalTCPConn, s.config.ChannelSize),
		reqNewConnChan: make(chan struct{}, s.config.ChannelSize),
		usageMonitor:   s.usageMonitor(ctx),
	}
}

// control is this generation's control loop, bound to the control channel
// the generation was started with. See controlLoop.
func (s *WsTransport) control(g *wsGen, ctx context.Context, lost func()) controlLoop {
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
func (s *WsTransport) seatClient(g *wsGen, conn *websocket.Conn) {
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
// the tunnel connections it opened that wait in the pool are dropped. The
// listener and the forwarded ports stay up for the next client.
func (s *WsTransport) vacate(g *wsGen) {
	s.controlChannel.Close()
	s.controlChannel.Clear()
	for drained := false; !drained; {
		select {
		case c := <-g.tunnelChannel:
			// Its keepalive stops with it rather than at its next beat.
			close(c.ping)
			c.conn.Close()
		default:
			drained = true
		}
	}
	s.status.set(fmt.Sprintf("Disconnected (%s)", s.config.Mode))
	metrics.ClearPeer()
}

func (s *WsTransport) tunnelListener(g *wsGen) {
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
				// A claim while a client is seated is that client re-dialing,
				// often before this side noticed its old channel was dead: it
				// takes the seat in place, and the ports and their users stay
				// up. It used to close both and rebuild the whole run. See
				// clientSeat.
				if g.seat.serving() {
					s.logger.Warn("a new control channel claim arrived; adopting the new client in place")
				}
				s.seatClient(g, conn)
				g.serving.Do(func() { s.serveGeneration(s.forwarder(g), func() { s.handleLoop(g) }) })

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
	// The tunnel's own port: retried rather than fatal. See bindfail.go.
	ln, ok := bindTunnelPort(g.ctx, s.logger, addr, func() (net.Listener, error) {
		// The websocket transports have never pinned the socket buffers.
		return network.ListenWithBuffers("tcp", addr, 0, 0, s.config.MSS, s.config.KeepAlive, !s.config.Nodelay)
	})
	if !ok {
		return
	}

	if s.config.Mode == config.WS {
		go func() {
			s.logger.Infof("ws server starting, listening on %s", addr)
			if !s.controlChannel.IsSet() {
				s.logger.Info("waiting for ws control channel connection")
			}
			// The bind already succeeded, so this is the HTTP server itself
			// stopping. Ending the run is right; ending the process is not —
			// the supervisor would restart it into the same condition.
			if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
				s.logger.Errorf("ws server on %s stopped: %v", addr, err)
			}
		}()
	} else {
		// Built before the goroutine starts so a bad certificate or an
		// unwritable ACME cache is reported here, at startup, instead of
		// surfacing later as handshake failures on a listener that is up.
		tlsCfg, err := network.ServerTLSConfig(s.tlsSettings(), s.logger.Warnf)
		if err != nil {
			// A certificate this tunnel cannot use is not something waiting
			// fixes, so this does not retry — but it is not a reason to end the
			// process either. Under a unit that restarts every three seconds
			// that turned one bad certificate into a crash loop, where what the
			// operator needed was the sentence explaining it, once, on a
			// process still running to be asked.
			ln.Close()
			s.logger.Errorf("wss on %s cannot start: its TLS certificate could not be "+
				"set up: %v", addr, err)
			return
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
				s.logger.Errorf("wss server on %s stopped: %v", addr, err)
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

func (s *WsTransport) handleLoop(g *wsGen) {
	for {
		select {
		case <-g.ctx.Done():
			return
		case localConn := <-g.localChannel:
			if expired(localConn) {
				drop(localConn, s.limits, s.logger)
				continue
			}
			pairing[TunnelChannel]{
				ctx: g.ctx, local: localConn, tunnel: g.tunnelChannel,
				limits: s.limits, log: s.logger,
				announce: func(c TunnelChannel, addr string) error {
					// The keepalive goroutine stops here: from this point the
					// connection is carrying traffic, and a ping written into
					// the middle of it would be framed as tunnel data.
					close(c.ping)
					c.mu.Lock()
					defer c.mu.Unlock()
					if err := c.conn.SetWriteDeadline(time.Now().Add(pairingWait(localConn.timeCreated))); err != nil {
						return err
					}
					defer c.conn.SetWriteDeadline(time.Time{})
					return c.conn.WriteMessage(websocket.TextMessage, []byte(addr))
				},
				discard: func(c TunnelChannel) { c.conn.Close() },
				relay: func(c TunnelChannel, local LocalTCPConn) {
					go func() {
						// Free the connection slot once the transfer ends, or
						// the limit would fill up permanently.
						defer s.limits.release()
						handlers.WSConnectionHandler(g.ctx, c.conn, local.conn,
							s.logger, g.usageMonitor, localForwardPort(local.conn), s.config.Sniffer)
					}()
				},
			}.run()
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

			// A stalled idle ping must not hold the pairing writer lock
			// indefinitely; use the same bound as the control channel.
			if err := controlwire.WS(conn.conn).Send(utils.SG_Ping); err != nil {
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
		// Only used when no certificate was configured at all, and cosmetic
		// even then — but a generated certificate that names the address it is
		// served from reads as a certificate rather than as a mistake.
		SelfSignedHost: certHost(s.config.BindAddr),
	}
}

// forwarder is this transport's forwarded ports for one generation.
func (s *WsTransport) forwarder(g *wsGen) portForwarder {
	return portForwarder{
		ctx: g.ctx, ports: s.config.Ports, acceptUDP: s.config.AcceptUDP,
		queue: g.localChannel, limits: s.limits, listeners: &s.listeners, log: s.logger,
		tune:   s.tuneUserConn,
		queued: requestAlways(g.reqNewConnChan, s.logger),
	}
}

// tuneUserConn is the websocket transports' socket options for a user's
// connection: nodelay as configured, and TCP keepalive with the tunnel's period.
func (s *WsTransport) tuneUserConn(c *net.TCPConn) {
	wsTune(c, s.config.Nodelay, s.config.KeepAlive, s.logger)
}

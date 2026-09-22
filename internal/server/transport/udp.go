package transport

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/web"
	"github.com/sirupsen/logrus"
)

// udpPayloadQueue is how many datagrams may wait for the goroutine that will
// forward them — per forwarded flow, and per pooled tunnel connection.
//
// It was 100_000. A Go channel allocates its whole buffer the moment it is
// made, so at 24 bytes for a slice header that reserved 2.3 MB for every
// connection the moment it was seen, whether or not it went on to carry a
// single byte. A pool of 64 idle connections was 145 MB of a 153 MB heap on a
// tunnel that was forwarding nothing at all.
//
// 256 is the depth the forwarded-UDP path already settled on for the same job
// (see udpFlowQueue): deep enough to absorb the pause while a flow waits to be
// paired with a tunnel connection — dropping there costs the opening packet of
// a session, which reads as "UDP does not work" rather than as one lost packet
// — and bounded, so a peer that floods a stalled flow cannot grow the process
// without limit.
const udpPayloadQueue = 256

// idleForward is how long a forwarded UDP flow may go without a packet before
// its two copy goroutines give up on it.
//
// A named constant because it was written out twice, as a local in each copy
// loop, and the words around it had drifted from the value: the case comment
// said thirty seconds, the log line said sixty, and the variable said sixty.
// UDP has no close, so this is the only thing that ends a flow.
const idleForward = 60 * time.Second

// udpGen is the state of a single run of the transport: the context that ends
// when the run does, and the channels its goroutines pass work over. Restart
// builds a fresh set for the next run, so carrying them here keeps a goroutine
// that outlives its run from reaching into the run that replaced it.
type udpGen struct {
	ctx            context.Context
	tunnelChannel  chan *TunnelUDPConn
	reqNewConnChan chan struct{}
	usageMonitor   *web.Usage
}

type UdpTransport struct {
	// The listeners this transport is holding right now. Start waits on it, so
	// "Start returned" means "the ports are free". See listeners.go.
	listeners listenerSet

	// The status shown in the panel. Behind a lock because the run being
	// replaced and the run replacing it both write it. See tunnelStatus.
	status    tunnelStatus
	config    *UdpConfig
	parentctx context.Context
	// The current run. Replaced by Restart while the previous run's
	// goroutines are still reading it, so it lives behind a lock.
	run    runState
	logger *logrus.Logger
	// The run's channels and its usage monitor are deliberately not fields: they
	// belong to one generation, and a field outlives the generation that made
	// it. See Start.
	activeConnections map[string]*TunnelUDPConn
	activeMu          sync.Mutex
	controlChannel    netControl
	restartMutex      sync.Mutex
	limits            *limiter
	rtt               int64 // for Fun!
}

type UdpConfig struct {
	BindAddr    string
	Token       string
	SnifferLog  string
	Ports       []string
	Sniffer     bool
	Heartbeat   time.Duration // in seconds, for udp conn and control channel
	ChannelSize int
	WebPort     int
	// SO_RCVBUF/SO_SNDBUF size the datagram sockets. The kernel default is a few
	// hundred KB, which a datagram flood — a speed test, a busy game server —
	// overruns in a blink, and the packets it cannot hold are dropped before any
	// goroutine reads them. Sizing the socket to the preset's several MB is what
	// keeps the tunnel carrying traffic under load instead of stalling.
	SO_RCVBUF int
	SO_SNDBUF int
	// MaxConnections caps how many source addresses may be forwarded at once,
	// and BandwidthMbps caps throughput across the whole tunnel. Zero means
	// unlimited, the same as everywhere else.
	//
	// Both were accepted by the menu, saved into the TOML and shown in the
	// panel, and this struct had nowhere to put them — so a udp tunnel was the
	// one transport where a limit an operator set was silently not a limit.
	// "Connection" is a source address here rather than a socket, because that
	// is the only thing a connectionless protocol has that means the same
	// thing: one peer's flow through the tunnel.
	MaxConnections int
	BandwidthMbps  int
}

func NewUDPServer(parentCtx context.Context, config *UdpConfig, logger *logrus.Logger) *UdpTransport {
	// Create a derived context from the parent context
	ctx, cancel := context.WithCancel(parentCtx)

	// Initialize the TcpTransport struct
	server := &UdpTransport{
		config:            config,
		parentctx:         parentCtx,
		logger:            logger,
		activeConnections: map[string]*TunnelUDPConn{},
		activeMu:          sync.Mutex{},
		limits:            newLimiter(Limits{MaxConnections: config.MaxConnections, BandwidthMbps: config.BandwidthMbps}),
		rtt:               0,
	}

	// The first run is installed the same way every later one is, so there is
	// only one path that ever writes it.
	server.run.set(ctx, cancel)

	return server
}

// Start brings up the first run, building its generation exactly the way
// Restart builds every later one.
//
// It used to take the first generation's channels from fields on the transport,
// and Restart never replaced those fields — it only built fresh channels for
// the new generation. So the first run's tunnel channel stayed reachable from
// the struct for the life of the process, and with it every TunnelUDPConn left
// queued in it, each holding a payload channel of its own. Measured with a pool
// of 64: 145 MB still held after the client had gone and the run had been torn
// down, released only by restarting the server. Building the generation here
// leaves nothing behind to pin it.
func (s *UdpTransport) Start() {
	ctx := s.run.context()
	s.start(&udpGen{
		ctx:            ctx,
		tunnelChannel:  make(chan *TunnelUDPConn, s.config.ChannelSize),
		reqNewConnChan: make(chan struct{}, s.config.ChannelSize),
		usageMonitor: web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx,
			s.config.SnifferLog, s.config.Sniffer, s.status.get, s.logger),
	})
}

// start runs one generation of the transport. Everything it needs is in g:
// nothing in here reaches back for a field that the next Restart is entitled to
// replace while this run is still using it.
func (s *UdpTransport) start(g *udpGen) {
	s.status.set("Disconnected (UDP)")

	if s.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}

	go s.channelHandshake(g)
}

func (s *UdpTransport) Restart() {
	if !s.restartMutex.TryLock() {
		s.logger.Warn("server restart already in progress, skipping restart attempt")
		return
	}
	defer s.restartMutex.Unlock()

	s.logger.Info("restarting server...")

	// for removing timeout logs
	level := s.logger.GetLevel()
	s.logger.SetLevel(logrus.FatalLevel)

	s.run.stop()

	// Close open connection
	if s.controlChannel.IsSet() {
		s.controlChannel.Close()
	}

	// Wait for the listeners rather than guessing at how long they take.
	//
	// This was a flat two-second sleep, and the comment next to it said what it
	// was for: the run being replaced still holds the ports, and binding them
	// again before it lets go fails. A sleep is a guess — usually long enough,
	// never a guarantee, and silently wrong on a loaded machine, which is
	// exactly when a restart is most likely to be happening.
	//
	// listenerSet answers the question instead of approximating it. It is also
	// faster in the ordinary case: a listener closes in microseconds, so this
	// returns at once rather than always costing two seconds.
	s.listeners.wait(s.parentctx)

	// The whole tunnel may have been shut down while this restart was waiting —
	// on a reload, or on the process going down. Rebuilding the run from a
	// parent context that is already finished would bind the listeners again
	// only to close them, and on a reload that means fighting the run that is
	// replacing this one for its own ports. Nothing here is worth starting.
	if s.parentctx.Err() != nil {
		// The level was turned down to hide the timeouts a teardown produces;
		// leaving it there would silence the shutdown itself.
		s.logger.SetLevel(level)
		// Abandoning is not a reason to keep claiming a peer.
		//
		// This branch used to return before the two lines below, which sit on
		// the path that carries on — so a restart that gave up left the status
		// reading "Connected" and left the peer published in the metrics
		// snapshot. The process usually exits straight afterwards and the
		// snapshot goes stale, which is why this was invisible; with a
		// transport fallback chain it is not, because the chain cancels a
		// candidate's context and the *process keeps running*. The snapshot
		// then carries a fresh timestamp and a connected peer for a tunnel that
		// is mid-rotation with nothing connected at all, and the watchdog
		// reads that and calls it healthy.
		//
		// The run is over. Whatever ended it, there is no peer.
		s.status.set("")
		metrics.ClearPeer()
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
	g := &udpGen{
		ctx:            ctx,
		tunnelChannel:  make(chan *TunnelUDPConn, s.config.ChannelSize),
		reqNewConnChan: make(chan struct{}, s.config.ChannelSize),
		usageMonitor:   web.NewDataStore(fmt.Sprintf(":%v", s.config.WebPort), ctx, s.config.SnifferLog, s.config.Sniffer, s.status.get, s.logger),
	}

	// Re-initialize variables
	s.status.set("")
	s.controlChannel.Clear()
	metrics.ClearPeer()
	s.activeConnections = map[string]*TunnelUDPConn{}
	s.activeMu = sync.Mutex{}

	// set the log level again
	s.logger.SetLevel(level)

	go s.start(g)
}

func (s *UdpTransport) channelHandshake(g *udpGen) {
	// This transport's control channel is TCP even though its data is not, so
	// this goroutine holds a listener too and Start has to wait for it. Counted
	// here rather than in tunnelListener, which owns the UDP half.
	s.listeners.hold()
	defer s.listeners.release()

	// The tunnel's own control port: retried rather than fatal. See bindfail.go.
	// Named apart from the acceptBackoff further down — that one paces a
	// spinning accept loop in milliseconds, this one waits seconds for another
	// process to let go of a port.
	var bindBackoff listenBackoff
	var listener net.Listener
	for {
		var err error
		listener, err = net.Listen("tcp", s.config.BindAddr)
		if err == nil {
			break
		}
		s.logger.Error(bindFailure("tunnel port", s.config.BindAddr, err))
		if !bindBackoff.wait(g.ctx) {
			return
		}
	}

	s.logger.Infof("server started successfully, listening on address: %s", listener.Addr().String())

	defer listener.Close()

	// Close the listener when the run ends so the blocked Accept below returns
	// instead of holding this goroutine open past the restart that replaced it.
	go func() {
		<-g.ctx.Done()
		listener.Close()
	}()

	// Unlike the pool listeners, this one keeps accepting for the whole run. The
	// first valid claim becomes the control channel; a later one means the
	// client restarted on its own and re-dialed, while this run never noticed
	// because the old TCP connection has not yet failed a read or write. The
	// old single-accept design left that tunnel dead until the server was
	// restarted by hand — the exact symptom this fixes.
	established := false

	var backoff acceptBackoff
	for {
		conn, err := listener.Accept()
		if err != nil {
			if g.ctx.Err() != nil {
				return
			}
			s.logger.Debugf("failed to accept control channel connection on %s: %v", listener.Addr(), err)
			// The context check above catches a shutdown, but a listener broken
			// for any other reason fails instantly and forever; without a pause
			// this loop would spin on a core. See acceptBackoff.
			if !backoff.Fail(g.ctx) {
				return
			}
			continue
		}
		backoff.OK()

		if !s.validControlClaim(conn) {
			conn.Close()
			continue
		}

		if established {
			// A second valid claim means the client restarted on its own and
			// re-dialed, while this run never noticed because the old connection
			// has not failed a read or write yet. The claim is answered (above),
			// so the client knows it reached the right server; rebuilding the run
			// then re-binds this listener, which the re-dialing client reaches on
			// its next retry.
			s.logger.Warn("a new control channel claim arrived; restarting to adopt the new client")
			conn.Close()
			go s.Restart()
			return
		}

		// A dead client that never sends FIN/RST — a hard kill, or a path that
		// blackholes under load — would otherwise sit here as a zombie. Keepalive
		// probes turn that into a read error the channel handler can act on.
		enableKeepAlive(conn, 30*time.Second)

		s.controlChannel.Set(conn)
		// The engine says whether it holds a control channel; the watchdog reads
		// it rather than the socket table, which shows a socket long after the
		// tunnel behind it has stopped working. See metrics.Snapshot.Connected.
		metrics.ReportPeer(conn.RemoteAddr().String())
		s.logger.Info("control channel successfully established.")
		established = true

		go s.tunnelListener(g)
		go s.parsePortMappings(g)
		go s.channelHandler(g)
	}
}

// validControlClaim reads the token handshake a control-channel claimant must
// pass and answers it. It leaves the connection open on success; the caller
// closes it when this returns false.
func (s *UdpTransport) validControlClaim(conn net.Conn) bool {
	// Set a read deadline for the token response
	if err := conn.SetReadDeadline(time.Now().Add(controlClaimTimeout)); err != nil {
		s.logger.Errorf("failed to set read deadline: %v", err)
		return false
	}

	msg, transport, err := utils.ReceiveBinaryTransportString(conn)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			s.logger.Warn("timeout while waiting for control channel signal")
		} else {
			s.logger.Errorf("failed to receive control channel signal: %v", err)
		}
		return false
	}

	if transport != utils.SG_Chan {
		s.logger.Errorf("invalid signal received for channel, discarding connection")
		return false
	}

	// Resetting the deadline (removes any existing deadline)
	conn.SetReadDeadline(time.Time{})

	if !tokenMatches(msg, s.config.Token) {
		s.logger.Warnf("invalid security token received")
		return false
	}

	if err := utils.SendBinaryTransportString(conn, s.config.Token, utils.SG_Chan); err != nil {
		s.logger.Errorf("failed to send security token: %v", err)
		return false
	}

	return true
}

func (s *UdpTransport) channelHandler(g *udpGen) {
	ticker := time.NewTicker(s.config.Heartbeat)
	defer ticker.Stop()

	// Channel to receive the message or error
	messageChan := make(chan byte, 1)

	go func() {
		for {
			select {
			case <-g.ctx.Done():
				return
			default:
				message, err := utils.ReceiveBinaryByte(s.controlChannel.Get())
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
				messageChan <- message
			}
		}
	}()

	// RTT measurment
	rtt := time.Now()
	err := utils.SendBinaryByteWithin(s.controlChannel.Get(), utils.SG_RTT, controlWriteTimeout)
	if err != nil {
		s.logger.Error("failed to send RTT signal, attempting to restart server...")
		go s.Restart()
		return
	}

	for {
		select {
		case <-g.ctx.Done():
			_ = utils.SendBinaryByteWithin(s.controlChannel.Get(), utils.SG_Closed, controlWriteTimeout)
			return

		case <-g.reqNewConnChan:
			err := utils.SendBinaryByteWithin(s.controlChannel.Get(), utils.SG_Chan, controlWriteTimeout)
			if err != nil {
				s.logger.Error("failed to send request new connection signal. ", err)
				go s.Restart()
				return
			}

		case <-ticker.C:
			err := utils.SendBinaryByteWithin(s.controlChannel.Get(), utils.SG_HB, controlWriteTimeout)
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

			if message == utils.SG_Closed {
				s.logger.Warn("control channel has been closed by the client")
				go s.Restart()
				return

			} else if message == utils.SG_RTT {
				measureRTT := time.Since(rtt)
				s.rtt = measureRTT.Milliseconds()
				s.logger.Infof("Round Trip Time (RTT): %d ms", s.rtt)
			}
		}
	}
}

// applyBuffers sizes a datagram socket to the configured SO_RCVBUF/SO_SNDBUF. A
// zero value leaves the kernel default in place. Best effort: a socket that
// refuses the size — usually because net.core.rmem_max is lower — still works,
// it just has less headroom against a burst.
func (s *UdpTransport) applyBuffers(conn *net.UDPConn) {
	if s.config.SO_RCVBUF > 0 {
		if err := conn.SetReadBuffer(s.config.SO_RCVBUF); err != nil {
			s.logger.Warnf("failed to set UDP read buffer to %d: %v", s.config.SO_RCVBUF, err)
		}
	}
	if s.config.SO_SNDBUF > 0 {
		if err := conn.SetWriteBuffer(s.config.SO_SNDBUF); err != nil {
			s.logger.Warnf("failed to set UDP write buffer to %d: %v", s.config.SO_SNDBUF, err)
		}
	}
}

func (s *UdpTransport) tunnelListener(g *udpGen) {
	// Counted while this goroutine holds a listener, so Start can wait for the
	// port rather than sleeping and hoping. See listeners.go.
	s.listeners.hold()
	defer s.listeners.release()

	// An address that does not parse is a configuration error: retrying it
	// would loop forever on something only an edit can fix.
	tunnelUDPAddr, err := net.ResolveUDPAddr("udp", s.config.BindAddr)
	if err != nil {
		s.logger.Errorf("tunnel port %s is not an address this machine can listen on: %v",
			s.config.BindAddr, err)
		return
	}

	// The port itself: retried rather than fatal. See bindfail.go.
	var backoff listenBackoff
	var listener *net.UDPConn
	for {
		listener, err = net.ListenUDP("udp", tunnelUDPAddr)
		if err == nil {
			break
		}
		s.logger.Error(bindFailure("tunnel port", s.config.BindAddr, err))
		if !backoff.wait(g.ctx) {
			return
		}
	}

	// This one socket receives every client's pooled traffic, so it is the first
	// place a flood is felt; give it the configured headroom.
	s.applyBuffers(listener)

	defer listener.Close()

	s.logger.Infof("UDP tunnel listener started successfully, listening on address: %s", listener.LocalAddr().String())

	go s.acceptTunnelConn(g, listener)

	<-g.ctx.Done()
}

func (s *UdpTransport) acceptTunnelConn(g *udpGen, listener *net.UDPConn) {
	// Buffer for UDP reads
	buf := make([]byte, 16*1024)

	for {
		select {
		case <-g.ctx.Done():
			return
		default:
			n, addr, err := listener.ReadFromUDP(buf)
			if err != nil {
				s.logger.Errorf("failed to read from tunnel UDP listener: %v", err)
				continue
			}

			// Create a unique identifier for the connection based on IP and port
			key := addr.String()

			s.activeMu.Lock()
			// Check if the connection is already active
			if existingConn, exists := s.activeConnections[key]; exists {
				// These bytes crossed the tunnel, so they are counted here —
				// before the queue, which may not have room for them. A packet
				// dropped below was still carried and still paid for, and a
				// counter that hid it would show a tunnel losing traffic under
				// load as a tunnel doing nothing.
				//
				// This transport reported 0 in and 0 out however much it
				// carried: neither CountedConn nor AddBytes appeared in either
				// of its files, because it never hands out a net.Conn for the
				// wrapper to go around. The panel, the CLI, the Telegram report
				// and the traffic history all read it as idle.
				metrics.AddBytes(uint64(n), 0)
				s.limits.waitBytes(g.ctx, n)

				// Send the payload to the existing connection's payload channel
				select {
				case existingConn.payload <- append([]byte(nil), buf[:n]...): // Copy the packet to avoid data overwriting
					s.logger.Tracef("buffered %d bytes for existing connection %s", n, addr.String())

				default:
					s.logger.Warnf("payload channel for connection %s is full, dropping UDP packet", addr.String())
				}
				s.activeMu.Unlock()
				continue
			}

			s.activeMu.Unlock()

			if !tokenMatches(string(buf[:n]), s.config.Token) { // For new connections, validate the token
				s.logger.Errorf("invalid token received from %s", addr.String())
				continue
			}

			// Initialize the payload channel for the new connection
			payloadChan := make(chan []byte, udpPayloadQueue)

			// Create a new TunnelUDPConn
			tunnelConn := TunnelUDPConn{
				timeCreated: time.Now().UnixNano(), // Just for debugging
				payload:     payloadChan,
				addr:        addr,
				listener:    listener,
				ping:        make(chan struct{}, 1), // Initialize the ping channel
				mu:          &sync.Mutex{},
			}

			s.activeMu.Lock()
			// Add the new connection to the active connections map
			s.activeConnections[key] = &tunnelConn
			s.activeMu.Unlock()

			// Send the new tunnel connection to the tunnel channel
			select {
			case g.tunnelChannel <- &tunnelConn:
				go s.keepAlive(g, &tunnelConn)
				s.logger.Debugf("accepted tunnel connection from %s", addr.String())
			default:
				s.logger.Warn("UDP tunnel channel is full")
				// Close the newly created connection as it couldn't be added.
				// Under the lock: this map is read and written by every other
				// datagram that arrives, and deleting from it unguarded is a
				// data race that can corrupt the map outright.
				s.activeMu.Lock()
				if s.activeConnections[key] == &tunnelConn {
					close(tunnelConn.payload)
					delete(s.activeConnections, key)
				}
				s.activeMu.Unlock()
			}
		}
	}
}

func (s *UdpTransport) parsePortMappings(g *udpGen) {
	for _, portMapping := range s.config.Ports {
		parts := strings.Split(portMapping, "=")
		// One unreadable mapping is one mapping, not a reason to end the
		// process. See the same passage in tcp.go.
		if len(parts) > 2 {
			s.logger.Errorf("ignoring the port mapping %q: it has more than one '='", portMapping)
			continue
		}

		// The left-hand side may name a local address as well as a port or a
		// range, so one machine can serve different exposed ports on different
		// local IPs. See expandListenSpec.
		listens, err := expandListenSpec(parts[0])
		if err != nil {
			s.logger.Errorf("ignoring the port mapping %q: %v", portMapping, err)
			continue
		}

		var remoteAddr string
		if len(parts) == 2 {
			remoteAddr = strings.TrimSpace(parts[1])
		}

		for _, l := range listens {
			// A mapping that named no destination forwards each port to itself.
			target := remoteAddr
			if target == "" {
				target = l.port
			}
			go s.localListener(g, l.addr, target)
			if len(listens) > 1 {
				time.Sleep(1 * time.Millisecond) // for wide port ranges
			}
		}
	}
}

func (s *UdpTransport) localListener(g *udpGen, localAddr, remoteAddr string) {
	// Counted while this goroutine holds a listener, so Start can wait for the
	// port rather than sleeping and hoping. See listeners.go.
	s.listeners.hold()
	defer s.listeners.release()

	localUDPAddr, err := net.ResolveUDPAddr("udp", localAddr)
	if err != nil {
		s.logger.Error(bindFailure("forwarded port", localAddr, err))
		return
	}

	listener, err := net.ListenUDP("udp", localUDPAddr)
	if err != nil {
		// One forwarded port, not the tunnel. See bindfail.go.
		s.logger.Error(bindFailure("forwarded port", localAddr, err))
		return
	}

	s.applyBuffers(listener)

	defer listener.Close()

	s.logger.Infof("UDP listener started successfully, listening on address: %s", listener.LocalAddr().String())

	// Buffer for UDP reads
	buf := make([]byte, 16*1024)

	// Track active connections
	activeConnections := map[string]*LocalUDPConn{}

	// mutex
	mu := &sync.Mutex{}

	// make a new channel for recieve udp packets
	udpChan := make(chan *LocalUDPConn, s.config.ChannelSize)

	// handle channel
	go s.handleLoop(g, udpChan, &activeConnections, mu)

	go func() {
		for {
			select {
			case <-g.ctx.Done():
				return
			default:
				n, addr, err := listener.ReadFromUDP(buf)
				if err != nil {
					s.logger.Errorf("failed to read from UDP listener: %v", err)
					continue
				}

				// Create a unique identifier for the connection based on IP and port
				key := addr.String()

				mu.Lock()
				// Check if the connection is already active
				if existingConn, exists := activeConnections[key]; exists {
					// If connection is active and not closed, send payload
					select {
					case existingConn.payload <- append([]byte(nil), buf[:n]...):
						s.logger.Tracef("buffered %d bytes for existing connection %s", n, addr.String())
					default:
						s.logger.Warnf("payload channel for connection %s is full, dropping UDP packet", addr.String())
					}
					mu.Unlock()
					continue
				}

				mu.Unlock()

				// A new source address is a new flow, and a flow is what
				// max_connections counts here. Taken before the flow costs
				// anything, the same as every other transport does on accept —
				// and released on all three ways out below, or a tunnel with a
				// limit would bleed slots until it forwarded nothing.
				if !s.limits.acquire() {
					s.logger.Warnf("connection limit reached, dropping UDP packet from %s", addr.String())
					continue
				}

				// Create a new payload channel for this connection
				payloadChan := make(chan []byte, udpPayloadQueue)

				// Build the UDP connection object
				newUDPConn := LocalUDPConn{
					timeCreated: time.Now().UnixMilli(), // Just for debugging
					payload:     payloadChan,
					remoteAddr:  remoteAddr,
					listener:    listener,
					addr:        addr,
				}

				mu.Lock()
				// Store the new connection
				activeConnections[key] = &newUDPConn
				mu.Unlock()

				select {
				case udpChan <- &newUDPConn:
					s.logger.Debugf("accepted UDP connection from %s", addr.String())
					payloadChan <- append([]byte(nil), buf[:n]...) // Send a copy of the new payload to the channel

					// Request a new TCP connection
					select {
					case g.reqNewConnChan <- struct{}{}:
						// Successfully requested a new TCP connection
					default:
						// The channel is full, do nothing
						s.logger.Warn("channel is full, cannot request a new connection")
					}

				default:
					s.logger.Warn("UDP channel is full, dropping packet.")
					// Take the flow back out the way every other path does it:
					// under the lock, and only when the entry is still this one.
					//
					// It used to close the channel and delete the key with no
					// lock held at all, while the insert four lines above took
					// one and every other reader and writer of this map takes
					// one. Two things came of that. The delete raced the map —
					// which is the plain data race — and the close raced a send
					// into the very channel being closed, which is a panic
					// rather than a race: the reader path above sends into
					// existingConn.payload while holding mu, and mu was exactly
					// what this was not holding. dropTunnelConn says so in its
					// own doc comment; this was the one place that did not
					// follow it.
					//
					// Only reachable when udpChan is full, which is why -race
					// has never caught it: nothing in the suite fills it.
					mu.Lock()
					if activeConnections[key] == &newUDPConn {
						close(newUDPConn.payload)
						delete(activeConnections, key)
					}
					mu.Unlock()
					s.limits.release()
				}
			}
		}
	}()

	<-g.ctx.Done()

}

func (s *UdpTransport) handleLoop(g *udpGen, udpChan chan *LocalUDPConn, activeConnections *map[string]*LocalUDPConn, mu *sync.Mutex) {
	for {
		select {
		case <-g.ctx.Done():
			return
		case localConn := <-udpChan:
			if nowMillis()-localConn.timeCreated > pairingTimeout.Milliseconds() {
				s.logger.Debugf("timeouted local connection: %d ms", nowMillis()-localConn.timeCreated)
				// Drop the flow whole, rather than only stopping work on it.
				//
				// Giving up here while leaving the source address in the table
				// was what made this transport go permanently quiet for a peer:
				// every later datagram from that address found the stale entry
				// and was filed into a payload channel no goroutine would ever
				// read again. The peer stayed silent until the service was
				// restarted, which is exactly the "UDP worked, then stopped"
				// report. Removing it means the next datagram starts a fresh
				// flow and the peer recovers on its own.
				//
				// Under the same lock the listener holds while it delivers, so
				// closing the channel here cannot race a send into it.
				s.dropLocalFlow(localConn, activeConnections, mu)
				continue
			}

		loop:
			for {
				// The timeout runs on a timer, so it fires whether or not a
				// tunnel connection ever arrives. The check above used to be the
				// only one and the select below blocks, so on a pool that had
				// run dry the flow was held with nothing to time it out.
				timer := time.NewTimer(pairingWait(localConn.timeCreated))

				select {
				case <-g.ctx.Done():
					timer.Stop()
					// The run is going away and this flow never reached
					// udpCopy, so nothing else will drop it or give its slot
					// back.
					s.dropLocalFlow(localConn, activeConnections, mu)
					return

				case <-timer.C:
					continue loop

				case tunnelConn := <-g.tunnelChannel:
					timer.Stop()
					close(tunnelConn.ping)
					tunnelConn.mu.Lock()

					// Send the target addr over the connection
					if _, err := tunnelConn.listener.WriteTo([]byte(localConn.remoteAddr), tunnelConn.addr); err != nil {
						s.logger.Errorf("%v", err)
						// Release the lock and drop the connection whole before
						// reaching for the next one. Leaving with neither done
						// held the mutex for good — keepAlive takes it with
						// TryLock, so that connection could never be pinged
						// again — and left the entry in activeConnections with
						// its payload channel never closed, so a tunnel
						// connection that failed a single write was lost to the
						// run rather than replaced.
						tunnelConn.mu.Unlock()
						s.dropTunnelConn(tunnelConn)
						continue loop
					}

					// Handle data exchange between connections
					go s.udpCopy(g, localConn, tunnelConn, activeConnections, mu)

					s.logger.Debugf("initiate new handler for connection %s with timestamp %d", localConn.addr.String(), localConn.timeCreated)
					break loop
				}
			}
		}
	}
}

func (s *UdpTransport) udpCopy(g *udpGen, udpLocal *LocalUDPConn, udpTunnel *TunnelUDPConn, activeConnections *map[string]*LocalUDPConn, mu *sync.Mutex) {
	done := make(chan struct{})

	// Handle data from local to tunnel
	go func() {
		defer close(done)
		s.udpLocalCopy(g, udpLocal, udpTunnel)
	}()

	// Handle data from tunnel to local
	s.udpTunnelCopy(g, udpTunnel, udpLocal)

	// Wait until one of the directions is done (connection closed or idle)
	<-done

	// Remove local connection from active connections and close the channel.
	// Only if the entry is still this flow: the source address may already have
	// been recycled by a newer one, and deleting that would leave it in the
	// table's place with nothing reading its payload — the same stale-entry
	// failure the timeout path in handleLoop is careful to avoid.
	key := udpLocal.addr.String()
	mu.Lock()
	if (*activeConnections)[key] == udpLocal {
		close(udpLocal.payload)
		delete(*activeConnections, key)
	}
	mu.Unlock()

	// The flow is over, so its slot goes back. Exactly one release per flow:
	// this is the path a flow that was paired takes, and the two in
	// localListener and handleLoop are the paths of a flow that never was.
	s.limits.release()

	// Remove tunnel connection from active connections and close the channel.
	s.dropTunnelConn(udpTunnel)
}

// dropLocalFlow takes a forwarded flow out of the active set, closes its
// payload channel and gives its connection slot back.
//
// Only when the entry is still this flow: the source address may already have
// been recycled by a newer one, and deleting that would leave it in the table's
// place with nothing reading its payload — every later datagram from the address
// filed against a channel no goroutine reads. That is the stale entry that made
// this transport go permanently quiet for a peer.
//
// The slot goes back unconditionally, because it was taken unconditionally when
// the flow was created. Exactly one of this, the timeout path and udpCopy's
// teardown runs for any given flow.
func (s *UdpTransport) dropLocalFlow(localConn *LocalUDPConn, activeConnections *map[string]*LocalUDPConn, mu *sync.Mutex) {
	key := localConn.addr.String()
	mu.Lock()
	if (*activeConnections)[key] == localConn {
		close(localConn.payload)
		delete(*activeConnections, key)
	}
	mu.Unlock()
	s.limits.release()
}

// dropTunnelConn takes a tunnel connection out of the active set and closes its
// payload channel, under the lock every other reader and writer of that map
// holds — so closing here cannot race a send into it.
//
// Only when the entry is still this connection. A peer that came back under the
// same address has a newer one recorded there, and removing that would strand
// it: every later datagram from the address would be filed against a channel no
// goroutine reads.
func (s *UdpTransport) dropTunnelConn(conn *TunnelUDPConn) {
	key := conn.addr.String()
	s.activeMu.Lock()
	if s.activeConnections[key] == conn {
		close(conn.payload)
		delete(s.activeConnections, key)
	}
	s.activeMu.Unlock()
}

func (s *UdpTransport) udpLocalCopy(g *udpGen, from *LocalUDPConn, to *TunnelUDPConn) {
	// One timer for the session, reset per packet.
	//
	// This was time.After inside the select, which allocates a fresh timer on
	// every iteration and never stops it — so a loop that runs once per
	// datagram left one live 60-second timer per packet sitting on the runtime's
	// timer heap. At a thousand packets a second that is sixty thousand of them
	// for one direction of one session. The semantics are unchanged: the timer
	// is reset after every packet, so it still measures time since the last one.
	idle := time.NewTimer(idleForward)
	defer idle.Stop()

	for {
		select {
		case <-g.ctx.Done():
			// Teardown is immediate now. Without this the goroutine outlived
			// the generation until the payload channel closed or the idle
			// timeout fired, which keepAlive below already knew not to do.
			return

		case data, ok := <-from.payload: // Wait for data on the UDP payload channel
			if !ok {
				return
			}

			packetSize := len(data)

			totalWritten := 0
			for totalWritten < packetSize {
				// Write the packet to the tunnel
				w, err := to.listener.WriteToUDP(data[totalWritten:], to.addr)
				if err != nil {
					s.logger.Errorf("failed to write UDP payload to tunnel: %v", err)
					return
				}
				totalWritten += w
			}

			// Onto the tunnel: the other half of what this transport never
			// counted. See acceptTunnelConn for the inbound side.
			metrics.AddBytes(0, uint64(totalWritten))
			s.limits.waitBytes(g.ctx, totalWritten)

			if s.config.Sniffer {
				g.usageMonitor.AddOrUpdatePort(from.listener.LocalAddr().(*net.UDPAddr).Port, uint64(totalWritten))
			}

			s.logger.Debugf("forwarded %d bytes from local connection %s to tunnel", packetSize, from.addr.String())

		case <-idle.C:
			s.logger.Debugf("connection idle for %s, closing UDP connection for %s", idleForward, from.addr.String())
			return
		}

		// Rearm for the next packet. Stop before Reset because the timer has
		// not fired — reaching here means the payload case won the select — so
		// its channel is empty and Reset is safe.
		idle.Stop()
		idle.Reset(idleForward)
	}
}

func (s *UdpTransport) udpTunnelCopy(g *udpGen, from *TunnelUDPConn, to *LocalUDPConn) {
	// See udpLocalCopy for why this is one timer rather than a time.After per
	// packet, and why the context is watched.
	idle := time.NewTimer(idleForward)
	defer idle.Stop()

	for {
		select {
		case <-g.ctx.Done():
			return

		case data, ok := <-from.payload: // Wait for data on the UDP payload channel
			if !ok {
				return
			}

			packetSize := len(data)

			totalWritten := 0
			for totalWritten < packetSize {
				// Write the packet to the tunnel
				w, err := to.listener.WriteToUDP(data[totalWritten:], to.addr)
				if err != nil {
					s.logger.Errorf("failed to write UDP payload to tunnel: %v", err)
					return
				}
				totalWritten += w
			}

			if s.config.Sniffer {
				g.usageMonitor.AddOrUpdatePort(to.listener.LocalAddr().(*net.UDPAddr).Port, uint64(totalWritten))
			}

			s.logger.Debugf("forwarded %d bytes from local connection %s to tunnel", packetSize, from.addr.String())

		case <-idle.C:
			s.logger.Debugf("connection idle for %s, closing UDP connection for %s", idleForward, from.addr.String())
			return
		}

		// Rearm for the next packet. Stop before Reset because the timer has
		// not fired — reaching here means the payload case won the select — so
		// its channel is empty and Reset is safe.
		idle.Stop()
		idle.Reset(idleForward)
	}
}

func (s *UdpTransport) keepAlive(g *udpGen, conn *TunnelUDPConn) {
	ticker := time.NewTicker(s.config.Heartbeat) // Send periodic pings to the client

	defer ticker.Stop()

	for {
		select {
		case <-g.ctx.Done():
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
			if _, err := conn.listener.WriteTo([]byte{utils.SG_Ping}, conn.addr); err != nil {
				conn.mu.Unlock()
				return
			}
			conn.mu.Unlock()
			s.logger.Trace("ping sent to the client")
		}
	}
}

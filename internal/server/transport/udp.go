package transport

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/web"
	"github.com/sirupsen/logrus"
)

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
	// The status shown in the panel. Behind a lock because the run being
	// replaced and the run replacing it both write it. See tunnelStatus.
	status    tunnelStatus
	config    *UdpConfig
	parentctx context.Context
	// The current run. Replaced by Restart while the previous run's
	// goroutines are still reading it, so it lives behind a lock.
	run               runState
	logger            *logrus.Logger
	tunnelChannel     chan *TunnelUDPConn
	activeConnections map[string]*TunnelUDPConn
	activeMu          sync.Mutex
	reqNewConnChan    chan struct{}
	controlChannel    netControl
	restartMutex      sync.Mutex
	usageMonitor      *web.Usage
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
}

func NewUDPServer(parentCtx context.Context, config *UdpConfig, logger *logrus.Logger) *UdpTransport {
	// Create a derived context from the parent context
	ctx, cancel := context.WithCancel(parentCtx)

	// Initialize the TcpTransport struct
	server := &UdpTransport{
		config:            config,
		parentctx:         parentCtx,
		logger:            logger,
		tunnelChannel:     make(chan *TunnelUDPConn, config.ChannelSize),
		activeConnections: map[string]*TunnelUDPConn{},
		activeMu:          sync.Mutex{},
		reqNewConnChan:    make(chan struct{}, config.ChannelSize),
		rtt:               0,
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
func (s *UdpTransport) Start() {
	s.start(&udpGen{
		ctx:            s.run.context(),
		tunnelChannel:  s.tunnelChannel,
		reqNewConnChan: s.reqNewConnChan,
		usageMonitor:   s.usageMonitor,
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
	listener, err := net.Listen("tcp", s.config.BindAddr)
	if err != nil {
		s.logger.Fatalf("failed to start listener on %s: %v", s.config.BindAddr, err)
		return
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
			if !backoff.fail(g.ctx) {
				return
			}
			continue
		}
		backoff.ok()

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

	if msg != s.config.Token {
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
	err := utils.SendBinaryByte(s.controlChannel.Get(), utils.SG_RTT)
	if err != nil {
		s.logger.Error("failed to send RTT signal, attempting to restart server...")
		go s.Restart()
		return
	}

	for {
		select {
		case <-g.ctx.Done():
			_ = utils.SendBinaryByte(s.controlChannel.Get(), utils.SG_Closed)
			return

		case <-g.reqNewConnChan:
			err := utils.SendBinaryByte(s.controlChannel.Get(), utils.SG_Chan)
			if err != nil {
				s.logger.Error("failed to send request new connection signal. ", err)
				go s.Restart()
				return
			}

		case <-ticker.C:
			err := utils.SendBinaryByte(s.controlChannel.Get(), utils.SG_HB)
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
	tunnelUDPAddr, err := net.ResolveUDPAddr("udp", s.config.BindAddr)
	if err != nil {
		s.logger.Fatalf("failed to resolve tunnel address: %v", err)
	}

	listener, err := net.ListenUDP("udp", tunnelUDPAddr)
	if err != nil {
		s.logger.Fatalf("failed to listen on tunnel UDP port: %v", err)
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

			if string(buf[:n]) != s.config.Token { // For new connections, validate the token
				s.logger.Errorf("invalid token received from %s", addr.String())
				continue
			}

			// Initialize the payload channel for the new connection
			payloadChan := make(chan []byte, 100_000)

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

func (s *UdpTransport) localListener(g *udpGen, localAddr, remoteAddr string) {
	localUDPAddr, err := net.ResolveUDPAddr("udp", localAddr)
	if err != nil {
		s.logger.Fatalf("failed to resolve local address: %v", err)
	}

	listener, err := net.ListenUDP("udp", localUDPAddr)
	if err != nil {
		s.logger.Fatalf("failed to listen on local UDP port: %v", err)
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

				// Create a new payload channel for this connection, Buffer up to 100,000 packets for the connection
				payloadChan := make(chan []byte, 100_000)

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
					// Close the newly created connection as it couldn't be added
					close(newUDPConn.payload)
					delete(activeConnections, key)
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
			if time.Now().UnixMilli()-localConn.timeCreated > 3000 { // 3000ms
				s.logger.Debugf("timeouted local connection: %d ms", time.Now().UnixMilli()-localConn.timeCreated)
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
				key := localConn.addr.String()
				mu.Lock()
				if (*activeConnections)[key] == localConn {
					close(localConn.payload)
					delete(*activeConnections, key)
				}
				mu.Unlock()
				continue
			}

		loop:
			for {
				select {
				case <-g.ctx.Done():
					return

				case tunnelConn := <-g.tunnelChannel:
					close(tunnelConn.ping)
					tunnelConn.mu.Lock()

					// Send the target addr over the connection
					if _, err := tunnelConn.listener.WriteTo([]byte(localConn.remoteAddr), tunnelConn.addr); err != nil {
						s.logger.Errorf("%v", err)
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

	// Remove local connection from active connections and close the channel
	mu.Lock()
	close(udpLocal.payload)
	delete(*activeConnections, udpLocal.addr.String())
	mu.Unlock()

	// Remove tunnel connection from active connections and close the channel
	s.activeMu.Lock()
	close(udpTunnel.payload)
	delete(s.activeConnections, udpTunnel.addr.String())
	s.activeMu.Unlock()

}

func (s *UdpTransport) udpLocalCopy(g *udpGen, from *LocalUDPConn, to *TunnelUDPConn) {
	inactivityTimeout := 60 * time.Second // Define a 60-second inactivity timeout

	for {
		select {
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
				g.usageMonitor.AddOrUpdatePort(from.listener.LocalAddr().(*net.UDPAddr).Port, uint64(totalWritten))
			}

			s.logger.Debugf("forwarded %d bytes from local connection %s to tunnel", packetSize, from.addr.String())

		case <-time.After(inactivityTimeout): // Timeout after 30 seconds of inactivity
			s.logger.Debugf("connection idle for 60 seconds, closing UDP connection for %s", from.addr.String())
			return
		}
	}
}

func (s *UdpTransport) udpTunnelCopy(g *udpGen, from *TunnelUDPConn, to *LocalUDPConn) {
	inactivityTimeout := 60 * time.Second // Define a 60-second inactivity timeout

	for {
		select {
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

		case <-time.After(inactivityTimeout): // Timeout after 30 seconds of inactivity
			s.logger.Debugf("connection idle for 60 seconds, closing UDP connection for %s", from.addr.String())
			return
		}
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

package transport

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils/network"
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

// udpReadErrorPause is how long a read loop waits after an error that is not
// the socket closing, so one that repeats does not spin a core.
const udpReadErrorPause = 50 * time.Millisecond

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
	// seat holds the client this generation serves; see clientSeat.
	seat clientSeat
}

type UdpTransport struct {
	// The generations of this transport and what outlives them: see
	// lifecycle.go.
	lifecycle

	config *UdpConfig
	// The run's channels and its usage monitor are deliberately not fields: they
	// belong to one generation, and a field outlives the generation that made
	// it. See Start.
	activeConnections map[string]*TunnelUDPConn
	activeMu          sync.Mutex
	controlChannel    netControl
	limits            *limiter
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
	// Initialize the TcpTransport struct
	server := &UdpTransport{
		config: config,
		lifecycle: lifecycle{
			parentctx: parentCtx,
			logger:    logger,
			usage:     usageSpec{webPort: config.WebPort, snifferLog: config.SnifferLog, sniffer: config.Sniffer},
		},
		activeConnections: map[string]*TunnelUDPConn{},
		activeMu:          sync.Mutex{},
		limits:            newLimiter(Limits{MaxConnections: config.MaxConnections, BandwidthMbps: config.BandwidthMbps}),
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
// the new generation. So the first run's tunnel channel stayed reachable from
// the struct for the life of the process, and with it every TunnelUDPConn left
// queued in it, each holding a payload channel of its own. Measured with a pool
// of 64: 145 MB still held after the client had gone and the run had been torn
// down, released only by restarting the server. Building the generation here
// leaves nothing behind to pin it.
func (s *UdpTransport) Start() {
	s.start(s.newGen(s.run.context()))
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
	s.restart(s.controlChannel.Close, func(ctx context.Context) {
		s.controlChannel.Clear()
		// Replaced under its lock, and the lock itself is never replaced. It used
		// to be reassigned here while the previous generation's copy loops were
		// still finishing and locking it: overwriting a mutex another goroutine
		// holds makes that goroutine's Unlock a fatal error, which took the whole
		// engine down. Their flows are simply not in the new table, so their
		// cleanup finds nothing to remove.
		s.activeMu.Lock()
		s.activeConnections = map[string]*TunnelUDPConn{}
		s.activeMu.Unlock()
		go s.start(s.newGen(ctx))
	})
}

// newGen builds one generation's channels and usage monitor.
func (s *UdpTransport) newGen(ctx context.Context) *udpGen {
	return &udpGen{
		ctx:            ctx,
		tunnelChannel:  make(chan *TunnelUDPConn, s.config.ChannelSize),
		reqNewConnChan: make(chan struct{}, s.config.ChannelSize),
		usageMonitor:   s.usageMonitor(ctx),
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
	listener, ok := bindTunnelPort(g.ctx, s.logger, s.config.BindAddr, func() (*net.UDPConn, error) {
		return net.ListenUDP("udp", tunnelUDPAddr)
	})
	if !ok {
		return
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
	buf := make([]byte, network.MaxDatagram)

	for {
		select {
		case <-g.ctx.Done():
			return
		default:
			n, addr, err := listener.ReadFromUDP(buf)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return // the generation ended and closed it
				}
				s.logger.Errorf("failed to read from tunnel UDP listener: %v", err)
				time.Sleep(udpReadErrorPause) // an error that repeats must not spin
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
				// Debug, not error: this port is public, and anything at all
				// sent to it lands here. At error level every junk datagram
				// was a journal line, so anyone could fill the disk and bury
				// the real errors at line rate.
				s.logger.Debugf("invalid token received from %s", addr.String())
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

// applyBuffers sizes a datagram socket to the configured SO_RCVBUF/SO_SNDBUF;
// see network.SizeUDPBuffers.
func (s *UdpTransport) applyBuffers(conn *net.UDPConn) {
	network.SizeUDPBuffers(conn, s.config.SO_RCVBUF, s.config.SO_SNDBUF, s.logger.Warnf)
}

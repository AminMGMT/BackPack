package transport

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/backpack/backpack/internal/web"

	"github.com/sirupsen/logrus"
)

type TcpTransport struct {
	// The status shown in the panel. Behind a lock because the run being
	// replaced and the run replacing it both write it. See tunnelStatus.
	status          tunnelStatus
	config          *TcpConfig
	parentctx       context.Context
	state           clientState
	logger          *logrus.Logger
	restartMutex    sync.Mutex
	poolConnections int32
	loadConnections int32
	controlFlow     chan struct{}
	// poolNonce is what the server issued for this run; every pool connection
	// presents it so the server need not judge them by source address. Empty
	// against a server too old to issue one.
	poolNonce network.PoolNonce
	// legacyServer decides which handshake to ask for, and remembers what this
	// server has actually proved about itself. It is re-armed on restart: the
	// fallback is a guess drawn from a closed connection, and a closed
	// connection is far more often a dead path than an old server.
	legacyServer legacyProbe
}
type TcpConfig struct {
	RemoteAddr string
	// Endpoints rotates through the server addresses (primary + fallbacks)
	// so a filtered IP or blocked port does not stop the tunnel.
	Endpoints      *network.Endpoints
	Token          string
	SnifferLog     string
	KeepAlive      time.Duration
	RetryInterval  time.Duration
	DialTimeOut    time.Duration
	ConnPoolSize   int
	WebPort        int
	Nodelay        bool
	Sniffer        bool
	AggressivePool bool
	MSS            int
	SO_RCVBUF      int
	SO_SNDBUF      int
	// Outbound says how the connections that reach the tunnel server leave
	// this machine: through a proxy, from a chosen source address or
	// interface, under a routing mark. Nil dials directly. None of it is ever
	// applied to the dial to the local backend — see network/outbound.go.
	Outbound *network.Outbound
	// Stealth wraps every tunnel-carrying connection in the Noise record layer,
	// so the stream has no fingerprint for deep packet inspection to match.
	Stealth bool
}

// wrapStealth upgrades a freshly dialled tunnel connection to the Noise record
// layer when this tunnel runs in stealth mode, and returns it unchanged
// otherwise. Only tunnel-carrying connections are wrapped; the dial to the
// local backend stays plain, since that traffic never leaves the machine.
func (c *TcpTransport) wrapStealth(conn net.Conn) (net.Conn, error) {
	if !c.config.Stealth {
		return conn, nil
	}
	return network.NoiseClientConn(conn, c.config.Token, c.config.DialTimeOut)
}

func NewTCPClient(parentCtx context.Context, config *TcpConfig, logger *logrus.Logger) *TcpTransport {
	// Create a derived context from the parent context
	ctx, cancel := context.WithCancel(parentCtx)

	// Initialize the TcpTransport struct
	client := &TcpTransport{
		config:          config,
		parentctx:       parentCtx,
		logger:          logger,
		poolConnections: 0,
		loadConnections: 0,
		controlFlow:     make(chan struct{}, 100),
	}

	// Seed the first generation through the same path a restart uses, so
	// there is only one way this state is ever published.
	client.state.Reset(ctx, cancel, web.NewDataStore(fmt.Sprintf(":%v", config.WebPort), ctx, config.SnifferLog, config.Sniffer, client.status.get, logger))
	return client
}

func (c *TcpTransport) Start() {
	if c.config.WebPort > 0 {
		go c.state.Usage().Monitor()
	}

	c.status.set("Disconnected (TCP)")

	go c.channelDialer()
}
func (c *TcpTransport) Restart() {
	if !c.restartMutex.TryLock() {
		c.logger.Warn("client is already restarting")
		return
	}
	defer c.restartMutex.Unlock()

	c.logger.Info("restarting client...")

	// for removing timeout logs
	level := c.logger.GetLevel()
	c.logger.SetLevel(logrus.FatalLevel)

	if c.state.Cancel() != nil {
		c.state.Cancel()()
	}

	// close control channel connection
	c.state.CloseConn()

	time.Sleep(2 * time.Second)

	// The whole tunnel may have been shut down while this restart was waiting —
	// on a reload, or on the process going down. Rebuilding the run from a
	// parent context that is already finished would bind the listeners again
	// only to close them, and on a reload that means fighting the run that is
	// replacing this one for its own ports. Nothing here is worth starting.
	if c.parentctx.Err() != nil {
		// The level was turned down to hide the timeouts a teardown produces;
		// leaving it there would silence the shutdown itself.
		c.logger.SetLevel(level)
		// Abandoning is not a reason to keep claiming a peer. See the same
		// branch in internal/server/transport — this end publishes the status
		// the panel reads and the "connected" flag the watchdog reads, and both
		// used to survive a restart that gave up.
		c.status.set("")
		metrics.ClearPeer()
		c.logger.Debug("restart abandoned: the tunnel is shutting down")
		return
	}

	ctx, cancel := context.WithCancel(c.parentctx)

	// Publish the whole new generation at once: a reader must never see
	// the new context paired with the old monitor, or vice versa.
	c.state.Reset(ctx, cancel, web.NewDataStore(fmt.Sprintf(":%v", c.config.WebPort), ctx, c.config.SnifferLog, c.config.Sniffer, c.status.get, c.logger))
	c.status.set("")
	// The next control channel issues its own nonce; carrying this one over
	// would have the pool announcing a value the server has already forgotten.
	c.poolNonce.Clear()
	// Ask for the current handshake again. A fallback decided during the
	// outage that caused this restart was a guess about the server drawn from
	// a broken path, and carrying it forward costs the nonce for nothing.
	c.legacyServer.reset()
	atomic.StoreInt32(&c.poolConnections, 0)
	atomic.StoreInt32(&c.loadConnections, 0)
	// The published pool figures belong to the run that just ended. Left
	// behind, the panel would keep showing the size and throughput of a
	// connection that is gone until the new run's first tick replaced them.
	metrics.ClearPool()
	// The peer belongs to the generation that just ended.
	metrics.ClearPeer()
	drain(c.controlFlow)

	// set the log level again
	c.logger.SetLevel(level)

	go c.Start()
}

func (c *TcpTransport) channelDialer() {
	c.logger.Info("attempting to establish a new control channel connection...")

	// One backoff for this reconnect loop: retries start at the configured
	// interval and stretch as the outage persists, instead of a fixed drumbeat.
	bo := newBackoff(c.config.RetryInterval)

	for {
		select {
		case <-c.state.Ctx().Done():
			return
		default:
			// Raced across the endpoint list rather than tried one at a time.
			//
			// A filtered address does not refuse a connection, it swallows it,
			// so walking the list sequentially costs a whole dial timeout per
			// dead address on every reconnect. The race gives the preferred
			// address a head start and only opens a second connection if that
			// head start expires — so a working first address is never raced.
			// See network.Race.
			//
			//set default behaviour of control channel to nodelay, also using default buffer parameters
			rawConn, won, err := network.Race(c.state.Ctx(),
				c.config.Endpoints.InPreferenceOrder(), network.RaceStagger,
				func(ctx context.Context, addr string) (net.Conn, error) {
					return network.TcpDialerVia(ctx, c.config.Outbound, addr,
						c.config.DialTimeOut, c.config.KeepAlive, true, 3, 0, 0, 0)
				})
			if err != nil {
				c.logger.Errorf("channel dialer: %v", err)
				// Nothing answered. Rotate anyway so the next attempt prefers a
				// different address: the race already tried them all, but the
				// preference is what the health scorer and the pool read.
				if next := c.config.Endpoints.Rotate(); c.config.Endpoints.Len() > 1 {
					c.logger.Infof("trying next server endpoint: %s", next)
				}
				bo.Wait(c.state.Ctx())
				continue
			}
			// Stay on whichever address won, so the data connections that
			// follow go to the same server the control channel is on.
			c.config.Endpoints.Prefer(won)

			// In stealth mode the Noise handshake runs first, so the token and
			// everything after it cross an already-encrypted, unfingerprintable
			// channel. A wrong token fails here, before any tunnel bytes.
			tunnelTCPConn, err := c.wrapStealth(rawConn)
			if err != nil {
				c.logger.Errorf("channel dialer: stealth handshake failed: %v", err)
				rawConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}

			// Sending security token. The nonce-carrying handshake is asked for
			// first; a server that predates it closes the connection without
			// answering, which is what flips the fallback below.
			signal := c.legacyServer.signal()
			err = utils.SendBinaryTransportString(tunnelTCPConn, c.config.Token, signal)
			if err != nil {
				c.logger.Errorf("failed to send security token: %v", err)
				tunnelTCPConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}

			// Set a read deadline for the token response
			if err := tunnelTCPConn.SetReadDeadline(time.Now().Add(controlAckTimeout)); err != nil {
				c.logger.Errorf("failed to set read deadline: %v", err)
				tunnelTCPConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}

			// Receive response
			message, ackSignal, err := utils.ReceiveBinaryTransportString(tunnelTCPConn)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					c.logger.Warn("timeout while waiting for control channel response")
				} else {
					c.logger.Errorf("failed to receive control channel response: %v", err)
					c.legacyServer.miss(c.logger, signal)
				}
				tunnelTCPConn.Close() // Close connection on error or timeout
				bo.Wait(c.state.Ctx())
				continue
			}
			// Resetting the deadline (removes any existing deadline)
			tunnelTCPConn.SetReadDeadline(time.Time{})

			// Plain TCP has no mux sessions, so the version the server sends
			// is not applicable here.
			// A refusal is an answer, and a far more useful one than the
			// silence it replaces. It is not a reason to fall back: the server
			// understood the handshake perfectly and declined it.
			if why, refused := refusalReason(message, ackSignal); refused {
				c.logger.Error("the tunnel was refused — " + why)
				c.legacyServer.ack(ackSignal)
				tunnelTCPConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}
			token, nonce, _ := decodeControlAck(message, ackSignal)

			if token == c.config.Token {
				// The server answered, so whatever it answered with is what it
				// speaks — proof that outranks any later closed connection.
				c.legacyServer.ack(ackSignal)
				// Before the control channel is published, so the pool
				// connections poolMaintainer starts below already have it.
				c.poolNonce.Set(nonce)
				// The engine knows it holds a control channel; the watchdog reads that
				// rather than the socket table, which shows a socket long after the
				// tunnel behind it has stopped working. See metrics.Snapshot.Connected.
				metrics.ReportPeer(tunnelTCPConn.RemoteAddr().String())
				c.state.SetConn(tunnelTCPConn)
				c.logger.Info("control channel established successfully")

				c.status.set("Connected (TCP)")
				go c.poolMaintainer()
				go c.channelHandler()

				return

			} else {
				c.logger.Errorf("invalid token received (does not match the server's token). Retrying...")
				tunnelTCPConn.Close() // Close connection if the token is invalid
				bo.Wait(c.state.Ctx())
				continue
			}
		}
	}
}

// poolMaintainer keeps the pool the right size. The policy is poolSizer's,
// shared with every other client transport — see poolmaintain.go.
func (c *TcpTransport) poolMaintainer() {
	poolSizer{
		ctx:        c.state.Ctx(),
		log:        c.logger,
		size:       c.config.ConnPoolSize,
		aggressive: c.config.AggressivePool,
		open:       &c.poolConnections,
		taken:      &c.loadConnections,
		shrink:     c.controlFlow,
		dial:       c.tunnelDialer,
	}.maintain()
}

func (c *TcpTransport) channelHandler() {
	// See beatClock: learns how often the server really heartbeats.
	beats := &beatClock{}

	msgChan := make(chan byte, 1000)

	// The generation this handler belongs to, captured once.
	//
	// Everything below used to ask c.state.Cancel() != nil before deciding a
	// failure was worth restarting for. That is always true: the constructor
	// sets a cancel function before any of this can run, and Reset sets another
	// on every restart. So the guard was open in every case it was written to
	// close, and each goroutine dying during a teardown queued another restart
	// of a tunnel that was already on its way down. The server transports were
	// corrected to ask their generation's context instead; the client ones were
	// not.
	//
	// Captured rather than read through c.state each time, for the same reason
	// the server holds its context in the generation: Restart publishes a new
	// one while these goroutines are still winding down, and a goroutine that
	// went on to watch the new context would never see its own run end.
	ctx := c.state.Ctx()

	// Goroutine to handle the blocking ReceiveBinaryString
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				// A control channel that has gone quiet has to be noticed
				// here, because nothing else notices it in time: TCP takes
				// eleven minutes to give up on the default keepalive_period,
				// and the watchdog sees an ESTABLISHED socket for every
				// second of it. See controlDeadline.
				if err := c.state.Conn().SetReadDeadline(time.Now().Add(beats.deadline(c.config.KeepAlive))); err != nil {
					if ctx.Err() == nil {
						c.logger.Errorf("failed to set control channel deadline: %v", err)
						go c.Restart()
					}
					return
				}
				msg, err := utils.ReceiveBinaryByte(c.state.Conn())
				if err != nil {
					if ctx.Err() == nil {
						c.logger.Error("failed to read from control channel. ", err)
						go c.Restart()
					}
					return
				}
				if msg == utils.SG_HB {
					beats.beat(time.Now())
				}
				msgChan <- msg
			}
		}
	}()

	// Main loop to listen for context cancellation or received messages
	for {
		select {
		case <-ctx.Done():
			_ = utils.SendBinaryByteWithin(c.state.Conn(), utils.SG_Closed, controlWriteTimeout)
			return

		case msg := <-msgChan:
			switch msg {
			case utils.SG_Chan:
				atomic.AddInt32(&c.loadConnections, 1)

				select {
				case <-c.controlFlow: // Do nothing

				default:
					c.logger.Debug("channel signal received, initiating tunnel dialer")
					go c.tunnelDialer()
				}

			case utils.SG_HB:
				c.logger.Debug("heartbeat signal received successfully")

			case utils.SG_Closed:
				c.logger.Warn("control channel has been closed by the server")
				go c.Restart()
				return

			case utils.SG_RTT:
				err := utils.SendBinaryByteWithin(c.state.Conn(), utils.SG_RTT, controlWriteTimeout)
				if err != nil {
					c.logger.Error("failed to send RTT signal, restarting client: ", err)
					go c.Restart()
					return
				}

			default:
				c.logger.Errorf("unexpected response from channel: %v.", msg)
				go c.Restart()
				return
			}
		}
	}
}

// Dialing to the tunnel server, chained functions, without retry
func (c *TcpTransport) tunnelDialer() {
	c.logger.Debugf("initiating new connection to tunnel server at %s", c.config.RemoteAddr)

	// Dial to the tunnel server
	// Next() rather than Current(): with load balancing enabled the pool
	// spreads its connections over every configured endpoint, so one
	// congested route only slows its own share of the traffic.
	rawConn, err := network.TcpDialerVia(c.state.Ctx(), c.config.Outbound, c.config.Endpoints.Next(), c.config.DialTimeOut, c.config.KeepAlive, c.config.Nodelay, 3, c.config.SO_RCVBUF, c.config.SO_SNDBUF, c.config.MSS)
	if err != nil {
		c.logger.Error("tunnel server dialer: ", err)

		return
	}

	// Same stealth upgrade as the control channel: the data connection carries
	// its bytes through the Noise record layer when the tunnel is in that mode.
	tcpConn, err := c.wrapStealth(rawConn)
	if err != nil {
		c.logger.Debugf("tunnel dialer: stealth handshake failed: %v", err)
		rawConn.Close()
		return
	}

	// Say what this connection is, so the server admits it on the nonce rather
	// than on the address it happened to dial out from.
	if err := announcePoolConn(tcpConn, c.poolNonce.Get()); err != nil {
		c.logger.Debugf("tunnel dialer: failed to announce the pool connection: %v", err)
		tcpConn.Close()
		return
	}

	// Increment active connections counter
	atomic.AddInt32(&c.poolConnections, 1)

	// Attempt to receive the remote address from the tunnel server
	remoteAddr, transport, err := utils.ReceiveBinaryTransportString(tcpConn)

	// Decrement active connections after successful or failed connection
	atomic.AddInt32(&c.poolConnections, -1)

	if err != nil {
		c.logger.Debugf("failed to receive port from tunnel connection %s: %v", tcpConn.RemoteAddr().String(), err)
		tcpConn.Close()
		return
	}

	// A forwarded UDP flow says so in the target address, on this transport as
	// on every other one, so there is one thing to recognise rather than a
	// signal byte here and a marked address everywhere else.
	if dialForwardedUDP(tcpConn, remoteAddr, c.logger, c.state.Usage(), c.config.Sniffer) {
		return
	}

	// Extract the port from the received address
	port, resolvedAddr, err := network.ResolveRemoteAddr(remoteAddr)
	if err != nil {
		c.logger.Infof("failed to resolve remote port: %v", err)
		tcpConn.Close() // Close the connection on error
		return
	}

	switch transport {
	case utils.SG_TCP:
		// Dial local server using the received address
		c.localDialer(tcpConn, resolvedAddr, port)

	default:
		c.logger.Error("undefined transport. close the connection.")
		tcpConn.Close()
	}
}

func (c *TcpTransport) localDialer(tcpConn net.Conn, resolvedAddr string, port int) {
	// Pick a healthy backend when several are configured; a single backend is
	// returned unchanged, so ordinary tunnels are untouched.
	resolvedAddr = backends.pick(resolvedAddr)
	var sendBuf, recvBuf int

	if strings.Contains(resolvedAddr, "127.0.0.1") {
		// Use 32 KB for localhost
		sendBuf = 32 * 1024
		recvBuf = 32 * 1024
	} else {
		// Use your custom buffer sizes
		sendBuf = c.config.SO_SNDBUF
		recvBuf = c.config.SO_RCVBUF
	}

	localConnection, err := network.TcpDialer(c.state.Ctx(), resolvedAddr, c.config.DialTimeOut, c.config.KeepAlive, true, 1, recvBuf, sendBuf, c.config.MSS)
	if err != nil {
		localDial.Report(c.logger, resolvedAddr, err)
		tcpConn.Close()
		return
	}

	// The last hop worked, so any run of failures recorded for the panel
	// ends here. See localdial.go.
	ReportLocalDialOK()
	c.logger.Debugf("connected to local address %s successfully", resolvedAddr)

	handlers.TCPConnectionHandler(c.state.Ctx(), false, metrics.CountedConn(tcpConn), localConnection, c.logger, c.state.Usage(), port, c.config.Sniffer)
}

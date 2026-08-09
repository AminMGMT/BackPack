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
	"github.com/xtaci/smux"
)

type TcpMuxTransport struct {
	config *TcpMuxConfig
	// muxV1/muxV2 are both built up front so that adopting the server's
	// version costs nothing per session; muxVersion is the one in force.
	muxV1           *smux.Config
	muxV2           *smux.Config
	muxVersion      atomic.Int32
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
	// legacyServer records that the server did not understand the nonce
	// handshake, so later attempts skip straight to the old one instead of
	// spending a connection discovering it again. It is not reset on restart:
	// an upgraded server means an upgraded binary, which means a new process.
	legacyServer atomic.Bool
}

type TcpMuxConfig struct {
	RemoteAddr string
	// Endpoints rotates through the server addresses (primary + fallbacks)
	// so a filtered IP or blocked port does not stop the tunnel.
	Endpoints        *network.Endpoints
	Token            string
	SnifferLog       string
	Nodelay          bool
	Sniffer          bool
	KeepAlive        time.Duration
	RetryInterval    time.Duration
	DialTimeOut      time.Duration
	MuxVersion       int
	MaxFrameSize     int
	MaxReceiveBuffer int
	MaxStreamBuffer  int
	ConnPoolSize     int
	WebPort          int
	AggressivePool   bool
	MSS              int
	SO_RCVBUF        int
	SO_SNDBUF        int
	// Outbound says how the connections that reach the tunnel server leave
	// this machine: through a proxy, from a chosen source address or
	// interface, under a routing mark. Nil dials directly. None of it is ever
	// applied to the dial to the local backend — see network/outbound.go.
	Outbound *network.Outbound
}

func NewMuxClient(parentCtx context.Context, config *TcpMuxConfig, logger *logrus.Logger) *TcpMuxTransport {
	// Create a derived context from the parent context
	ctx, cancel := context.WithCancel(parentCtx)

	// Initialize the TcpTransport struct
	muxSettings := network.MuxSettings{
		MaxFrameSize:     config.MaxFrameSize,
		MaxReceiveBuffer: config.MaxReceiveBuffer,
		MaxStreamBuffer:  config.MaxStreamBuffer,
	}
	client := &TcpMuxTransport{
		muxV1:           network.SmuxConfig(1, muxSettings),
		muxV2:           network.SmuxConfig(2, muxSettings),
		config:          config,
		parentctx:       parentCtx,
		logger:          logger,
		poolConnections: 0,
		loadConnections: 0,
		controlFlow:     make(chan struct{}, 100),
	}

	// Seed the first generation through the same path a restart uses, so
	// there is only one way this state is ever published.
	client.state.Reset(ctx, cancel, web.NewDataStore(fmt.Sprintf(":%v", config.WebPort), ctx, config.SnifferLog, config.Sniffer, logger))
	return client
}

func (c *TcpMuxTransport) Start() {
	if c.config.WebPort > 0 {
		go c.state.Usage().Monitor()
	}

	c.state.Usage().SetTunnelStatus("Disconnected (TCPMUX)")

	go c.channelDialer()
}

func (c *TcpMuxTransport) Restart() {
	if !c.restartMutex.TryLock() {
		c.logger.Warn("client is already restarting")
		return
	}
	defer c.restartMutex.Unlock()

	c.logger.Info("restarting client...")

	// for removing timeout logs
	level := c.logger.Level
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
		c.logger.Debug("restart abandoned: the tunnel is shutting down")
		return
	}

	ctx, cancel := context.WithCancel(c.parentctx)

	// Publish the whole new generation at once: a reader must never see
	// the new context paired with the old monitor, or vice versa.
	c.state.Reset(ctx, cancel, web.NewDataStore(fmt.Sprintf(":%v", c.config.WebPort), ctx, c.config.SnifferLog, c.config.Sniffer, c.logger))
	c.state.Usage().SetTunnelStatus("")
	// The next control channel issues its own nonce; carrying this one over
	// would have the pool announcing a value the server has already forgotten.
	c.poolNonce.Clear()
	c.muxVersion.Store(0)
	atomic.StoreInt32(&c.poolConnections, 0)
	atomic.StoreInt32(&c.loadConnections, 0)
	// The published pool figures belong to the run that just ended. Left
	// behind, the panel would keep showing the size and throughput of a
	// connection that is gone until the new run's first tick replaced them.
	metrics.ClearPool()
	drain(c.controlFlow)

	// set the log level again
	c.logger.SetLevel(level)

	go c.Start()

}

func (c *TcpMuxTransport) channelDialer() {
	c.logger.Info("attempting to establish a new tcpmux control channel connection...")

	// One backoff for this reconnect loop (see backoff.go): fixed-interval
	// retries become exponential, so a sustained outage is probed a few times a
	// minute rather than every second.
	bo := newBackoff(c.config.RetryInterval)

	for {
		select {
		case <-c.state.Ctx().Done():
			return
		default:
			tunnelConn, err := network.TcpDialerVia(c.state.Ctx(), c.config.Outbound, c.config.Endpoints.Current(), c.config.DialTimeOut, c.config.KeepAlive, true, 3, 0, 0, 0)
			if err != nil {
				c.logger.Errorf("channel dialer: %v", err)
				// The current endpoint did not answer — move to the next one so a
				// filtered IP or blocked port cannot stall the tunnel forever.
				if next := c.config.Endpoints.Rotate(); c.config.Endpoints.Len() > 1 {
					c.logger.Infof("trying next server endpoint: %s", next)
				}
				bo.Wait(c.state.Ctx())
				continue
			}

			// Sending security token. The nonce-carrying handshake is asked
			// for first; a server that predates it closes the connection
			// without answering, which is what flips the fallback below.
			signal := controlSignal(&c.legacyServer)
			err = utils.SendBinaryTransportString(tunnelConn, c.config.Token, signal)
			if err != nil {
				c.logger.Errorf("failed to send security token: %v", err)
				tunnelConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}

			// Set a read deadline for the token response
			if err := tunnelConn.SetReadDeadline(time.Now().Add(controlAckTimeout)); err != nil {
				c.logger.Errorf("failed to set read deadline: %v", err)
				tunnelConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}
			// Receive response
			message, ackSignal, err := utils.ReceiveBinaryTransportString(tunnelConn)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					c.logger.Warn("timeout while waiting for control channel response")
				} else {
					c.logger.Errorf("failed to receive control channel response: %v", err)
					noteLegacyServer(c.logger, &c.legacyServer, signal)
				}
				tunnelConn.Close() // Close connection on error or timeout
				bo.Wait(c.state.Ctx())
				continue
			}
			// Resetting the deadline (removes any existing deadline)
			tunnelConn.SetReadDeadline(time.Time{})

			token, nonce, muxVersion := decodeControlAck(message, ackSignal)

			if token == c.config.Token {
				// Before the control channel is published, so the pool
				// connections poolMaintainer starts below already have it.
				c.poolNonce.Set(nonce)
				// Settled before the pool starts, so no session is ever built
				// on a version the server has not confirmed.
				c.setMuxVersion(muxVersion)
				c.state.SetConn(tunnelConn)
				c.logger.Infof("control channel established successfully (mux version %d)", c.muxVersion.Load())

				c.state.Usage().SetTunnelStatus("Connected (TCPMux)")

				go c.poolMaintainer()
				go c.channelHandler()

				return
			} else {
				c.logger.Errorf("invalid token received (does not match the server's token). Retrying...")
				tunnelConn.Close() // Close connection if the token is invalid
				bo.Wait(c.state.Ctx())
				continue
			}
		}
	}

}

func (c *TcpMuxTransport) poolMaintainer() {
	for i := 0; i < c.config.ConnPoolSize; i++ { //initial pool filling
		go c.tunnelDialer()
	}

	// factors
	a := 4
	b := 5
	x := 3
	y := 4.0

	if c.config.AggressivePool {
		c.logger.Info("aggressive pool management enabled")
		a = 1
		b = 2
		x = 0
		y = 0.75
	}

	tickerPool := time.NewTicker(time.Second * 1)
	defer tickerPool.Stop()

	tickerLoad := time.NewTicker(time.Second * 10)
	defer tickerLoad.Stop()

	newPoolSize := c.config.ConnPoolSize // intial value
	var load poolLoad                    // throughput signal, see poolload.go
	var poolConnectionsSum int32 = 0

	for {
		select {
		case <-c.state.Ctx().Done():
			return

		case <-tickerPool.C:
			// Accumulate pool connections over time (every second)
			atomic.AddInt32(&poolConnectionsSum, atomic.LoadInt32(&c.poolConnections))

		case <-tickerLoad.C:
			// Calculate the loadConnections over the last 10 seconds
			loadConnections := (int(atomic.LoadInt32(&c.loadConnections)) + 9) / 10 // +9 for ceil-like logic
			atomic.StoreInt32(&c.loadConnections, 0)                                // Reset

			// Calculate the average pool connections over the last 10 seconds
			poolConnectionsAvg := (int(atomic.LoadInt32(&poolConnectionsSum)) + 9) / 10 // +9 for ceil-like logic
			atomic.StoreInt32(&poolConnectionsSum, 0)                                   // Reset

			// Throughput carried since the previous tick. A pool whose
			// connections are each working hard should grow even when nobody
			// is asking for new ones — see poolload.go.
			mbps := load.mbps()

			// The pool is allowed to outgrow its configured size, which from
			// outside is indistinguishable from a leak. Publish what it is
			// doing and why, so the panel can say "8 configured, 19 open,
			// carrying 240 Mbit/s" instead of leaving somebody to guess.
			metrics.ReportPool(poolConnectionsAvg, newPoolSize, c.config.ConnPoolSize, mbps)

			// Dynamically adjust the pool size based on current connections
			if ((loadConnections+a) > poolConnectionsAvg*b && poolCanGrow(newPoolSize, c.config.ConnPoolSize)) ||
				load.wantsMore(mbps, poolConnectionsAvg, newPoolSize, c.config.ConnPoolSize) {
				c.logger.Debugf("increasing pool size: %d -> %d, avg pool conn: %d, avg load conn: %d, throughput: %d Mbit/s", newPoolSize, newPoolSize+1, poolConnectionsAvg, loadConnections, mbps)
				newPoolSize++

				// Add a new connection to the pool
				go c.tunnelDialer()
			} else if float64(loadConnections+x) < float64(poolConnectionsAvg)*y && newPoolSize > c.config.ConnPoolSize {
				c.logger.Debugf("decreasing pool size: %d -> %d, avg pool conn: %d, avg load conn: %d", newPoolSize, newPoolSize-1, poolConnectionsAvg, loadConnections)
				newPoolSize--

				// send a signal to controlFlow
				c.controlFlow <- struct{}{}
			}
		}
	}

}

func (c *TcpMuxTransport) channelHandler() {
	msgChan := make(chan byte, 1000)

	// Goroutine to handle the blocking ReceiveBinaryString
	go func() {
		for {
			select {
			case <-c.state.Ctx().Done():
				return
			default:
				msg, err := utils.ReceiveBinaryByte(c.state.Conn())
				if err != nil {
					if c.state.Cancel() != nil {
						c.logger.Error("failed to read from control channel. ", err)
						go c.Restart()
					}
					return
				}
				msgChan <- msg
			}
		}
	}()

	// Main loop to listen for context cancellation or received messages
	for {
		select {
		case <-c.state.Ctx().Done():
			_ = utils.SendBinaryByte(c.state.Conn(), utils.SG_Closed)
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

			default:
				c.logger.Errorf("unexpected response from channel: %v.", msg)
				go c.Restart()
				return
			}

		}
	}
}

func (c *TcpMuxTransport) tunnelDialer() {
	c.logger.Debugf("initiating new tunnel connection to address %s", c.config.RemoteAddr)

	// Dial to the tunnel server
	// Next() rather than Current(): with load balancing enabled the pool
	// spreads its connections over every configured endpoint, so one
	// congested route only slows its own share of the traffic.
	tunnelConn, err := network.TcpDialerVia(c.state.Ctx(), c.config.Outbound, c.config.Endpoints.Next(), c.config.DialTimeOut, c.config.KeepAlive, c.config.Nodelay, 3, c.config.SO_RCVBUF, c.config.SO_SNDBUF, c.config.MSS)
	if err != nil {
		c.logger.Errorf("tunnel server dialer: %v", err)

		return
	}

	// Say what this connection is, so the server admits it on the nonce rather
	// than on the address it happened to dial out from.
	if err := announcePoolConn(tunnelConn, c.poolNonce.Get()); err != nil {
		c.logger.Debugf("tunnel dialer: failed to announce the pool connection: %v", err)
		tunnelConn.Close()
		return
	}

	// Increment active connections counter
	atomic.AddInt32(&c.poolConnections, 1)

	c.handleSession(tunnelConn)
}

func (c *TcpMuxTransport) handleSession(tunnelConn net.Conn) {
	defer func() {
		atomic.AddInt32(&c.poolConnections, -1)
	}()

	// SMUX server
	session, err := smux.Server(tunnelConn, c.smuxCfg())
	if err != nil {
		c.logger.Errorf("failed to create mux session: %v", err)
		return
	}

	for {
		select {
		case <-c.state.Ctx().Done():
			return
		default:
			stream, err := session.AcceptStream()
			if err != nil {
				c.logger.Trace("session is closed: ", err)
				session.Close()
				return
			}

			remoteAddr, err := utils.ReceiveBinaryString(stream)
			if err != nil {
				c.logger.Errorf("unable to get port from stream connection %s: %v", tunnelConn.RemoteAddr().String(), err)
				stream.Close()
				continue
			}

			go c.localDialer(stream, remoteAddr)
		}
	}
}

func (c *TcpMuxTransport) localDialer(stream *smux.Stream, remoteAddr string) {
	// Extract the port from the received address
	port, resolvedAddr, err := network.ResolveRemoteAddr(remoteAddr)
	if err != nil {
		c.logger.Infof("failed to resolve remote port: %v", err)
		stream.Close()
		return
	}
	// Pick a healthy backend when several are configured (single = unchanged).
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
		stream.Close()
		return
	}

	c.logger.Debugf("connected to local address %s successfully", remoteAddr)

	handlers.TCPConnectionHandler(c.state.Ctx(), false, metrics.CountedConn(stream), localConnection, c.logger, c.state.Usage(), int(port), c.config.Sniffer)
}

// setMuxVersion adopts the version the server settled on. A legacy server sends
// none, and both ends then keep to their own configuration, exactly as they did
// before there was anything to agree about.
func (c *TcpMuxTransport) setMuxVersion(negotiated int) {
	if negotiated != 1 && negotiated != 2 {
		negotiated = c.config.MuxVersion
		if negotiated != 1 && negotiated != 2 {
			// Nothing configured and nothing negotiated: the peer predates the
			// handshake, so it can only be speaking version 1.
			negotiated = 1
		}
	}
	c.muxVersion.Store(int32(negotiated))
}

// smuxCfg returns the session configuration for the version in force.
func (c *TcpMuxTransport) smuxCfg() *smux.Config {
	if c.muxVersion.Load() == 2 {
		return c.muxV2
	}
	return c.muxV1
}

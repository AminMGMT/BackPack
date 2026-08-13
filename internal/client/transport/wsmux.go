package transport

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/backpack/backpack/internal/web"
	"github.com/xtaci/smux"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

type WsMuxTransport struct {
	// The status shown in the panel. Behind a lock because the run being
	// replaced and the run replacing it both write it. See tunnelStatus.
	status          tunnelStatus
	config          *WsMuxConfig
	smuxConfig      *smux.Config
	parentctx       context.Context
	state           clientState
	logger          *logrus.Logger
	restartMutex    sync.Mutex
	poolConnections int32
	loadConnections int32
	controlFlow     chan struct{}
}
type WsMuxConfig struct {
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
	Mode             config.TransportType
	SimpleAuth       bool
	AggressivePool   bool
	EdgeIP           string
	// MSS caps the largest TCP segment these connections send. Zero leaves it
	// to the kernel, which is the default and almost always right; it is set
	// where the path silently drops full-sized packets. See manage.SetMSS.
	MSS int
	// Outbound says how the connections that reach the tunnel server leave
	// this machine: through a proxy, from a chosen source address or
	// interface, under a routing mark. Nil dials directly. None of it is ever
	// applied to the dial to the local backend — see network/outbound.go.
	Outbound *network.Outbound
}

func NewWSMuxClient(parentCtx context.Context, config *WsMuxConfig, logger *logrus.Logger) *WsMuxTransport {
	// Create a derived context from the parent context
	ctx, cancel := context.WithCancel(parentCtx)

	// Initialize the TcpTransport struct
	client := &WsMuxTransport{
		smuxConfig: &smux.Config{
			Version:           network.ResolveStaticMuxVersion(config.MuxVersion),
			KeepAliveInterval: 20 * time.Second,
			KeepAliveTimeout:  40 * time.Second,
			MaxFrameSize:      config.MaxFrameSize,
			MaxReceiveBuffer:  config.MaxReceiveBuffer,
			MaxStreamBuffer:   config.MaxStreamBuffer,
		},
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

func (c *WsMuxTransport) Start() {
	if c.config.WebPort > 0 {
		go c.state.Usage().Monitor()
	}

	c.status.set(fmt.Sprintf("Disconnected (%s)", c.config.Mode))

	go c.channelDialer()
}

func (c *WsMuxTransport) Restart() {
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
	c.state.Reset(ctx, cancel, web.NewDataStore(fmt.Sprintf(":%v", c.config.WebPort), ctx, c.config.SnifferLog, c.config.Sniffer, c.status.get, c.logger))
	c.status.set("")
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

func (c *WsMuxTransport) channelDialer() {
	c.logger.Infof("attempting to establish a new %s control channel connection", c.config.Mode)

	// One backoff for this reconnect loop (see backoff.go): fixed-interval
	// retries become exponential, so a sustained outage is probed a few times a
	// minute rather than every second.
	bo := newBackoff(c.config.RetryInterval)

	for {
		select {
		case <-c.state.Ctx().Done():
			return
		default:

			tunnelWSConn, err := network.WebSocketDialer(c.state.Ctx(), c.config.Outbound, c.config.Endpoints.Current(), c.config.EdgeIP, "/channel", c.config.DialTimeOut, c.config.KeepAlive, true, c.config.Token, c.config.Mode, c.config.SimpleAuth, 3, 0, 0, c.config.MSS)
			if err != nil {
				c.logger.Errorf("control channel dialer: %v", err)
				// The current endpoint did not answer — move to the next one so a
				// filtered IP or blocked port cannot stall the tunnel forever.
				if next := c.config.Endpoints.Rotate(); c.config.Endpoints.Len() > 1 {
					c.logger.Infof("trying next server endpoint: %s", next)
				}
				bo.Wait(c.state.Ctx())
				continue
			}
			c.state.SetWSConn(tunnelWSConn)
			c.logger.Info("control channel established successfully")

			c.status.set(fmt.Sprintf("Connected (%s)", c.config.Mode))

			go c.poolMaintainer()
			go c.channelHandler()

			return
		}
	}
}

func (c *WsMuxTransport) poolMaintainer() {
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

func (c *WsMuxTransport) channelHandler() {
	msgChan := make(chan byte, 1000)

	// Goroutine to handle the blocking ReceiveBinaryString
	go func() {
		for {
			select {
			case <-c.state.Ctx().Done():
				return

			default:
				messageType, msg, err := c.state.WSConn().ReadMessage()
				if err != nil {
					if c.state.Cancel() != nil {
						c.logger.Error("failed to read from channel connection. ", err)
						go c.Restart()
					}
					return
				}
				signal, ok := utils.WebSocketSignal(messageType, msg)
				if !ok {
					c.logger.Warnf("ignoring a malformed control frame (type %d, %d bytes)", messageType, len(msg))
					continue
				}
				msgChan <- signal
			}
		}
	}()

	for {
		select {
		case <-c.state.Ctx().Done():
			_ = c.state.WSConn().WriteMessage(websocket.BinaryMessage, []byte{utils.SG_Closed})
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
				c.logger.Debug("heartbeat received successfully")
				err := c.state.WSConn().WriteMessage(websocket.BinaryMessage, []byte{utils.SG_HB})
				if err != nil {
					c.logger.Errorf("failed to send heartbeat: %v", msg)
					go c.Restart()
					return
				}
				c.logger.Trace("heartbeat signal sent successfully")

			case utils.SG_Closed:
				c.logger.Warn("control channel has been closed by the server")
				go c.Restart()
				return

			default:
				c.logger.Errorf("unexpected response from control channel: %v", msg)
				go c.Restart()
				return
			}

		}
	}
}

func (c *WsMuxTransport) tunnelDialer() {
	c.logger.Debugf("initiating new %s tunnel connection to address %s", c.config.Mode, c.config.RemoteAddr)

	// Dial to the tunnel server
	// Next() rather than Current(): with load balancing enabled the pool
	// spreads its connections over every configured endpoint, so one
	// congested route only slows its own share of the traffic.
	tunnelWSConn, err := network.WebSocketDialer(c.state.Ctx(), c.config.Outbound, c.config.Endpoints.Next(), c.config.EdgeIP, "/tunnel", c.config.DialTimeOut, c.config.KeepAlive, c.config.Nodelay, c.config.Token, c.config.Mode, c.config.SimpleAuth, 3, 2*1024*1024, 2*1024*1024, c.config.MSS)
	if err != nil {
		c.logger.Errorf("tunnel server dialer: %v", err)

		return
	}

	// Increment active connections counter
	atomic.AddInt32(&c.poolConnections, 1)

	c.handleSession(tunnelWSConn)
}

func (c *WsMuxTransport) handleSession(tunnelConn *websocket.Conn) {
	defer func() {
		atomic.AddInt32(&c.poolConnections, -1)
	}()

	// SMUX server
	session, err := smux.Server(tunnelConn.NetConn(), c.smuxConfig)
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
				c.logger.Debug("session is closed: ", err)
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

// dialUDP forwards a target marked as UDP, reporting whether it took the flow.
func (c *WsMuxTransport) dialUDP(stream net.Conn, remoteAddr string) bool {
	return dialForwardedUDP(stream, remoteAddr, c.logger, c.state.Usage(), c.config.Sniffer)
}

func (c *WsMuxTransport) localDialer(stream *smux.Stream, remoteAddr string) {
	if c.dialUDP(stream, remoteAddr) {
		return
	}
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
		sendBuf = 0
		recvBuf = 0
	}

	localConnection, err := network.TcpDialer(c.state.Ctx(), resolvedAddr, c.config.DialTimeOut, c.config.KeepAlive, true, 1, recvBuf, sendBuf, 0)
	if err != nil {
		localDial.Report(c.logger, resolvedAddr, err)
		stream.Close()
		return
	}

	c.logger.Debugf("connected to local address %s successfully", remoteAddr)

	handlers.TCPConnectionHandler(c.state.Ctx(), false, metrics.CountedConn(stream), localConnection, c.logger, c.state.Usage(), int(port), c.config.Sniffer)
}

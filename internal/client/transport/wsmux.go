package transport

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/xtaci/smux"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

type WsMuxTransport struct {
	// The generations of this transport and what outlives them: see
	// lifecycle.go.
	lifecycle

	config     *WsMuxConfig
	smuxConfig *smux.Config
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
		config: config,
	}

	client.firstGeneration(parentCtx, logger, usageSpec{webPort: config.WebPort, snifferLog: config.SnifferLog, sniffer: config.Sniffer})
	return client
}

func (c *WsMuxTransport) Start() {
	if c.config.WebPort > 0 {
		c.state.Go(c.state.Usage().Monitor)
	}

	c.status.set(fmt.Sprintf("Disconnected (%s)", c.config.Mode))

	c.state.Go(c.channelDialer)
}

func (c *WsMuxTransport) Restart() {
	c.restart(nil, nil, c.Start)
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
			// See metrics.Snapshot.Connected: the watchdog asks the engine, not the
			// socket table.
			metrics.ReportPeer(tunnelWSConn.RemoteAddr().String())
			c.state.SetWSConn(tunnelWSConn)
			c.logger.Info("control channel established successfully")

			c.status.set(fmt.Sprintf("Connected (%s)", c.config.Mode))

			c.state.Go(c.poolMaintainer)
			loop := c.control()
			c.state.Go(func() { loop.run() })

			return
		}
	}
}

// poolMaintainer keeps the pool the right size. The policy is poolSizer's,
// shared with every other client transport — see poolmaintain.go.
func (c *WsMuxTransport) poolMaintainer() {
	poolSizer{
		mux:        true,
		ctx:        c.state.Ctx(),
		log:        c.logger,
		size:       c.config.ConnPoolSize,
		aggressive: c.config.AggressivePool,
		open:       &c.poolConnections,
		taken:      &c.loadConnections,
		shrink:     c.controlFlow,
		dial:       c.tunnelDialer,
		spawn:      c.state.Go,
		pending:    &c.dialingConnections,
		maxSize:    muxPoolLimit(c.config.ConnPoolSize, c.config.MaxReceiveBuffer),
	}.maintain()
}

// control is this generation's control loop. The websocket server listens
// for each heartbeat to be answered.
func (c *WsMuxTransport) control() controlLoop {
	loop := c.lifecycle.control(controlwire.WS(c.state.WSConn()), c.config.KeepAlive, c.tunnelDialer, c.Restart)
	loop.echoBeats = true
	return loop
}

func (c *WsMuxTransport) tunnelDialer() {
	ready, ok := c.beginPoolDial(muxPoolLimit(c.config.ConnPoolSize, c.config.MaxReceiveBuffer))
	if !ok {
		return
	}
	defer ready()

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

	defer c.state.Own(tunnelWSConn)()

	// Increment active connections counter
	atomic.AddInt32(&c.poolConnections, 1)
	ready()

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

	c.serveSession(session, tunnelConn.RemoteAddr(), c.backend())
}

// backend is how this transport's users reach the local service. See
// backend.go.
func (c *WsMuxTransport) backend() backendOpts {
	return backendOpts{
		dialTimeout: c.config.DialTimeOut,
		keepAlive:   c.config.KeepAlive,
		rcvBuf:      0,
		sndBuf:      0,
		mss:         0,
		sniffer:     c.config.Sniffer,
	}
}

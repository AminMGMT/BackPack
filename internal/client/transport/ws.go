package transport

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/utils/network"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

type WsTransport struct {
	// The generations of this transport and what outlives them: see
	// lifecycle.go.
	lifecycle

	config *WsConfig
}
type WsConfig struct {
	RemoteAddr string
	// Endpoints rotates through the server addresses (primary + fallbacks)
	// so a filtered IP or blocked port does not stop the tunnel.
	Endpoints      *network.Endpoints
	Token          string
	SnifferLog     string
	Nodelay        bool
	Sniffer        bool
	KeepAlive      time.Duration
	RetryInterval  time.Duration
	DialTimeOut    time.Duration
	ConnPoolSize   int
	WebPort        int
	Mode           config.TransportType
	SimpleAuth     bool
	AggressivePool bool
	EdgeIP         string
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

func NewWSClient(parentCtx context.Context, config *WsConfig, logger *logrus.Logger) *WsTransport {
	// Initialize the TcpTransport struct
	client := &WsTransport{
		config: config,
	}

	client.firstGeneration(parentCtx, logger, usageSpec{webPort: config.WebPort, snifferLog: config.SnifferLog, sniffer: config.Sniffer})
	return client
}

func (c *WsTransport) Start() {
	// for  webui
	if c.config.WebPort > 0 {
		c.state.Go(c.state.Usage().Monitor)
	}

	c.status.set(fmt.Sprintf("Disconnected (%s)", c.config.Mode))

	c.state.Go(c.channelDialer)

}
func (c *WsTransport) Restart() {
	c.restart(nil, nil, c.Start)
}

func (c *WsTransport) channelDialer() {
	c.logger.Info("attempting to establish a new websocket control channel connection")

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
func (c *WsTransport) poolMaintainer() {
	poolSizer{
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
		maxSize:    c.config.ConnPoolSize * poolGrowthLimit,
	}.maintain()
}

// control is this generation's control loop. The websocket server listens
// for each heartbeat to be answered.
func (c *WsTransport) control() controlLoop {
	loop := c.lifecycle.control(controlwire.WS(c.state.WSConn()), c.config.KeepAlive, c.tunnelDialer, c.Restart)
	loop.echoBeats = true
	return loop
}

func (c *WsTransport) tunnelDialer() {
	ready, ok := c.beginPoolDial(c.config.ConnPoolSize * poolGrowthLimit)
	if !ok {
		return
	}
	defer ready()

	c.logger.Debugf("initiating new websocket tunnel connection to address %s", c.config.RemoteAddr)
	ctx := c.state.Ctx()

	// Dial to the tunnel server
	// Next() rather than Current(): with load balancing enabled the pool
	// spreads its connections over every configured endpoint, so one
	// congested route only slows its own share of the traffic.
	tunnelConn, err := network.WebSocketDialer(c.state.Ctx(), c.config.Outbound, c.config.Endpoints.Next(), c.config.EdgeIP, "/tunnel", c.config.DialTimeOut, c.config.KeepAlive, c.config.Nodelay, c.config.Token, c.config.Mode, c.config.SimpleAuth, 3, 1024*1024, 1024*1024, c.config.MSS)
	if err != nil {
		c.logger.Errorf("tunnel server dialer: %v", err)

		return
	}

	defer c.state.Own(tunnelConn)()

	// Increment active connections counter
	atomic.AddInt32(&c.poolConnections, 1)
	ready()

	for {
		select {
		case <-c.state.Ctx().Done():
			return
		default:
			_, remoteAddrBytes, err := tunnelConn.ReadMessage()
			if err != nil {
				c.logger.Debugf("unable to get port from websocket connection %s: %v", tunnelConn.RemoteAddr().String(), err)
				tunnelConn.Close()

				// Decrement active connections on failure
				atomic.AddInt32(&c.poolConnections, -1)

				return
			}

			if bytes.Equal(remoteAddrBytes, []byte{utils.SG_Ping}) {
				c.logger.Trace("ping received from the server")
				continue
			}

			// Decrement active connections
			atomic.AddInt32(&c.poolConnections, -1)

			remoteAddr := string(remoteAddrBytes)

			// A UDP flow is carried as framed datagrams over this same
			// websocket, read back as one stream: the relay on the other end
			// splits the frames across messages wherever it likes, so a
			// message is not a datagram and must not be treated as one.
			if dialForwardedUDP(ctx, c.config.DialTimeOut, &wsStream{conn: tunnelConn}, remoteAddr, c.logger, c.state.Usage(), c.config.Sniffer) {
				return
			}

			// Extract the port from the received address
			port, resolvedAddr, err := network.ResolveRemoteAddr(remoteAddr)
			if err != nil {
				c.logger.Infof("failed to resolve remote port: %v", err)
				tunnelConn.Close() // Close the connection on error
				return
			}

			c.localDialer(tunnelConn, resolvedAddr, port)
			return
		}
	}
}

func (c *WsTransport) localDialer(tunnelCon *websocket.Conn, remoteAddr string, port int) {
	// A websocket tunnel leaves a remote backend's buffers to the kernel.
	backend := c.dialBackend(remoteAddr, backendOpts{dialTimeout: c.config.DialTimeOut, keepAlive: c.config.KeepAlive})
	if backend == nil {
		tunnelCon.Close()
		return
	}
	handlers.WSConnectionHandler(c.state.Ctx(), tunnelCon, backend, c.logger, c.state.Usage(), port, c.config.Sniffer)
}

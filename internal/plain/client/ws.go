package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/plain/core"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// WsTransport is the plain ws and wss client: a /channel websocket for
// control, and a pool of /tunnel websockets that each carry one user
// connection.
type WsTransport struct {
	config       *WsConfig
	parentctx    context.Context
	logger       *logrus.Logger
	restartMutex sync.Mutex
	gen          atomic.Pointer[clientGen]
	status       core.Status
}

type WsConfig struct {
	RemoteAddr     string
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
	AggressivePool bool
	EdgeIP         string
	// OnLegacyServer: see TcpConfig.OnLegacyServer.
	OnLegacyServer func()
	// Route is where tunnel connections go and how: fallback_addrs,
	// load_balance, health_failover and the outbound settings. nil dials
	// remote_addr directly. Backpack.
	Route *core.Route
	// Layer is the tunnel's encryption (Backpack); nil is none.
	Layer *core.Layer
}

func NewWSClient(parentCtx context.Context, config *WsConfig, logger *logrus.Logger) *WsTransport {
	client := &WsTransport{
		config:    config,
		parentctx: parentCtx,
		logger:    logger,
	}
	client.gen.Store(newClientGen(parentCtx, config.WebPort, config.SnifferLog, config.Sniffer, &client.status, logger))
	context.AfterFunc(parentCtx, core.Unseated)
	return client
}

// Running reports whether a control channel is up.
func (c *WsTransport) Running() bool { return c.status.Running() }

func (c *WsTransport) Start() { c.start(c.gen.Load()) }

func (c *WsTransport) start(g *clientGen) {
	if c.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}

	c.status.Set(fmt.Sprintf("Disconnected (%s)", c.config.Mode))

	go c.channelDialer(g)
}

func (c *WsTransport) Restart() {
	if !c.restartMutex.TryLock() {
		c.logger.Warn("client is already restarting")
		return
	}
	defer c.restartMutex.Unlock()

	c.logger.Info("restarting client...")

	level := c.logger.Level
	c.logger.SetLevel(logrus.FatalLevel)

	old := c.gen.Load()
	old.cancel()

	if control := old.wsControl.Get(); control != nil {
		control.Close()
	}
	core.Unseated()

	time.Sleep(2 * time.Second)

	if c.parentctx.Err() != nil {
		c.logger.SetLevel(level)
		return
	}

	g := newClientGen(c.parentctx, c.config.WebPort, c.config.SnifferLog, c.config.Sniffer, &c.status, c.logger)
	c.gen.Store(g)
	c.status.Set("")

	c.logger.SetLevel(level)

	go c.start(g)
}

func (c *WsTransport) channelDialer(g *clientGen) {
	c.logger.Info("attempting to establish a new websocket control channel connection")

	for {
		select {
		case <-g.ctx.Done():
			return
		default:
			tunnelWSConn, err := core.WebSocketDialer(g.ctx, c.config.Route, c.config.Route.Control(c.config.RemoteAddr), c.config.EdgeIP, "/channel", c.config.DialTimeOut, c.config.KeepAlive, true, c.config.Layer.Bearer(c.config.Token), c.config.Mode, 3, 0, 0)
			var he *core.HandshakeError
			if err != nil && errors.As(err, &he) && he.LegacyServerAnswer() && c.config.OnLegacyServer != nil {
				// Backpack: a v2.0.0 Iran server answers this claim with its
				// decoy page; the tunnel then runs on that engine.
				c.logger.Info("the Iran server runs Backpack v2.0.0: using its engine until it is updated")
				c.config.OnLegacyServer()
				return
			}
			if err != nil {
				c.config.Route.ControlFailed(c.logger)
				c.logger.Errorf("control channel dialer: %v", err)
				time.Sleep(c.config.RetryInterval)
				continue
			}
			// Backpack: the tunnel's encryption on the control messages.
			control, err := c.config.Layer.WSClient(tunnelWSConn)
			if err != nil {
				c.logger.Errorf("control channel dialer: %s handshake: %v", c.config.Layer.Mode(), err)
				time.Sleep(c.config.RetryInterval)
				continue
			}
			g.wsControl.Set(control)
			c.logger.Info("control channel established successfully")

			c.status.Set(fmt.Sprintf("Connected (%s)", c.config.Mode))
			core.Seated(tunnelWSConn.RemoteAddr().String())

			go c.poolMaintainer(g)
			go c.channelHandler(g)

			return
		}
	}
}

func (c *WsTransport) poolMaintainer(g *clientGen) {
	maintainPool(g, c.config.ConnPoolSize, c.config.AggressivePool, c.logger, func() { c.tunnelDialer(g) })
}

func (c *WsTransport) channelHandler(g *clientGen) {
	wsChannelHandler(g, c.logger, c.Restart, func() { c.tunnelDialer(g) }, "unexpected response from channel: %v")
}

// wsChannelHandler is the ws clients' control loop: dial on SG_Chan unless the
// pool is shrinking, answer each heartbeat, restart on SG_Closed or anything
// else. ws and wsmux carry the same loop, apart from one message.
func wsChannelHandler(g *clientGen, logger *logrus.Logger, restart func(), dial func(), unexpected string) {
	control := g.wsControl.Get()
	msgChan := make(chan byte, 1000)

	go func() {
		for {
			select {
			case <-g.ctx.Done():
				return

			default:
				_, msg, err := control.ReadMessage()
				if err != nil {
					logger.Error("failed to read from channel connection. ", err)
					go restart()
					return
				}

				msgChan <- msg[0]
			}
		}
	}()

	for {
		select {
		case <-g.ctx.Done():
			_ = control.WriteMessage(websocket.BinaryMessage, []byte{core.SG_Closed})
			return

		case msg := <-msgChan:
			switch msg {
			case core.SG_Chan:
				atomic.AddInt32(&g.loadConnections, 1)
				select {
				case <-g.controlFlow: // Do nothing

				default:
					logger.Debug("channel signal received, initiating tunnel dialer")
					go dial()
				}

			case core.SG_HB:
				logger.Debug("heartbeat signal received successfully")
				err := control.WriteMessage(websocket.BinaryMessage, []byte{core.SG_HB})
				if err != nil {
					logger.Errorf("failed to send heartbeat: %v", msg)
					go restart()
					return
				}
				logger.Trace("heartbeat signal sent successfully")

			case core.SG_Closed:
				logger.Warn("control channel has been closed by the server")
				go restart()
				return

			default:
				logger.Errorf(unexpected, msg)
				go restart()
				return
			}
		}
	}
}

func (c *WsTransport) tunnelDialer(g *clientGen) {
	c.logger.Debugf("initiating new websocket tunnel connection to address %s", c.config.RemoteAddr)

	rawWS, err := core.WebSocketDialer(g.ctx, c.config.Route, c.config.Route.Data(c.config.RemoteAddr), c.config.EdgeIP, "/tunnel", c.config.DialTimeOut, c.config.KeepAlive, c.config.Nodelay, c.config.Layer.Bearer(c.config.Token), c.config.Mode, 3, 1024*1024, 1024*1024)
	if err != nil {
		c.logger.Errorf("tunnel server dialer: %v", err)
		return
	}
	// Backpack: the tunnel's encryption on the websocket's messages.
	tunnelConn, err := c.config.Layer.WSClient(rawWS)
	if err != nil {
		c.logger.Errorf("tunnel server dialer: %s handshake: %v", c.config.Layer.Mode(), err)
		return
	}

	atomic.AddInt32(&g.poolConnections, 1)

	for {
		select {
		case <-g.ctx.Done():
			return
		default:
			_, remoteAddrBytes, err := tunnelConn.ReadMessage()
			if err != nil {
				c.logger.Debugf("unable to get port from websocket connection %s: %v", tunnelConn.RemoteAddr().String(), err)
				tunnelConn.Close()

				atomic.AddInt32(&g.poolConnections, -1)

				return
			}

			if bytes.Equal(remoteAddrBytes, []byte{core.SG_Ping}) {
				c.logger.Trace("ping received from the server")
				continue
			}

			atomic.AddInt32(&g.poolConnections, -1)

			remoteAddr, udp := core.SplitTarget(string(remoteAddrBytes))

			port, resolvedAddr, err := core.ResolveRemoteAddr(remoteAddr)
			if err != nil {
				c.logger.Infof("failed to resolve remote port: %v", err)
				tunnelConn.Close()
				return
			}
			if udp {
				UDPDialer(core.Counted(core.WSStream(tunnelConn)), core.PickBackend(resolvedAddr), c.logger, g.usageMonitor, port, c.config.Sniffer)
				return
			}

			c.localDialer(g, tunnelConn, resolvedAddr, port)
			return
		}
	}
}

func (c *WsTransport) localDialer(g *clientGen, tunnelCon *core.WSConn, remoteAddr string, port int) {
	var sendBuf, recvBuf int

	if strings.Contains(remoteAddr, "127.0.0.1") {
		sendBuf = 32 * 1024
		recvBuf = 32 * 1024
	} else {
		sendBuf = 0
		recvBuf = 0
	}

	// Backpack: a backend list ("a|b") is tried in turn; see core.DialBackend.
	localConnection, err := core.DialBackend(remoteAddr, func(addr string) (*net.TCPConn, error) {
		return core.TcpDialer(g.ctx, addr, "", c.config.DialTimeOut, c.config.KeepAlive, true, 1, recvBuf, sendBuf, 0)
	})
	if err != nil {
		c.logger.Errorf("local dialer: %v", err)
		tunnelCon.Close()
		return
	}
	c.logger.Debugf("connected to local address %s successfully", remoteAddr)

	core.WSConnectionHandler(g.ctx, tunnelCon, localConnection, c.logger, g.usageMonitor, int(port), c.config.Sniffer)
}

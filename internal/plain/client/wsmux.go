package client

import (
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
	"github.com/xtaci/smux"
)

// WsMuxTransport is the plain wsmux and wssmux client: its /tunnel
// websockets are smux sessions the server opens a stream on per user
// connection.
type WsMuxTransport struct {
	config       *WsMuxConfig
	smuxConfig   *smux.Config
	parentctx    context.Context
	logger       *logrus.Logger
	restartMutex sync.Mutex
	gen          atomic.Pointer[clientGen]
	status       core.Status
}

type WsMuxConfig struct {
	RemoteAddr       string
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
	AggressivePool   bool
	EdgeIP           string
	// OnLegacyServer: see TcpConfig.OnLegacyServer.
	OnLegacyServer func()
	// Layer is the tunnel's encryption (Backpack); nil is none.
	Layer *core.Layer
}

func NewWSMuxClient(parentCtx context.Context, config *WsMuxConfig, logger *logrus.Logger) *WsMuxTransport {
	client := &WsMuxTransport{
		smuxConfig: &smux.Config{
			Version:           config.MuxVersion,
			KeepAliveInterval: 20 * time.Second,
			KeepAliveTimeout:  40 * time.Second,
			MaxFrameSize:      config.MaxFrameSize,
			MaxReceiveBuffer:  config.MaxReceiveBuffer,
			MaxStreamBuffer:   config.MaxStreamBuffer,
		},
		config:    config,
		parentctx: parentCtx,
		logger:    logger,
	}
	client.gen.Store(newClientGen(parentCtx, config.WebPort, config.SnifferLog, config.Sniffer, &client.status, logger))
	context.AfterFunc(parentCtx, core.Unseated)
	return client
}

// Running reports whether a control channel is up.
func (c *WsMuxTransport) Running() bool { return c.status.Running() }

func (c *WsMuxTransport) Start() { c.start(c.gen.Load()) }

func (c *WsMuxTransport) start(g *clientGen) {
	if c.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}

	c.status.Set(fmt.Sprintf("Disconnected (%s)", c.config.Mode))

	go c.channelDialer(g)
}

func (c *WsMuxTransport) Restart() {
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

func (c *WsMuxTransport) channelDialer(g *clientGen) {
	c.logger.Infof("attempting to establish a new %s control channel connection", c.config.Mode)

	for {
		select {
		case <-g.ctx.Done():
			return
		default:
			tunnelWSConn, err := core.WebSocketDialer(g.ctx, c.config.RemoteAddr, c.config.EdgeIP, "/channel", c.config.DialTimeOut, c.config.KeepAlive, true, c.config.Layer.Bearer(c.config.Token), c.config.Mode, 3, 0, 0)
			var he *core.HandshakeError
			if err != nil && errors.As(err, &he) && he.LegacyServerAnswer() && c.config.OnLegacyServer != nil {
				// Backpack: a v2.0.0 Iran server answers this claim with its
				// decoy page; the tunnel then runs on that engine.
				c.logger.Info("the Iran server runs Backpack v2.0.0: using its engine until it is updated")
				c.config.OnLegacyServer()
				return
			}
			if err != nil {
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

func (c *WsMuxTransport) poolMaintainer(g *clientGen) {
	maintainPool(g, c.config.ConnPoolSize, c.config.AggressivePool, c.logger, func() { c.tunnelDialer(g) })
}

func (c *WsMuxTransport) channelHandler(g *clientGen) {
	wsChannelHandler(g, c.logger, c.Restart, func() { c.tunnelDialer(g) }, "unexpected response from control channel: %v")
}

func (c *WsMuxTransport) tunnelDialer(g *clientGen) {
	c.logger.Debugf("initiating new %s tunnel connection to address %s", c.config.Mode, c.config.RemoteAddr)

	tunnelWSConn, err := core.WebSocketDialer(g.ctx, c.config.RemoteAddr, c.config.EdgeIP, "/tunnel", c.config.DialTimeOut, c.config.KeepAlive, c.config.Nodelay, c.config.Layer.Bearer(c.config.Token), c.config.Mode, 3, 2*1024*1024, 2*1024*1024)
	if err != nil {
		c.logger.Errorf("tunnel server dialer: %v", err)
		return
	}

	// Backpack: the tunnel's encryption on the raw stream smux runs on.
	stream, err := c.config.Layer.Client(tunnelWSConn.NetConn())
	if err != nil {
		c.logger.Errorf("tunnel server dialer: %s handshake: %v", c.config.Layer.Mode(), err)
		return
	}

	atomic.AddInt32(&g.poolConnections, 1)

	c.handleSession(g, tunnelWSConn, stream)
}

func (c *WsMuxTransport) handleSession(g *clientGen, tunnelConn *websocket.Conn, stream net.Conn) {
	defer func() {
		atomic.AddInt32(&g.poolConnections, -1)
	}()

	session, err := smux.Server(stream, c.smuxConfig)
	if err != nil {
		c.logger.Errorf("failed to create mux session: %v", err)
		return
	}

	for {
		select {
		case <-g.ctx.Done():
			return
		default:
			stream, err := session.AcceptStream()
			if err != nil {
				c.logger.Debug("session is closed: ", err)
				session.Close()
				return
			}

			remoteAddr, err := core.ReceiveBinaryString(stream)
			if err != nil {
				c.logger.Errorf("unable to get port from stream connection %s: %v", tunnelConn.RemoteAddr().String(), err)
				stream.Close()
				continue
			}

			go c.localDialer(g, stream, remoteAddr)
		}
	}
}

func (c *WsMuxTransport) localDialer(g *clientGen, stream *smux.Stream, remoteAddr string) {
	remoteAddr, udp := core.SplitTarget(remoteAddr)
	port, resolvedAddr, err := core.ResolveRemoteAddr(remoteAddr)
	if err != nil {
		c.logger.Infof("failed to resolve remote port: %v", err)
		stream.Close()
		return
	}
	if udp {
		UDPDialer(core.Counted(stream), resolvedAddr, c.logger, g.usageMonitor, port, c.config.Sniffer)
		return
	}

	var sendBuf, recvBuf int

	if strings.Contains(resolvedAddr, "127.0.0.1") {
		sendBuf = 32 * 1024
		recvBuf = 32 * 1024
	} else {
		sendBuf = 0
		recvBuf = 0
	}

	localConnection, err := core.TcpDialer(g.ctx, resolvedAddr, "", c.config.DialTimeOut, c.config.KeepAlive, true, 1, recvBuf, sendBuf, 0)
	if err != nil {
		c.logger.Errorf("local dialer: %v", err)
		stream.Close()
		return
	}

	c.logger.Debugf("connected to local address %s successfully", remoteAddr)

	core.TCPConnectionHandler(g.ctx, false, core.Counted(stream), localConnection, c.logger, g.usageMonitor, int(port), c.config.Sniffer)
}

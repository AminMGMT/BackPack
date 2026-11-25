package client

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/plain/core"

	"github.com/sirupsen/logrus"
	"github.com/xtaci/smux"
)

// TcpMuxTransport is the plain tcpmux client: its pool connections are smux
// sessions the server opens streams on, one per user connection.
type TcpMuxTransport struct {
	config       *TcpMuxConfig
	smuxConfig   *smux.Config
	parentctx    context.Context
	logger       *logrus.Logger
	restartMutex sync.Mutex
	gen          atomic.Pointer[clientGen]
	status       core.Status
}

type TcpMuxConfig struct {
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
	AggressivePool   bool
	MSS              int
	SO_RCVBUF        int
	SO_SNDBUF        int
	// OnLegacyServer: see TcpConfig.OnLegacyServer.
	OnLegacyServer func()
	// Layer is the tunnel's encryption (Backpack); nil is none.
	Layer *core.Layer
}

func NewMuxClient(parentCtx context.Context, config *TcpMuxConfig, logger *logrus.Logger) *TcpMuxTransport {
	client := &TcpMuxTransport{
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
func (c *TcpMuxTransport) Running() bool { return c.status.Running() }

func (c *TcpMuxTransport) Start() { c.start(c.gen.Load()) }

func (c *TcpMuxTransport) start(g *clientGen) {
	if c.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}

	c.status.Set("Disconnected (TCPMUX)")

	go c.channelDialer(g)
}

func (c *TcpMuxTransport) Restart() {
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

	if control := old.controlChannel.Get(); control != nil {
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

func (c *TcpMuxTransport) channelDialer(g *clientGen) {
	c.logger.Info("attempting to establish a new tcpmux control channel connection...")

	for {
		select {
		case <-g.ctx.Done():
			return
		default:
			rawConn, err := core.TcpDialer(g.ctx, c.config.RemoteAddr, "", c.config.DialTimeOut, c.config.KeepAlive, true, 3, 0, 0, 0)
			if err != nil {
				c.logger.Errorf("channel dialer: %v", err)
				time.Sleep(c.config.RetryInterval)
				continue
			}
			tunnelConn, err := c.config.Layer.Client(rawConn)
			if err != nil {
				c.logger.Errorf("channel dialer: %s handshake: %v", c.config.Layer.Mode(), err)
				time.Sleep(c.config.RetryInterval)
				continue
			}

			err = core.SendBinaryTransportString(tunnelConn, c.config.Token, core.SG_Chan)
			if err != nil {
				c.logger.Errorf("failed to send security token: %v", err)
				tunnelConn.Close()
				continue
			}

			if err := tunnelConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				c.logger.Errorf("failed to set read deadline: %v", err)
				tunnelConn.Close()
				continue
			}
			message, answer, err := core.ReceiveBinaryTransportString(tunnelConn)
			// Backpack: a v2.0.0 Iran server refuses this handshake as too old;
			// the tunnel then runs on that engine until the server is updated.
			if err == nil && answer == core.SG_Refused && message == core.RefusedTooOld && c.config.OnLegacyServer != nil {
				c.logger.Info("the Iran server runs Backpack v2.0.0: using its engine until it is updated")
				tunnelConn.Close()
				c.config.OnLegacyServer()
				return
			}
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					c.logger.Warn("timeout while waiting for control channel response")
				} else {
					c.logger.Errorf("failed to receive control channel response: %v", err)
				}
				tunnelConn.Close()
				time.Sleep(c.config.RetryInterval)
				continue
			}
			tunnelConn.SetReadDeadline(time.Time{})

			if message == c.config.Token {
				g.controlChannel.Set(tunnelConn)
				c.logger.Info("control channel established successfully")

				c.status.Set("Connected (TCPMux)")
				core.Seated(tunnelConn.RemoteAddr().String())

				go c.poolMaintainer(g)
				go c.channelHandler(g)

				return
			} else {
				c.logger.Errorf("invalid token received. Expected: %s, Received: %s. Retrying...", c.config.Token, message)
				tunnelConn.Close()
				time.Sleep(c.config.RetryInterval)
				continue
			}
		}
	}
}

func (c *TcpMuxTransport) poolMaintainer(g *clientGen) {
	maintainPool(g, c.config.ConnPoolSize, c.config.AggressivePool, c.logger, func() { c.tunnelDialer(g) })
}

func (c *TcpMuxTransport) channelHandler(g *clientGen) {
	control := g.controlChannel.Get()
	msgChan := make(chan byte, 1000)

	go func() {
		for {
			select {
			case <-g.ctx.Done():
				return
			default:
				msg, err := core.ReceiveBinaryByte(control)
				if err != nil {
					c.logger.Error("failed to read from control channel. ", err)
					go c.Restart()
					return
				}
				msgChan <- msg
			}
		}
	}()

	for {
		select {
		case <-g.ctx.Done():
			_ = core.SendBinaryByte(control, core.SG_Closed)
			return

		case msg := <-msgChan:
			switch msg {
			case core.SG_Chan:
				atomic.AddInt32(&g.loadConnections, 1)

				select {
				case <-g.controlFlow: // Do nothing

				default:
					c.logger.Debug("channel signal received, initiating tunnel dialer")
					go c.tunnelDialer(g)
				}

			case core.SG_HB:
				c.logger.Debug("heartbeat signal received successfully")

			case core.SG_Closed:
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

func (c *TcpMuxTransport) tunnelDialer(g *clientGen) {
	c.logger.Debugf("initiating new tunnel connection to address %s", c.config.RemoteAddr)

	rawConn, err := core.TcpDialer(g.ctx, c.config.RemoteAddr, "", c.config.DialTimeOut, c.config.KeepAlive, c.config.Nodelay, 3, c.config.SO_RCVBUF, c.config.SO_SNDBUF, c.config.MSS)
	if err != nil {
		c.logger.Errorf("tunnel server dialer: %v", err)
		return
	}
	// Speed edit: see core.MuxSocket.
	core.MuxSocket(rawConn)
	tunnelConn, err := c.config.Layer.Client(rawConn)
	if err != nil {
		c.logger.Errorf("tunnel server dialer: %s handshake: %v", c.config.Layer.Mode(), err)
		return
	}

	atomic.AddInt32(&g.poolConnections, 1)

	c.handleSession(g, tunnelConn)
}

func (c *TcpMuxTransport) handleSession(g *clientGen, tunnelConn net.Conn) {
	defer func() {
		atomic.AddInt32(&g.poolConnections, -1)
	}()

	session, err := smux.Server(tunnelConn, c.smuxConfig)
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
				c.logger.Trace("session is closed: ", err)
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

func (c *TcpMuxTransport) localDialer(g *clientGen, stream *smux.Stream, remoteAddr string) {
	port, resolvedAddr, err := core.ResolveRemoteAddr(remoteAddr)
	if err != nil {
		c.logger.Infof("failed to resolve remote port: %v", err)
		stream.Close()
		return
	}

	var sendBuf, recvBuf int

	if strings.Contains(resolvedAddr, "127.0.0.1") {
		sendBuf = 32 * 1024
		recvBuf = 32 * 1024
	} else {
		sendBuf = c.config.SO_SNDBUF
		recvBuf = c.config.SO_RCVBUF
	}

	localConnection, err := core.TcpDialer(g.ctx, resolvedAddr, "", c.config.DialTimeOut, c.config.KeepAlive, true, 1, recvBuf, sendBuf, c.config.MSS)
	if err != nil {
		c.logger.Errorf("local dialer: %v", err)
		stream.Close()
		return
	}

	c.logger.Debugf("connected to local address %s successfully", remoteAddr)

	core.TCPConnectionHandler(g.ctx, false, core.Counted(stream), localConnection, c.logger, g.usageMonitor, int(port), c.config.Sniffer)
}

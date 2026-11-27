package client

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/plain/core"
	"github.com/backpack/backpack/internal/web"

	"github.com/sirupsen/logrus"
)

// TcpTransport is the plain tcp client: a control channel to the server,
// and a pool of tunnel connections it keeps dialled, each of which carries one
// user connection once the server names its destination.
type TcpTransport struct {
	config       *TcpConfig
	parentctx    context.Context
	logger       *logrus.Logger
	restartMutex sync.Mutex
	gen          atomic.Pointer[clientGen]
	status       core.Status
}

type TcpConfig struct {
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
	AggressivePool bool
	MSS            int
	SO_RCVBUF      int
	SO_SNDBUF      int
	// OnLegacyServer is called when the Iran server answers as Backpack v2.0.0
	// does, so the tunnel can run on that engine.
	OnLegacyServer func()
	// Route is where tunnel connections go and how: fallback_addrs,
	// load_balance, health_failover and the outbound settings. nil dials
	// remote_addr directly. Backpack.
	Route *core.Route
	// Layer is the tunnel's encryption (Backpack); nil is none.
	Layer *core.Layer
}

func NewTCPClient(parentCtx context.Context, config *TcpConfig, logger *logrus.Logger) *TcpTransport {
	client := &TcpTransport{
		config:    config,
		parentctx: parentCtx,
		logger:    logger,
	}
	client.gen.Store(newClientGen(parentCtx, config.WebPort, config.SnifferLog, config.Sniffer, &client.status, logger))
	context.AfterFunc(parentCtx, core.Unseated)
	return client
}

// Running reports whether a control channel is up.
func (c *TcpTransport) Running() bool { return c.status.Running() }

func (c *TcpTransport) Start() { c.start(c.gen.Load()) }

func (c *TcpTransport) start(g *clientGen) {
	if c.config.WebPort > 0 {
		go g.usageMonitor.Monitor()
	}

	c.status.Set("Disconnected (TCP)")

	go c.channelDialer(g)
}

func (c *TcpTransport) Restart() {
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

func (c *TcpTransport) channelDialer(g *clientGen) {
	c.logger.Info("attempting to establish a new control channel connection...")

	for {
		select {
		case <-g.ctx.Done():
			return
		default:
			rawConn, err := c.config.Route.Dial(g.ctx, c.config.Route.Control(c.config.RemoteAddr), c.config.DialTimeOut, c.config.KeepAlive, true, 3, 0, 0, 0)
			if err != nil {
				c.config.Route.ControlFailed(c.logger)
				c.logger.Errorf("channel dialer: %v", err)
				time.Sleep(c.config.RetryInterval)
				continue
			}
			tunnelTCPConn, err := c.config.Layer.Client(rawConn)
			if err != nil {
				c.logger.Errorf("channel dialer: %s handshake: %v", c.config.Layer.Mode(), err)
				time.Sleep(c.config.RetryInterval)
				continue
			}

			err = core.SendBinaryTransportString(tunnelTCPConn, c.config.Token, core.SG_Chan)
			if err != nil {
				c.logger.Errorf("failed to send security token: %v", err)
				tunnelTCPConn.Close()
				continue
			}

			if err := tunnelTCPConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				c.logger.Errorf("failed to set read deadline: %v", err)
				tunnelTCPConn.Close()
				continue
			}

			message, answer, err := core.ReceiveBinaryTransportString(tunnelTCPConn)
			// Backpack: a v2.0.0 Iran server refuses this handshake as too old;
			// the tunnel then runs on that engine until the server is updated.
			if err == nil && answer == core.SG_Refused && message == core.RefusedTooOld && c.config.OnLegacyServer != nil {
				c.logger.Info("the Iran server runs Backpack v2.0.0: using its engine until it is updated")
				tunnelTCPConn.Close()
				c.config.OnLegacyServer()
				return
			}
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					c.logger.Warn("timeout while waiting for control channel response")
				} else {
					c.logger.Errorf("failed to receive control channel response: %v", err)
				}
				tunnelTCPConn.Close()
				time.Sleep(c.config.RetryInterval)
				continue
			}
			tunnelTCPConn.SetReadDeadline(time.Time{})

			if message == c.config.Token {
				g.controlChannel.Set(tunnelTCPConn)
				c.logger.Info("control channel established successfully")

				c.status.Set("Connected (TCP)")
				core.Seated(tunnelTCPConn.RemoteAddr().String())
				go c.poolMaintainer(g)
				go c.channelHandler(g)

				return

			} else {
				c.logger.Errorf("invalid token received. Expected: %s, Received: %s. Retrying...", c.config.Token, message)
				tunnelTCPConn.Close()
				time.Sleep(c.config.RetryInterval)
				continue
			}
		}
	}
}

func (c *TcpTransport) poolMaintainer(g *clientGen) {
	maintainPool(g, c.config.ConnPoolSize, c.config.AggressivePool, c.logger, func() { c.tunnelDialer(g) })
}

func (c *TcpTransport) channelHandler(g *clientGen) {
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

			case core.SG_RTT:
				err := core.SendBinaryByte(control, core.SG_RTT)
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

func (c *TcpTransport) tunnelDialer(g *clientGen) {
	c.logger.Debugf("initiating new connection to tunnel server at %s", c.config.RemoteAddr)

	rawConn, err := c.config.Route.Dial(g.ctx, c.config.Route.Data(c.config.RemoteAddr), c.config.DialTimeOut, c.config.KeepAlive, c.config.Nodelay, 3, c.config.SO_RCVBUF, c.config.SO_SNDBUF, c.config.MSS)
	if err != nil {
		c.logger.Error("tunnel server dialer: ", err)
		return
	}
	tcpConn, err := c.config.Layer.Client(rawConn)
	if err != nil {
		c.logger.Errorf("tunnel server dialer: %s handshake: %v", c.config.Layer.Mode(), err)
		return
	}

	// Increment active connections counter
	atomic.AddInt32(&g.poolConnections, 1)

	// Attempt to receive the remote address from the tunnel server
	remoteAddr, transport, err := core.ReceiveBinaryTransportString(tcpConn)

	// Decrement active connections after successful or failed connection
	atomic.AddInt32(&g.poolConnections, -1)

	if err != nil {
		c.logger.Debugf("failed to receive port from tunnel connection %s: %v", tcpConn.RemoteAddr().String(), err)
		tcpConn.Close()
		return
	}

	port, resolvedAddr, err := core.ResolveRemoteAddr(remoteAddr)
	if err != nil {
		c.logger.Infof("failed to resolve remote port: %v", err)
		tcpConn.Close()
		return
	}

	switch transport {
	case core.SG_TCP:
		c.localDialer(g, tcpConn, resolvedAddr, port)

	case core.SG_UDP:
		UDPDialer(core.Counted(tcpConn), core.PickBackend(resolvedAddr), c.logger, g.usageMonitor, port, c.config.Sniffer)

	default:
		c.logger.Error("undefined transport. close the connection.")
		tcpConn.Close()
	}
}

func (c *TcpTransport) localDialer(g *clientGen, tcpConn net.Conn, resolvedAddr string, port int) {
	var sendBuf, recvBuf int

	if strings.Contains(resolvedAddr, "127.0.0.1") {
		// Use 32 KB for localhost
		sendBuf = 32 * 1024
		recvBuf = 32 * 1024
	} else {
		sendBuf = c.config.SO_SNDBUF
		recvBuf = c.config.SO_RCVBUF
	}

	// Backpack: a backend list ("a|b") is tried in turn; see core.DialBackend.
	localConnection, err := core.DialBackend(resolvedAddr, func(addr string) (*net.TCPConn, error) {
		return core.TcpDialer(g.ctx, addr, "", c.config.DialTimeOut, c.config.KeepAlive, true, 1, recvBuf, sendBuf, c.config.MSS)
	})
	if err != nil {
		c.logger.Errorf("local dialer: %v", err)
		tcpConn.Close()
		return
	}

	c.logger.Debugf("connected to local address %s successfully", resolvedAddr)

	core.TCPConnectionHandler(g.ctx, false, core.Counted(tcpConn), localConnection, c.logger, g.usageMonitor, port, c.config.Sniffer)
}

// clientGen is one run of a client transport: what a Restart
// re-initialises.
type clientGen struct {
	ctx             context.Context
	cancel          context.CancelFunc
	controlChannel  netControl
	wsControl       wsControl
	usageMonitor    *web.Usage
	poolConnections int32
	loadConnections int32
	controlFlow     chan struct{}
}

func newClientGen(parent context.Context, webPort int, snifferLog string, sniffer bool, status *core.Status, logger *logrus.Logger) *clientGen {
	ctx, cancel := context.WithCancel(parent)
	return &clientGen{
		ctx:          ctx,
		cancel:       cancel,
		usageMonitor: web.NewDataStore(fmt.Sprintf(":%v", webPort), ctx, snifferLog, sniffer, status.Get, logger),
		controlFlow:  make(chan struct{}, 100),
	}
}

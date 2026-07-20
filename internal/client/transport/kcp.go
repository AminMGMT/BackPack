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
	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
)

// KcpTransport is the client side of the KCP transport. It dials out to the
// server over UDP and carries SMUX streams inside a reliable KCP session, so
// the tunnel survives paths where a long-lived TCP connection would stall.
type KcpTransport struct {
	config          *KcpConfig
	smuxConfig      *smux.Config
	kcpSettings     network.KCPSettings
	parentctx       context.Context
	state           clientState
	logger          *logrus.Logger
	restartMutex    sync.Mutex
	poolConnections int32
	loadConnections int32
	controlFlow     chan struct{}
}

type KcpConfig struct {
	RemoteAddr string
	// Endpoints rotates through the server addresses (primary + fallbacks)
	// so a filtered IP or blocked port does not stop the tunnel.
	Endpoints        *network.Endpoints
	Token            string
	SnifferLog       string
	TunnelStatus     string
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
	SO_RCVBUF        int
	SO_SNDBUF        int

	// KCP tuning, filled from the tunnel's performance preset. These must match
	// the server's values — the FEC layer in particular is not negotiated.
	MTU          int
	Interval     int
	Resend       int
	NoDelay      int
	NoCongestion int
	SndWnd       int
	RcvWnd       int
	AckNoDelay   bool
	DataShards   int
	ParityShards int
}

func (c *KcpConfig) settings() network.KCPSettings {
	return network.KCPSettings{
		MTU:          c.MTU,
		Interval:     c.Interval,
		Resend:       c.Resend,
		NoDelay:      c.NoDelay,
		NoCongestion: c.NoCongestion,
		SndWnd:       c.SndWnd,
		RcvWnd:       c.RcvWnd,
		AckNoDelay:   c.AckNoDelay,
		DataShards:   c.DataShards,
		ParityShards: c.ParityShards,
		SO_RCVBUF:    c.SO_RCVBUF,
		SO_SNDBUF:    c.SO_SNDBUF,
	}
}

func NewKcpClient(parentCtx context.Context, config *KcpConfig, logger *logrus.Logger) *KcpTransport {
	ctx, cancel := context.WithCancel(parentCtx)

	client := &KcpTransport{
		smuxConfig: &smux.Config{
			Version:           config.MuxVersion,
			KeepAliveInterval: 20 * time.Second,
			KeepAliveTimeout:  40 * time.Second,
			MaxFrameSize:      config.MaxFrameSize,
			MaxReceiveBuffer:  config.MaxReceiveBuffer,
			MaxStreamBuffer:   config.MaxStreamBuffer,
		},
		config:          config,
		kcpSettings:     config.settings(),
		parentctx:       parentCtx,
		logger:          logger,
		poolConnections: 0,
		loadConnections: 0,
		controlFlow:     make(chan struct{}, 100),
	}
	// Seed the first generation through the same path a restart uses, so
	// there is only one way this state is ever published.
	client.state.Reset(ctx, cancel, web.NewDataStore(fmt.Sprintf(":%v", config.WebPort), ctx, config.SnifferLog, config.Sniffer, &config.TunnelStatus, logger))
	return client
}

func (c *KcpTransport) Start() {
	if c.config.WebPort > 0 {
		go c.state.Usage().Monitor()
	}

	c.config.TunnelStatus = "Disconnected (KCP)"

	go c.channelDialer()
}

func (c *KcpTransport) Restart() {
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

	c.state.CloseConn()

	time.Sleep(2 * time.Second)

	ctx, cancel := context.WithCancel(c.parentctx)

	// Publish the whole new generation at once: a reader must never see
	// the new context paired with the old monitor, or vice versa.
	c.state.Reset(ctx, cancel, web.NewDataStore(fmt.Sprintf(":%v", c.config.WebPort), ctx, c.config.SnifferLog, c.config.Sniffer, &c.config.TunnelStatus, c.logger))
	c.config.TunnelStatus = ""
	atomic.StoreInt32(&c.poolConnections, 0)
	atomic.StoreInt32(&c.loadConnections, 0)
	drain(c.controlFlow)

	c.logger.SetLevel(level)

	go c.Start()
}

// dial opens one KCP session.
//
// The control channel must stay pinned to one endpoint — it is the connection
// the server identifies this peer by — so it passes the current endpoint. Pool
// connections take the next one in the rotation, which spreads them over every
// configured endpoint when load balancing is on.
func (c *KcpTransport) dial(addr string) (*kcp.UDPSession, error) {
	session, err := network.KCPDial(addr, c.config.Token, c.kcpSettings)
	if err != nil {
		return nil, err
	}
	return session, nil
}

func (c *KcpTransport) channelDialer() {
	c.logger.Info("attempting to establish a new kcp control channel connection...")

	for {
		select {
		case <-c.state.Ctx().Done():
			return
		default:
			tunnelConn, err := c.dial(c.config.Endpoints.Current())
			if err != nil {
				c.logger.Errorf("channel dialer: %v", err)
				// The current endpoint did not answer — move to the next one so a
				// filtered IP or blocked port cannot stall the tunnel forever.
				if next := c.config.Endpoints.Rotate(); c.config.Endpoints.Len() > 1 {
					c.logger.Infof("trying next server endpoint: %s", next)
				}
				time.Sleep(c.config.RetryInterval)
				continue
			}

			// The control channel carries small, latency-critical signals.
			tunnelConn.SetACKNoDelay(true)

			// Sending security token
			if err := utils.SendBinaryTransportString(tunnelConn, c.config.Token, utils.SG_Chan); err != nil {
				c.logger.Errorf("failed to send security token: %v", err)
				tunnelConn.Close()
				continue
			}

			// Set a read deadline for the token response
			if err := tunnelConn.SetReadDeadline(time.Now().Add(c.config.DialTimeOut)); err != nil {
				c.logger.Errorf("failed to set read deadline: %v", err)
				tunnelConn.Close()
				continue
			}

			message, _, err := utils.ReceiveBinaryTransportString(tunnelConn)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					c.logger.Warn("timeout while waiting for control channel response")
				} else {
					c.logger.Errorf("failed to receive control channel response: %v", err)
				}
				tunnelConn.Close()
				// A silent server is exactly what a filtered address looks like
				// over UDP, so rotate before retrying.
				if next := c.config.Endpoints.Rotate(); c.config.Endpoints.Len() > 1 {
					c.logger.Infof("trying next server endpoint: %s", next)
				}
				time.Sleep(c.config.RetryInterval)
				continue
			}

			// Resetting the deadline (removes any existing deadline)
			tunnelConn.SetReadDeadline(time.Time{})

			if message != c.config.Token {
				c.logger.Errorf("invalid token received (does not match the server's token). Retrying...")
				tunnelConn.Close()
				time.Sleep(c.config.RetryInterval)
				continue
			}

			c.state.SetConn(tunnelConn)
			c.logger.Info("control channel established successfully")

			c.config.TunnelStatus = "Connected (KCP)"

			go c.poolMaintainer()
			go c.channelHandler()

			return
		}
	}
}

func (c *KcpTransport) poolMaintainer() {
	for i := 0; i < c.config.ConnPoolSize; i++ { // initial pool filling
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

	newPoolSize := c.config.ConnPoolSize // initial value
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

			// Dynamically adjust the pool size based on current connections
			if (loadConnections + a) > poolConnectionsAvg*b {
				c.logger.Debugf("increasing pool size: %d -> %d, avg pool conn: %d, avg load conn: %d", newPoolSize, newPoolSize+1, poolConnectionsAvg, loadConnections)
				newPoolSize++

				go c.tunnelDialer()
			} else if float64(loadConnections+x) < float64(poolConnectionsAvg)*y && newPoolSize > c.config.ConnPoolSize {
				c.logger.Debugf("decreasing pool size: %d -> %d, avg pool conn: %d, avg load conn: %d", newPoolSize, newPoolSize-1, poolConnectionsAvg, loadConnections)
				newPoolSize--

				c.controlFlow <- struct{}{}
			}
		}
	}
}

// controlDeadline is how long the client waits for any word from the server
// before deciding the tunnel is dead. It has to comfortably exceed the server's
// heartbeat interval, or a healthy but quiet tunnel would be torn down.
func (c *KcpTransport) controlDeadline() time.Duration {
	if c.config.KeepAlive > 0 {
		return c.config.KeepAlive
	}
	return 90 * time.Second
}

func (c *KcpTransport) channelHandler() {
	msgChan := make(chan byte, 1000)

	// Goroutine to handle the blocking ReceiveBinaryByte
	go func() {
		for {
			select {
			case <-c.state.Ctx().Done():
				return
			default:
				// KCP gives no signal when the peer disappears: unlike TCP there
				// is no connection for the operating system to tear down, so a
				// read on a dead tunnel would block forever and the client would
				// never reconnect. The server heartbeats regularly, so silence
				// for longer than the keepalive period means the peer is gone.
				if err := c.state.Conn().SetReadDeadline(time.Now().Add(c.controlDeadline())); err != nil {
					c.logger.Errorf("failed to set control channel deadline: %v", err)
					go c.Restart()
					return
				}
				msg, err := utils.ReceiveBinaryByte(c.state.Conn())
				if err != nil {
					if c.state.Cancel() != nil {
						if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
							c.logger.Warn("no heartbeat from the server within the keepalive period, reconnecting")
						} else {
							c.logger.Error("failed to read from control channel. ", err)
						}
						go c.Restart()
					}
					return
				}
				msgChan <- msg
			}
		}
	}()

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

func (c *KcpTransport) tunnelDialer() {
	addr := c.config.Endpoints.Next()
	c.logger.Debugf("initiating new tunnel connection to address %s", addr)

	tunnelConn, err := c.dial(addr)
	if err != nil {
		c.logger.Errorf("tunnel server dialer: %v", err)
		return
	}

	// KCP has no connection handshake of its own: the server's listener only
	// materialises a session once it receives a packet from this socket. So
	// every pool connection announces itself with the token, which both wakes
	// the listener and authenticates the session before any data flows.
	if err := utils.SendBinaryTransportString(tunnelConn, c.config.Token, utils.SG_TCP); err != nil {
		c.logger.Errorf("failed to announce tunnel connection: %v", err)
		tunnelConn.Close()
		return
	}

	atomic.AddInt32(&c.poolConnections, 1)

	c.handleSession(tunnelConn)
}

func (c *KcpTransport) handleSession(tunnelConn net.Conn) {
	defer func() {
		atomic.AddInt32(&c.poolConnections, -1)
	}()

	// SMUX server
	session, err := smux.Server(tunnelConn, c.smuxConfig)
	if err != nil {
		c.logger.Errorf("failed to create mux session: %v", err)
		tunnelConn.Close()
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

func (c *KcpTransport) localDialer(stream *smux.Stream, remoteAddr string) {
	port, resolvedAddr, err := network.ResolveRemoteAddr(remoteAddr)
	if err != nil {
		c.logger.Infof("failed to resolve remote port: %v", err)
		stream.Close()
		return
	}

	var sendBuf, recvBuf int

	if strings.Contains(resolvedAddr, "127.0.0.1") {
		// Use 32 KB for localhost
		sendBuf = 32 * 1024
		recvBuf = 32 * 1024
	} else {
		sendBuf = c.config.SO_SNDBUF
		recvBuf = c.config.SO_RCVBUF
	}

	localConnection, err := network.TcpDialer(c.state.Ctx(), resolvedAddr, "", c.config.DialTimeOut, c.config.KeepAlive, true, 1, recvBuf, sendBuf, 0)
	if err != nil {
		localDial.Report(c.logger, resolvedAddr, err)
		stream.Close()
		return
	}

	c.logger.Debugf("connected to local address %s successfully", remoteAddr)

	handlers.TCPConnectionHandler(c.state.Ctx(), false, metrics.CountedConn(stream), localConnection, c.logger, c.state.Usage(), int(port), c.config.Sniffer)
}

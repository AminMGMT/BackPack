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

	"github.com/quic-go/quic-go"
	"github.com/sirupsen/logrus"
)

// QuicTransport is the client side of the QUIC transport. It dials one QUIC
// connection out to the server and carries everything over its streams: a
// control stream for the signalling, and a stream per forwarded flow. QUIC
// supplies the TLS 1.3, the multiplexing, the congestion control and the loss
// recovery, so there is no smux and no KCP-style tuning to keep in step.
type QuicTransport struct {
	config          *QuicConfig
	quicSettings    network.QUICSettings
	parentctx       context.Context
	state           clientState
	logger          *logrus.Logger
	restartMutex    sync.Mutex
	poolConnections int32
	loadConnections int32
	controlFlow     chan struct{}

	// connMu guards quicConn, the connection this run opens its streams on. It is
	// replaced on every restart, so a data stream is always opened on the current
	// connection rather than a torn-down one.
	connMu        sync.Mutex
	quicConn      *quic.Conn
	forwardActive int32
}

type QuicConfig struct {
	RemoteAddr string
	// Endpoints rotates through the server addresses (primary + fallbacks) so a
	// filtered IP or blocked port does not stop the tunnel.
	Endpoints      *network.Endpoints
	Token          string
	SnifferLog     string
	TunnelStatus   string
	Sniffer        bool
	KeepAlive      time.Duration
	RetryInterval  time.Duration
	DialTimeOut    time.Duration
	ConnPoolSize   int
	WebPort        int
	AggressivePool bool
	SO_RCVBUF      int
	SO_SNDBUF      int
	Forward        bool
	Ports          []string
	ProxyProtocol  bool
	MaxConnections int
	BandwidthMbps  int
}

func (c *QuicConfig) settings() network.QUICSettings {
	return network.QUICSettings{
		KeepAlivePeriod: c.KeepAlive,
		MaxIdleTimeout:  quicIdleTimeout(c.KeepAlive),
		SO_RCVBUF:       c.SO_RCVBUF,
		SO_SNDBUF:       c.SO_SNDBUF,
	}
}

// quicIdleTimeout mirrors the server's: silence for longer than this means the
// peer is gone. It has to comfortably exceed the keepalive.
func quicIdleTimeout(keepAlive time.Duration) time.Duration {
	if keepAlive <= 0 {
		return 30 * time.Second
	}
	return 3 * keepAlive
}

func NewQuicClient(parentCtx context.Context, config *QuicConfig, logger *logrus.Logger) *QuicTransport {
	ctx, cancel := context.WithCancel(parentCtx)

	client := &QuicTransport{
		config:       config,
		quicSettings: config.settings(),
		parentctx:    parentCtx,
		logger:       logger,
		controlFlow:  make(chan struct{}, 100),
	}
	// Seed the first generation through the same path a restart uses, so there is
	// only one way this state is ever published.
	client.state.Reset(ctx, cancel, web.NewDataStore(fmt.Sprintf(":%v", config.WebPort), ctx, config.SnifferLog, config.Sniffer, &config.TunnelStatus, logger))
	return client
}

func (c *QuicTransport) setQUICConn(conn *quic.Conn) {
	c.connMu.Lock()
	c.quicConn = conn
	c.connMu.Unlock()
}

func (c *QuicTransport) getQUICConn() *quic.Conn {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.quicConn
}

func (c *QuicTransport) Start() {
	if c.config.WebPort > 0 {
		go c.state.Usage().Monitor()
	}

	c.config.TunnelStatus = "Disconnected (QUIC)"

	go c.channelDialer()
}

func (c *QuicTransport) Restart() {
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
	// Closing the QUIC connection tears down every stream it carries, including
	// the pool, and releases the UDP socket underneath.
	if qc := c.getQUICConn(); qc != nil {
		_ = qc.CloseWithError(0, "restart")
		c.setQUICConn(nil)
	}

	time.Sleep(2 * time.Second)

	// The whole tunnel may have been shut down while this restart was waiting.
	// Rebuilding from a finished parent context would bind and close for nothing.
	if c.parentctx.Err() != nil {
		c.logger.SetLevel(level)
		c.logger.Debug("restart abandoned: the tunnel is shutting down")
		return
	}

	ctx, cancel := context.WithCancel(c.parentctx)

	c.state.Reset(ctx, cancel, web.NewDataStore(fmt.Sprintf(":%v", c.config.WebPort), ctx, c.config.SnifferLog, c.config.Sniffer, &c.config.TunnelStatus, c.logger))
	c.config.TunnelStatus = ""
	atomic.StoreInt32(&c.poolConnections, 0)
	atomic.StoreInt32(&c.loadConnections, 0)
	atomic.StoreInt32(&c.forwardActive, 0)
	metrics.ClearPool()
	drain(c.controlFlow)

	c.logger.SetLevel(level)

	go c.Start()
}

func (c *QuicTransport) channelDialer() {
	c.logger.Info("attempting to establish a new quic control channel connection...")

	// One backoff for this reconnect loop (see backoff.go): fixed-interval
	// retries become exponential, so a sustained outage is probed a few times a
	// minute rather than every second.
	bo := newBackoff(c.config.RetryInterval)

	for {
		select {
		case <-c.state.Ctx().Done():
			return
		default:
			conn, err := network.QUICDial(c.state.Ctx(), c.config.Endpoints.Current(), c.quicSettings)
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

			stream, err := conn.OpenStreamSync(c.state.Ctx())
			if err != nil {
				c.logger.Errorf("failed to open control stream: %v", err)
				_ = conn.CloseWithError(0, "control open failed")
				bo.Wait(c.state.Ctx())
				continue
			}
			control := network.NewQUICStreamConn(stream, conn)

			// Sending security token
			if err := utils.SendBinaryTransportString(control, c.config.Token, utils.SG_Chan); err != nil {
				c.logger.Errorf("failed to send security token: %v", err)
				_ = conn.CloseWithError(0, "token send failed")
				bo.Wait(c.state.Ctx())
				continue
			}

			if err := control.SetReadDeadline(time.Now().Add(c.config.DialTimeOut)); err != nil {
				c.logger.Errorf("failed to set read deadline: %v", err)
				_ = conn.CloseWithError(0, "deadline failed")
				bo.Wait(c.state.Ctx())
				continue
			}

			message, _, err := utils.ReceiveBinaryTransportString(control)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					c.logger.Warn("timeout while waiting for control channel response")
				} else {
					c.logger.Errorf("failed to receive control channel response: %v", err)
				}
				_ = conn.CloseWithError(0, "no response")
				// A silent server is exactly what a filtered address looks like, so
				// rotate before retrying.
				if next := c.config.Endpoints.Rotate(); c.config.Endpoints.Len() > 1 {
					c.logger.Infof("trying next server endpoint: %s", next)
				}
				bo.Wait(c.state.Ctx())
				continue
			}

			control.SetReadDeadline(time.Time{})

			if message != c.config.Token {
				c.logger.Errorf("invalid token received (does not match the server's token). Retrying...")
				_ = conn.CloseWithError(0, "bad token")
				bo.Wait(c.state.Ctx())
				continue
			}

			c.setQUICConn(conn)
			c.state.SetConn(control)
			c.logger.Info("control channel established successfully")

			c.config.TunnelStatus = "Connected (QUIC)"

			go c.channelHandler()
			if c.config.Forward {
				go startForwardIngress(c.state.Ctx(), c.config.Ports, c.config.MaxConnections, c.config.BandwidthMbps, c.config.ProxyProtocol, c.logger, c.state.Usage(), c.config.Sniffer, &c.forwardActive, c.openForwardQUICStream, func() { go c.Restart() })
			} else {
				go c.poolMaintainer()
			}

			return
		}
	}
}

func (c *QuicTransport) openForwardQUICStream(target string) (net.Conn, error) {
	qc := c.getQUICConn()
	if qc == nil {
		return nil, fmt.Errorf("forward QUIC connection is not ready")
	}
	stream, err := qc.OpenStreamSync(c.state.Ctx())
	if err != nil {
		return nil, err
	}
	data := network.NewQUICStreamConn(stream, qc)
	fail := func(err error) (net.Conn, error) { data.Close(); return nil, err }
	if err := utils.SendBinaryTransportString(data, c.config.Token, utils.SG_ForwardTCP); err != nil {
		return fail(err)
	}
	if err := utils.SendBinaryString(data, target); err != nil {
		return fail(err)
	}
	_ = data.SetReadDeadline(time.Now().Add(c.config.DialTimeOut))
	status, err := utils.ReceiveBinaryByte(data)
	_ = data.SetReadDeadline(time.Time{})
	if err != nil {
		return fail(err)
	}
	if status != utils.SG_ForwardOK {
		return fail(fmt.Errorf("Kharej backend %q is unavailable or invalid", target))
	}
	return data, nil
}

func (c *QuicTransport) poolMaintainer() {
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
	var load poolLoad                    // throughput signal, see poolload.go
	var poolConnectionsSum int32 = 0

	for {
		select {
		case <-c.state.Ctx().Done():
			return

		case <-tickerPool.C:
			atomic.AddInt32(&poolConnectionsSum, atomic.LoadInt32(&c.poolConnections))

		case <-tickerLoad.C:
			loadConnections := (int(atomic.LoadInt32(&c.loadConnections)) + 9) / 10
			atomic.StoreInt32(&c.loadConnections, 0)

			poolConnectionsAvg := (int(atomic.LoadInt32(&poolConnectionsSum)) + 9) / 10
			atomic.StoreInt32(&poolConnectionsSum, 0)

			mbps := load.mbps()

			metrics.ReportPool(poolConnectionsAvg, newPoolSize, c.config.ConnPoolSize, mbps)

			if ((loadConnections+a) > poolConnectionsAvg*b && poolCanGrow(newPoolSize, c.config.ConnPoolSize)) ||
				load.wantsMore(mbps, poolConnectionsAvg, newPoolSize, c.config.ConnPoolSize) {
				c.logger.Debugf("increasing pool size: %d -> %d, avg pool conn: %d, avg load conn: %d, throughput: %d Mbit/s", newPoolSize, newPoolSize+1, poolConnectionsAvg, loadConnections, mbps)
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
func (c *QuicTransport) controlDeadline() time.Duration {
	if c.config.KeepAlive > 0 {
		return c.config.KeepAlive
	}
	return 90 * time.Second
}

func (c *QuicTransport) channelHandler() {
	msgChan := make(chan byte, 1000)

	go func() {
		for {
			select {
			case <-c.state.Ctx().Done():
				return
			default:
				// The server heartbeats regularly, so silence for longer than the
				// keepalive period means the peer is gone. QUIC's own idle timeout
				// would eventually notice too, but this reconnects promptly.
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

// tunnelDialer opens one data stream, announces it, and then waits for the
// server to name the backend it should carry.
func (c *QuicTransport) tunnelDialer() {
	qc := c.getQUICConn()
	if qc == nil {
		return
	}

	stream, err := qc.OpenStreamSync(c.state.Ctx())
	if err != nil {
		c.logger.Errorf("failed to open tunnel stream: %v", err)
		return
	}
	data := network.NewQUICStreamConn(stream, qc)

	// Announce the stream with the token so the server can authenticate it and
	// file it as a data stream.
	if err := utils.SendBinaryTransportString(data, c.config.Token, utils.SG_TCP); err != nil {
		c.logger.Errorf("failed to announce tunnel stream: %v", err)
		stream.Close()
		return
	}

	atomic.AddInt32(&c.poolConnections, 1)

	// Wait until the server assigns this stream a backend to reach.
	remoteAddr, err := utils.ReceiveBinaryString(data)
	atomic.AddInt32(&c.poolConnections, -1)
	if err != nil {
		c.logger.Tracef("tunnel stream closed before use: %v", err)
		stream.Close()
		return
	}

	c.localDialer(data, remoteAddr)
}

func (c *QuicTransport) localDialer(stream net.Conn, remoteAddr string) {
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
		sendBuf = 32 * 1024
		recvBuf = 32 * 1024
	} else {
		sendBuf = c.config.SO_SNDBUF
		recvBuf = c.config.SO_RCVBUF
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

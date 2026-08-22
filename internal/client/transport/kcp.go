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
	// The status shown in the panel. Behind a lock because the run being
	// replaced and the run replacing it both write it. See tunnelStatus.
	status          tunnelStatus
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
	// UseICMP carries the session inside ICMP echo (the xdi transport) rather
	// than UDP. Only the packet layer differs; everything here is unchanged.
	UseICMP bool
	// UseSpoof carries the session inside raw IPv4 packets with a forged source
	// (the spoof transport). Only the packet layer differs. See spoofSettings.
	UseSpoof       bool
	SpoofProfile   string
	SpoofUplink    string
	SpoofDownlink  string
	SpoofSrcIP     string
	SpoofSrcPool   []string
	SpoofPeerIP    string
	SpoofInterface string
	SpoofXDPIface  string
	SpoofSockBuf   int
	SpoofPeerSrcIP string
	SpoofICMPReply bool
	SpoofMTU       int
	SpoofDPI       network.SpoofDPI
	// UsePck carries the session inside TCP segments built and read through a
	// packet socket (the pck transport). Only the packet layer differs; nothing
	// is forged. See settings().
	UsePck        bool
	PckInterface  string
	PckGatewayMAC string
	PckFlags      []string
}

// transportLabel is what the panel and logs call this transport — XDI over ICMP
// echo, SPOOF over forged raw IP, KCP over UDP.
func (c *KcpTransport) transportLabel() string {
	if c.config.UseICMP {
		return "XDI"
	}
	if c.config.UseSpoof {
		return "SPOOF"
	}
	if c.config.UsePck {
		return "PCK"
	}
	return "KCP"
}

func (c *KcpConfig) settings() network.KCPSettings {
	s := network.KCPSettings{
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
		UseICMP:      c.UseICMP,
	}
	if c.UsePck {
		// The flag cycle is validated at load time (checkPck); an unparseable
		// one here falls back to the default rather than killing the tunnel.
		flags, err := network.ParseTCPFlagList(c.PckFlags)
		if err != nil {
			flags, _ = network.ParseTCPFlagList(nil)
		}
		s.Pck = &network.PcapCarrier{
			Interface:  c.PckInterface,
			GatewayMAC: c.PckGatewayMAC,
			Flags:      flags,
		}
	}
	if c.UseSpoof {
		// Profile is validated at load time (checkSpoof); default to udp here.
		up, down := network.ResolveSpoofDirections(c.SpoofProfile, c.SpoofUplink, c.SpoofDownlink)
		s.Spoof = &network.SpoofCarrier{
			Uplink:     up,
			Downlink:   down,
			SrcIP:      c.SpoofSrcIP,
			SrcPool:    c.SpoofSrcPool,
			PeerIP:     c.SpoofPeerIP,
			Interface:  c.SpoofInterface,
			XDPIface:   c.SpoofXDPIface,
			SockBuf:    c.SpoofSockBuf,
			PeerSrcIP:  c.SpoofPeerSrcIP,
			ReplySplit: c.SpoofICMPReply,
			MTU:        c.SpoofMTU,
			DPI:        c.SpoofDPI,
		}
	}
	return s
}

func NewKcpClient(parentCtx context.Context, config *KcpConfig, logger *logrus.Logger) *KcpTransport {
	ctx, cancel := context.WithCancel(parentCtx)

	client := &KcpTransport{
		smuxConfig: &smux.Config{
			Version:           network.ResolveStaticMuxVersion(config.MuxVersion),
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
	// Surface the carrier's startup diagnostics (effective FEC/MTU, and for pck
	// the discovered egress and RST-guard status) in the tunnel log, so a client
	// that never connects reports why instead of staying silent.
	client.kcpSettings.Logf = logger.Infof
	// Seed the first generation through the same path a restart uses, so
	// there is only one way this state is ever published.
	client.state.Reset(ctx, cancel, web.NewDataStore(fmt.Sprintf(":%v", config.WebPort), ctx, config.SnifferLog, config.Sniffer, client.status.get, logger))
	return client
}

func (c *KcpTransport) Start() {
	if c.config.WebPort > 0 {
		go c.state.Usage().Monitor()
	}

	c.status.set("Disconnected (" + c.transportLabel() + ")")

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
	level := c.logger.GetLevel()
	c.logger.SetLevel(logrus.FatalLevel)

	if c.state.Cancel() != nil {
		c.state.Cancel()()
	}

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
	// Likewise the peer: this generation's control channel is gone, and a
	// stale address would keep the card green across a reconnect that has not
	// happened yet.
	metrics.ClearPeer()
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

	// One backoff for this reconnect loop (see backoff.go): fixed-interval
	// retries become exponential, so a sustained outage is probed a few times a
	// minute rather than every second.
	bo := newBackoff(c.config.RetryInterval)

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
				bo.Wait(c.state.Ctx())
				continue
			}

			// The control channel carries small, latency-critical signals.
			tunnelConn.SetACKNoDelay(true)

			// Sending security token
			if err := utils.SendBinaryTransportString(tunnelConn, c.config.Token, utils.SG_Chan); err != nil {
				c.logger.Errorf("failed to send security token: %v", err)
				tunnelConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}

			// Set a read deadline for the token response
			if err := tunnelConn.SetReadDeadline(time.Now().Add(c.config.DialTimeOut)); err != nil {
				c.logger.Errorf("failed to set read deadline: %v", err)
				tunnelConn.Close()
				bo.Wait(c.state.Ctx())
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
				bo.Wait(c.state.Ctx())
				continue
			}

			// Resetting the deadline (removes any existing deadline)
			tunnelConn.SetReadDeadline(time.Time{})

			if message != c.config.Token {
				c.logger.Errorf("invalid token received (does not match the server's token). Retrying...")
				tunnelConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}

			c.state.SetConn(tunnelConn)
			c.logger.Info("control channel established successfully")

			// The dialling side has to record its peer for the same reason the
			// listening side does, and it was the only one not doing it.
			//
			// The panel asks the socket table whether a tunnel is up. That
			// works for the carriers that leave a socket behind — plain kcp,
			// udp and quic dial a connected UDP socket the kernel will name a
			// peer for — and answers "no" for the ones whose whole purpose is
			// to leave nothing there: xdi rides in ICMP, pck builds its own TCP
			// segments through a packet socket, spoof sends from a raw socket.
			// Those tunnels carried traffic perfectly while this side's card
			// showed offline and the other end's showed online, because only
			// the far end wrote down what it knew.
			metrics.ReportPeer(tunnelConn.RemoteAddr().String())

			c.status.set("Connected (" + c.transportLabel() + ")")

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

// dialUDP forwards a target marked as UDP, reporting whether it took the flow.
func (c *KcpTransport) dialUDP(stream net.Conn, remoteAddr string) bool {
	return dialForwardedUDP(stream, remoteAddr, c.logger, c.state.Usage(), c.config.Sniffer)
}

func (c *KcpTransport) localDialer(stream *smux.Stream, remoteAddr string) {
	if c.dialUDP(stream, remoteAddr) {
		return
	}
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

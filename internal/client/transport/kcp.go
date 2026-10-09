package transport

import (
	"context"
	"net"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"

	"github.com/sirupsen/logrus"
	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
)

// KcpTransport is the client side of the KCP transport. It dials out to the
// server over UDP and carries SMUX streams inside a reliable KCP session, so
// the tunnel survives paths where a long-lived TCP connection would stall.
type KcpTransport struct {
	// The generations of this transport and what outlives them: see
	// lifecycle.go.
	lifecycle

	config      *KcpConfig
	smuxConfig  *smux.Config
	kcpSettings network.KCPSettings
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
	return s
}

func NewKcpClient(parentCtx context.Context, config *KcpConfig, logger *logrus.Logger) *KcpTransport {
	client := &KcpTransport{
		smuxConfig: &smux.Config{
			Version:           network.ResolveStaticMuxVersion(config.MuxVersion),
			KeepAliveInterval: 20 * time.Second,
			KeepAliveTimeout:  40 * time.Second,
			MaxFrameSize:      config.MaxFrameSize,
			MaxReceiveBuffer:  config.MaxReceiveBuffer,
			MaxStreamBuffer:   config.MaxStreamBuffer,
		},
		config:      config,
		kcpSettings: config.settings(),
	}
	// Surface the carrier's startup diagnostics (effective FEC/MTU, and for pck
	// the discovered egress and RST-guard status) in the tunnel log, so a client
	// that never connects reports why instead of staying silent.
	client.kcpSettings.Logf = logger.Infof
	client.firstGeneration(parentCtx, logger, usageSpec{webPort: config.WebPort, snifferLog: config.SnifferLog, sniffer: config.Sniffer})
	return client
}

func (c *KcpTransport) Start() {
	if c.config.WebPort > 0 {
		c.state.Go(c.state.Usage().Monitor)
	}

	c.status.set("Disconnected (" + c.transportLabel() + ")")

	c.state.Go(c.channelDialer)
}

func (c *KcpTransport) Restart() {
	c.restart(nil, nil, c.Start)
}

// dial opens one KCP session.
//
// The control channel must stay pinned to one endpoint — it is the connection
// the server identifies this peer by — so it passes the current endpoint. Pool
// connections take the next one in the rotation, which spreads them over every
// configured endpoint when load balancing is on.
func (c *KcpTransport) dial(addr string) (*kcp.UDPSession, error) {
	ctx := c.state.Ctx()
	if c.config.DialTimeOut > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.config.DialTimeOut)
		defer cancel()
	}
	return network.KCPDialContext(ctx, addr, c.config.Token, c.kcpSettings)
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
			if err := utils.SendBinaryTransportStringWithin(tunnelConn, c.config.Token, utils.SG_Chan, 10*time.Second); err != nil {
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

			message, answer, err := utils.ReceiveBinaryTransportString(tunnelConn)
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

			if restartingRefusal(message, answer) {
				// Not a failure: the server has the token and is rebuilding its
				// run for this client. Claimed again after the ordinary first
				// backoff, which is about as long as the rebuild takes; a server
				// that kept saying so would still be backed off from, not spun on.
				c.logger.Info("the server is restarting to adopt this client; claiming again")
				tunnelConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}
			if why, refused := refusalReason(message, answer); refused {
				c.logger.Errorf("%s. Retrying...", why)
				tunnelConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}
			if message != c.config.Token {
				c.logger.Errorf("invalid token received (does not match the server's token). Retrying...")
				tunnelConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}

			// Heartbeats every few seconds are all it carries once the pool is
			// up, so it idles like any pool session (kcpidle.go) — keeping the
			// ack-nodelay it was given above when it wakes.
			c.state.SetConn(network.IdleAwareKCP(tunnelConn, c.kcpSettings, true))
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

			c.state.Go(c.poolMaintainer)
			loop := c.control()
			c.state.Go(func() { loop.run() })

			return
		}
	}
}

// poolMaintainer keeps the pool the right size. The policy is poolSizer's,
// shared with every other client transport — see poolmaintain.go.
func (c *KcpTransport) poolMaintainer() {
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

// control is this generation's control loop. See controlLoop.
func (c *KcpTransport) control() controlLoop {
	return c.lifecycle.control(controlwire.Net(c.state.Conn()), c.config.KeepAlive, c.tunnelDialer, c.Restart)
}

func (c *KcpTransport) tunnelDialer() {
	ready, ok := c.beginPoolDial(muxPoolLimit(c.config.ConnPoolSize, c.config.MaxReceiveBuffer))
	if !ok {
		return
	}
	defer ready()

	addr := c.config.Endpoints.Next()
	c.logger.Debugf("initiating new tunnel connection to address %s", addr)

	tunnelConn, err := c.dial(addr)
	if err != nil {
		c.logger.Errorf("tunnel server dialer: %v", err)
		return
	}

	defer c.state.Own(tunnelConn)()

	// KCP has no connection handshake of its own: the server's listener only
	// materialises a session once it receives a packet from this socket. So
	// every pool connection announces itself with the token, which both wakes
	// the listener and authenticates the session before any data flows.
	if err := utils.SendBinaryTransportStringWithin(tunnelConn, c.config.Token, utils.SG_TCP, 10*time.Second); err != nil {
		c.logger.Errorf("failed to announce tunnel connection: %v", err)
		tunnelConn.Close()
		return
	}

	atomic.AddInt32(&c.poolConnections, 1)
	ready()

	c.handleSession(network.IdleAwareKCP(tunnelConn, c.kcpSettings, c.kcpSettings.AckNoDelay))
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

	c.serveSession(session, tunnelConn.RemoteAddr(), c.backend())
}

// backend is how this transport's users reach the local service. See
// backend.go.
func (c *KcpTransport) backend() backendOpts {
	return backendOpts{
		dialTimeout: c.config.DialTimeOut,
		keepAlive:   c.config.KeepAlive,
		rcvBuf:      c.config.SO_RCVBUF,
		sndBuf:      c.config.SO_SNDBUF,
		mss:         0,
		sniffer:     c.config.Sniffer,
	}
}

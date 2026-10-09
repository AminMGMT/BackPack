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
	"github.com/xtaci/smux"
)

type TcpMuxTransport struct {
	// The generations of this transport and what outlives them: see
	// lifecycle.go.
	lifecycle

	config *TcpMuxConfig
	// muxV1/muxV2 are both built up front so that adopting the server's
	// version costs nothing per session; muxVersion is the one in force.
	muxV1      *smux.Config
	muxV2      *smux.Config
	muxVersion atomic.Int32
	// poolNonce is what the server issued for this run; every pool connection
	// presents it so the server need not judge them by source address. Empty
	// against a server too old to issue one.
	poolNonce network.PoolNonce
	// legacyServer decides which handshake to ask for, and remembers what this
	// server has actually proved about itself. It is re-armed on restart: the
	// fallback is a guess drawn from a closed connection, and a closed
	// connection is far more often a dead path than an old server.
	legacyServer legacyProbe
}

type TcpMuxConfig struct {
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
	AggressivePool   bool
	MSS              int
	SO_RCVBUF        int
	SO_SNDBUF        int
	// Outbound says how the connections that reach the tunnel server leave
	// this machine: through a proxy, from a chosen source address or
	// interface, under a routing mark. Nil dials directly. None of it is ever
	// applied to the dial to the local backend — see network/outbound.go.
	Outbound *network.Outbound
}

func NewMuxClient(parentCtx context.Context, config *TcpMuxConfig, logger *logrus.Logger) *TcpMuxTransport {
	// Initialize the TcpTransport struct
	muxSettings := network.MuxSettings{
		MaxFrameSize:     config.MaxFrameSize,
		MaxReceiveBuffer: config.MaxReceiveBuffer,
		MaxStreamBuffer:  config.MaxStreamBuffer,
	}
	client := &TcpMuxTransport{
		muxV1:  network.SmuxConfig(1, muxSettings),
		muxV2:  network.SmuxConfig(2, muxSettings),
		config: config,
	}

	client.firstGeneration(parentCtx, logger, usageSpec{webPort: config.WebPort, snifferLog: config.SnifferLog, sniffer: config.Sniffer})
	return client
}

func (c *TcpMuxTransport) Start() {
	if c.config.WebPort > 0 {
		c.state.Go(c.state.Usage().Monitor)
	}

	c.status.set("Disconnected (TCPMUX)")

	c.state.Go(c.channelDialer)
}

func (c *TcpMuxTransport) Restart() {
	c.restart(nil, func() {
		// See TcpTransport.Restart: a fresh nonce and a fresh handshake, and
		// the mux version is negotiated again with it.
		c.poolNonce.Clear()
		c.legacyServer.reset()
		c.muxVersion.Store(0)
	}, c.Start)
}

func (c *TcpMuxTransport) channelDialer() {
	c.logger.Info("attempting to establish a new tcpmux control channel connection...")

	// One backoff for this reconnect loop (see backoff.go): fixed-interval
	// retries become exponential, so a sustained outage is probed a few times a
	// minute rather than every second.
	bo := newBackoff(c.config.RetryInterval)

	for {
		select {
		case <-c.state.Ctx().Done():
			return
		default:
			tunnelConn, err := network.TcpDialerVia(c.state.Ctx(), c.config.Outbound, c.config.Endpoints.Current(), c.config.DialTimeOut, c.config.KeepAlive, true, 3, 0, 0, 0)
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

			// Sending security token. The nonce-carrying handshake is asked
			// for first; a server that predates it closes the connection
			// without answering, which is what flips the fallback below.
			signal := c.legacyServer.signal()
			err = utils.SendBinaryTransportStringWithin(tunnelConn, c.config.Token, signal, 10*time.Second)
			if err != nil {
				c.logger.Errorf("failed to send security token: %v", err)
				tunnelConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}

			// Set a read deadline for the token response
			if err := tunnelConn.SetReadDeadline(time.Now().Add(controlAckTimeout)); err != nil {
				c.logger.Errorf("failed to set read deadline: %v", err)
				tunnelConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}
			// Receive response
			message, ackSignal, err := utils.ReceiveBinaryTransportString(tunnelConn)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					c.logger.Warn("timeout while waiting for control channel response")
				} else {
					c.logger.Errorf("failed to receive control channel response: %v", err)
					c.legacyServer.miss(c.logger, signal)
				}
				tunnelConn.Close() // Close connection on error or timeout
				bo.Wait(c.state.Ctx())
				continue
			}
			// Resetting the deadline (removes any existing deadline)
			tunnelConn.SetReadDeadline(time.Time{})

			// A refusal is an answer, and a far more useful one than the
			// silence it replaces. It is not a reason to fall back: the server
			// understood the handshake perfectly and declined it.
			if why, refused := refusalReason(message, ackSignal); refused {
				c.logger.Error("the tunnel was refused — " + why)
				c.legacyServer.ack(ackSignal)
				tunnelConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}
			token, nonce, muxVersion := decodeControlAck(message, ackSignal)

			if token == c.config.Token {
				// The server answered, so whatever it answered with is what it
				// speaks — proof that outranks any later closed connection.
				c.legacyServer.ack(ackSignal)
				// Before the control channel is published, so the pool
				// connections poolMaintainer starts below already have it.
				c.poolNonce.Set(nonce)
				// Settled before the pool starts, so no session is ever built
				// on a version the server has not confirmed.
				c.setMuxVersion(muxVersion)
				// The engine knows it holds a control channel; the watchdog reads that
				// rather than the socket table, which shows a socket long after the
				// tunnel behind it has stopped working. See metrics.Snapshot.Connected.
				metrics.ReportPeer(tunnelConn.RemoteAddr().String())
				c.state.SetConn(tunnelConn)
				c.logger.Infof("control channel established successfully (mux version %d)", c.muxVersion.Load())

				c.status.set("Connected (TCPMux)")

				c.state.Go(c.poolMaintainer)
				loop := c.control()
				c.state.Go(func() { loop.run() })

				return
			} else {
				c.logger.Errorf("invalid token received (does not match the server's token). Retrying...")
				tunnelConn.Close() // Close connection if the token is invalid
				bo.Wait(c.state.Ctx())
				continue
			}
		}
	}

}

// poolMaintainer keeps the pool the right size. The policy is poolSizer's,
// shared with every other client transport — see poolmaintain.go.
func (c *TcpMuxTransport) poolMaintainer() {
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
func (c *TcpMuxTransport) control() controlLoop {
	return c.lifecycle.control(controlwire.Net(c.state.Conn()), c.config.KeepAlive, c.tunnelDialer, c.Restart)
}

func (c *TcpMuxTransport) tunnelDialer() {
	ready, ok := c.beginPoolDial(muxPoolLimit(c.config.ConnPoolSize, c.config.MaxReceiveBuffer))
	if !ok {
		return
	}
	defer ready()

	c.logger.Debugf("initiating new tunnel connection to address %s", c.config.RemoteAddr)

	// Dial to the tunnel server
	// Next() rather than Current(): with load balancing enabled the pool
	// spreads its connections over every configured endpoint, so one
	// congested route only slows its own share of the traffic.
	tunnelConn, err := network.TcpDialerVia(c.state.Ctx(), c.config.Outbound, c.config.Endpoints.Next(), c.config.DialTimeOut, c.config.KeepAlive, c.config.Nodelay, 3, c.config.SO_RCVBUF, c.config.SO_SNDBUF, c.config.MSS)
	if err != nil {
		c.logger.Errorf("tunnel server dialer: %v", err)

		return
	}

	defer c.state.Own(tunnelConn)()

	// Say what this connection is, so the server admits it on the nonce rather
	// than on the address it happened to dial out from.
	if err := announcePoolConn(tunnelConn, c.poolNonce.Get()); err != nil {
		c.logger.Debugf("tunnel dialer: failed to announce the pool connection: %v", err)
		tunnelConn.Close()
		return
	}

	// Increment active connections counter
	atomic.AddInt32(&c.poolConnections, 1)
	ready()

	c.handleSession(tunnelConn)
}

func (c *TcpMuxTransport) handleSession(tunnelConn net.Conn) {
	defer func() {
		atomic.AddInt32(&c.poolConnections, -1)
	}()

	// SMUX server
	session, err := smux.Server(tunnelConn, c.smuxCfg())
	if err != nil {
		c.logger.Errorf("failed to create mux session: %v", err)
		return
	}

	c.serveSession(session, tunnelConn.RemoteAddr(), c.backend())
}

// setMuxVersion adopts the version the server settled on. A legacy server sends
// none, and both ends then keep to their own configuration, exactly as they did
// before there was anything to agree about.
func (c *TcpMuxTransport) setMuxVersion(negotiated int) {
	if negotiated != 1 && negotiated != 2 {
		negotiated = c.config.MuxVersion
		if negotiated != 1 && negotiated != 2 {
			// Nothing configured and nothing negotiated: the peer predates the
			// handshake, so it can only be speaking version 1.
			negotiated = 1
		}
	}
	c.muxVersion.Store(int32(negotiated))
}

// smuxCfg returns the session configuration for the version in force.
func (c *TcpMuxTransport) smuxCfg() *smux.Config {
	if c.muxVersion.Load() == 2 {
		return c.muxV2
	}
	return c.muxV1
}

// backend is how this transport's users reach the local service. See
// backend.go.
func (c *TcpMuxTransport) backend() backendOpts {
	return backendOpts{
		dialTimeout: c.config.DialTimeOut,
		keepAlive:   c.config.KeepAlive,
		rcvBuf:      c.config.SO_RCVBUF,
		sndBuf:      c.config.SO_SNDBUF,
		mss:         c.config.MSS,
		sniffer:     c.config.Sniffer,
	}
}

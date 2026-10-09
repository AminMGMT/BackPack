package transport

import (
	"context"
	"net"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/tunnel/naive"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"

	"github.com/sirupsen/logrus"
)

type TcpTransport struct {
	// The generations of this transport and what outlives them: see
	// lifecycle.go.
	lifecycle

	config *TcpConfig
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
type TcpConfig struct {
	RemoteAddr string
	// Endpoints rotates through the server addresses (primary + fallbacks)
	// so a filtered IP or blocked port does not stop the tunnel.
	Endpoints      *network.Endpoints
	Token          string
	SnifferLog     string
	KeepAlive      time.Duration
	RetryInterval  time.Duration
	DialTimeOut    time.Duration
	ConnPoolSize   int
	WebPort        int
	Nodelay        bool
	Sniffer        bool
	AggressivePool bool
	MSS            int
	SO_RCVBUF      int
	SO_SNDBUF      int
	// Outbound says how the connections that reach the tunnel server leave
	// this machine: through a proxy, from a chosen source address or
	// interface, under a routing mark. Nil dials directly. None of it is ever
	// applied to the dial to the local backend — see network/outbound.go.
	Outbound *network.Outbound
	// Stealth wraps every tunnel-carrying connection in the Noise record layer,
	// so the stream has no fingerprint for deep packet inspection to match.
	Stealth bool
	// ManagedHelper carries directional EOF as records through an outer proxy.
	ManagedHelper bool
}

// wrapStealth upgrades a freshly dialled tunnel connection to the Noise record
// layer when this tunnel runs in stealth mode, and returns it unchanged
// otherwise. Only tunnel-carrying connections are wrapped; the dial to the
// local backend stays plain, since that traffic never leaves the machine.
func (c *TcpTransport) wrapStealth(conn net.Conn) (net.Conn, error) {
	if c.config.ManagedHelper {
		return naive.WrapDuplex(conn), nil
	}
	if !c.config.Stealth {
		return conn, nil
	}
	return network.NoiseClientConn(conn, c.config.Token, c.config.DialTimeOut)
}

func NewTCPClient(parentCtx context.Context, config *TcpConfig, logger *logrus.Logger) *TcpTransport {
	// Initialize the TcpTransport struct
	client := &TcpTransport{
		config: config,
	}

	client.firstGeneration(parentCtx, logger, usageSpec{webPort: config.WebPort, snifferLog: config.SnifferLog, sniffer: config.Sniffer})
	return client
}

func (c *TcpTransport) Start() {
	if c.config.WebPort > 0 {
		c.state.Go(c.state.Usage().Monitor)
	}

	c.status.set("Disconnected (TCP)")

	c.state.Go(c.channelDialer)
}
func (c *TcpTransport) Restart() {
	c.restart(nil, func() {
		// The next control channel issues its own nonce; carrying this one
		// over would have the pool announcing a value the server has already
		// forgotten.
		c.poolNonce.Clear()
		// Ask for the current handshake again. A fallback decided during the
		// outage that caused this restart was a guess about the server drawn
		// from a broken path, and carrying it forward costs the nonce for
		// nothing.
		c.legacyServer.reset()
	}, c.Start)
}

func (c *TcpTransport) channelDialer() {
	c.logger.Info("attempting to establish a new control channel connection...")

	// One backoff for this reconnect loop: retries start at the configured
	// interval and stretch as the outage persists, instead of a fixed drumbeat.
	bo := newBackoff(c.config.RetryInterval)

	for {
		select {
		case <-c.state.Ctx().Done():
			return
		default:
			// Raced across the endpoint list rather than tried one at a time.
			//
			// A filtered address does not refuse a connection, it swallows it,
			// so walking the list sequentially costs a whole dial timeout per
			// dead address on every reconnect. The race gives the preferred
			// address a head start and only opens a second connection if that
			// head start expires — so a working first address is never raced.
			// See network.Race.
			//
			//set default behaviour of control channel to nodelay, also using default buffer parameters
			rawConn, won, err := network.Race(c.state.Ctx(),
				c.config.Endpoints.InPreferenceOrder(), network.RaceStagger,
				func(ctx context.Context, addr string) (net.Conn, error) {
					return network.TcpDialerVia(ctx, c.config.Outbound, addr,
						c.config.DialTimeOut, c.config.KeepAlive, true, 3, 0, 0, 0)
				})
			if err != nil {
				c.logger.Errorf("channel dialer: %v", err)
				// Nothing answered. Rotate anyway so the next attempt prefers a
				// different address: the race already tried them all, but the
				// preference is what the health scorer and the pool read.
				if next := c.config.Endpoints.Rotate(); c.config.Endpoints.Len() > 1 {
					c.logger.Infof("trying next server endpoint: %s", next)
				}
				bo.Wait(c.state.Ctx())
				continue
			}
			// Stay on whichever address won, so the data connections that
			// follow go to the same server the control channel is on.
			c.config.Endpoints.Prefer(won)

			// In stealth mode the Noise handshake runs first, so the token and
			// everything after it cross an already-encrypted, unfingerprintable
			// channel. A wrong token fails here, before any tunnel bytes.
			tunnelTCPConn, err := c.wrapStealth(rawConn)
			if err != nil {
				c.logger.Errorf("channel dialer: stealth handshake failed: %v", err)
				rawConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}

			// Sending security token. The nonce-carrying handshake is asked for
			// first; a server that predates it closes the connection without
			// answering, which is what flips the fallback below.
			signal := c.legacyServer.signal()
			err = utils.SendBinaryTransportStringWithin(tunnelTCPConn, c.config.Token, signal, 10*time.Second)
			if err != nil {
				c.logger.Errorf("failed to send security token: %v", err)
				tunnelTCPConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}

			// Set a read deadline for the token response
			if err := tunnelTCPConn.SetReadDeadline(time.Now().Add(controlAckTimeout)); err != nil {
				c.logger.Errorf("failed to set read deadline: %v", err)
				tunnelTCPConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}

			// Receive response
			message, ackSignal, err := utils.ReceiveBinaryTransportString(tunnelTCPConn)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					c.logger.Warn("timeout while waiting for control channel response")
				} else {
					c.logger.Errorf("failed to receive control channel response: %v", err)
					c.legacyServer.miss(c.logger, signal)
				}
				tunnelTCPConn.Close() // Close connection on error or timeout
				bo.Wait(c.state.Ctx())
				continue
			}
			// Resetting the deadline (removes any existing deadline)
			tunnelTCPConn.SetReadDeadline(time.Time{})

			// Plain TCP has no mux sessions, so the version the server sends
			// is not applicable here.
			// A refusal is an answer, and a far more useful one than the
			// silence it replaces. It is not a reason to fall back: the server
			// understood the handshake perfectly and declined it.
			if why, refused := refusalReason(message, ackSignal); refused {
				c.logger.Error("the tunnel was refused — " + why)
				c.legacyServer.ack(ackSignal)
				tunnelTCPConn.Close()
				bo.Wait(c.state.Ctx())
				continue
			}
			token, nonce, _ := decodeControlAck(message, ackSignal)

			if token == c.config.Token {
				// The server answered, so whatever it answered with is what it
				// speaks — proof that outranks any later closed connection.
				c.legacyServer.ack(ackSignal)
				// Before the control channel is published, so the pool
				// connections poolMaintainer starts below already have it.
				c.poolNonce.Set(nonce)
				// The engine knows it holds a control channel; the watchdog reads that
				// rather than the socket table, which shows a socket long after the
				// tunnel behind it has stopped working. See metrics.Snapshot.Connected.
				metrics.ReportPeer(tunnelTCPConn.RemoteAddr().String())
				c.state.SetConn(tunnelTCPConn)
				c.logger.Info("control channel established successfully")

				c.status.set("Connected (TCP)")
				c.state.Go(c.poolMaintainer)
				loop := c.control()
				c.state.Go(func() { loop.run() })

				return

			} else {
				c.logger.Errorf("invalid token received (does not match the server's token). Retrying...")
				tunnelTCPConn.Close() // Close connection if the token is invalid
				bo.Wait(c.state.Ctx())
				continue
			}
		}
	}
}

// poolMaintainer keeps the pool the right size. The policy is poolSizer's,
// shared with every other client transport — see poolmaintain.go.
func (c *TcpTransport) poolMaintainer() {
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

// control is this generation's control loop. See controlLoop.
func (c *TcpTransport) control() controlLoop {
	return c.lifecycle.control(controlwire.Net(c.state.Conn()), c.config.KeepAlive, c.tunnelDialer, c.Restart)
}

// Dialing to the tunnel server, chained functions, without retry
func (c *TcpTransport) tunnelDialer() {
	ready, ok := c.beginPoolDial(c.config.ConnPoolSize * poolGrowthLimit)
	if !ok {
		return
	}
	defer ready()

	c.logger.Debugf("initiating new connection to tunnel server at %s", c.config.RemoteAddr)
	ctx := c.state.Ctx()

	// Dial to the tunnel server
	// Next() rather than Current(): with load balancing enabled the pool
	// spreads its connections over every configured endpoint, so one
	// congested route only slows its own share of the traffic.
	rawConn, err := network.TcpDialerVia(c.state.Ctx(), c.config.Outbound, c.config.Endpoints.Next(), c.config.DialTimeOut, c.config.KeepAlive, c.config.Nodelay, 3, c.config.SO_RCVBUF, c.config.SO_SNDBUF, c.config.MSS)
	if err != nil {
		c.logger.Error("tunnel server dialer: ", err)

		return
	}

	defer c.state.Own(rawConn)()

	// Same stealth upgrade as the control channel: the data connection carries
	// its bytes through the Noise record layer when the tunnel is in that mode.
	tcpConn, err := c.wrapStealth(rawConn)
	if err != nil {
		c.logger.Debugf("tunnel dialer: stealth handshake failed: %v", err)
		rawConn.Close()
		return
	}

	// Say what this connection is, so the server admits it on the nonce rather
	// than on the address it happened to dial out from.
	if err := announcePoolConn(tcpConn, c.poolNonce.Get()); err != nil {
		c.logger.Debugf("tunnel dialer: failed to announce the pool connection: %v", err)
		tcpConn.Close()
		return
	}

	// Increment active connections counter
	atomic.AddInt32(&c.poolConnections, 1)
	ready()

	// Attempt to receive the remote address from the tunnel server
	remoteAddr, transport, err := utils.ReceiveBinaryTransportString(tcpConn)

	// Decrement active connections after successful or failed connection
	atomic.AddInt32(&c.poolConnections, -1)

	if err != nil {
		c.logger.Debugf("failed to receive port from tunnel connection %s: %v", tcpConn.RemoteAddr().String(), err)
		tcpConn.Close()
		return
	}

	// A forwarded UDP flow says so in the target address, on this transport as
	// on every other one, so there is one thing to recognise rather than a
	// signal byte here and a marked address everywhere else.
	if dialForwardedUDP(ctx, c.config.DialTimeOut, tcpConn, remoteAddr, c.logger, c.state.Usage(), c.config.Sniffer) {
		return
	}

	// Extract the port from the received address
	port, resolvedAddr, err := network.ResolveRemoteAddr(remoteAddr)
	if err != nil {
		c.logger.Infof("failed to resolve remote port: %v", err)
		tcpConn.Close() // Close the connection on error
		return
	}

	switch transport {
	case utils.SG_TCP:
		// Dial local server using the received address
		c.relay(tcpConn, resolvedAddr, port, c.backend())

	default:
		c.logger.Error("undefined transport. close the connection.")
		tcpConn.Close()
	}
}

// backend is how this transport's users reach the local service. See
// backend.go.
func (c *TcpTransport) backend() backendOpts {
	return backendOpts{
		dialTimeout: c.config.DialTimeOut,
		keepAlive:   c.config.KeepAlive,
		rcvBuf:      c.config.SO_RCVBUF,
		sndBuf:      c.config.SO_SNDBUF,
		mss:         c.config.MSS,
		sniffer:     c.config.Sniffer,
	}
}

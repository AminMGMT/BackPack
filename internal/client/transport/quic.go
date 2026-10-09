package transport

import (
	"context"
	"crypto/subtle"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"

	"github.com/quic-go/quic-go"
	"github.com/sirupsen/logrus"
)

// QuicTransport is the client side of the QUIC transport. It dials one QUIC
// connection out to the server and carries everything over its streams: a
// control stream for the signalling, and a stream per forwarded flow. QUIC
// supplies the TLS 1.3, the multiplexing, the congestion control and the loss
// recovery, so there is no smux and no KCP-style tuning to keep in step.
type QuicTransport struct {
	// The generations of this transport and what outlives them: see
	// lifecycle.go.
	lifecycle

	config       *QuicConfig
	quicSettings network.QUICSettings

	// connMu guards quicConn, the connection this run opens its streams on. It is
	// replaced on every restart, so a data stream is always opened on the current
	// connection rather than a torn-down one.
	connMu   sync.Mutex
	quicConn *quic.Conn
}

type QuicConfig struct {
	RemoteAddr string
	// Endpoints rotates through the server addresses (primary + fallbacks) so a
	// filtered IP or blocked port does not stop the tunnel.
	Endpoints      *network.Endpoints
	Token          string
	SnifferLog     string
	Sniffer        bool
	KeepAlive      time.Duration
	RetryInterval  time.Duration
	DialTimeOut    time.Duration
	ConnPoolSize   int
	WebPort        int
	AggressivePool bool
	SO_RCVBUF      int
	SO_SNDBUF      int
}

func (c *QuicConfig) settings() network.QUICSettings {
	return network.QUICSettings{
		DialTimeout:     c.DialTimeOut,
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
	client := &QuicTransport{
		config:       config,
		quicSettings: config.settings(),
	}
	// Seed the first generation through the same path a restart uses, so there is
	// only one way this state is ever published.
	client.firstGeneration(parentCtx, logger, usageSpec{webPort: config.WebPort, snifferLog: config.SnifferLog, sniffer: config.Sniffer})
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
		c.state.Go(c.state.Usage().Monitor)
	}

	c.status.set("Disconnected (QUIC)")

	c.state.Go(c.channelDialer)
}

func (c *QuicTransport) Restart() {
	c.restart(func() {
		// The connection the streams ride on belongs to the generation too.
		if qc := c.getQUICConn(); qc != nil {
			_ = qc.CloseWithError(0, "restart")
			c.setQUICConn(nil)
		}
	}, nil, c.Start)
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

			// A proof of the token, bound to this TLS session, rather than the
			// token itself — see network/quicbind.go. The certificate is not
			// verified, so whatever answered this dial may not be the server,
			// and the token is the one thing it must not be handed.
			proof, err := network.QUICClientProof(conn, c.config.Token)
			if err != nil {
				c.logger.Errorf("could not bind the credential to the QUIC session: %v", err)
				_ = conn.CloseWithError(0, "binding failed")
				bo.Wait(c.state.Ctx())
				continue
			}
			if err := utils.SendBinaryTransportStringWithin(control, proof, utils.SG_Chan, 10*time.Second); err != nil {
				c.logger.Errorf("failed to send the control channel claim: %v", err)
				_ = conn.CloseWithError(0, "claim send failed")
				bo.Wait(c.state.Ctx())
				continue
			}

			if err := control.SetReadDeadline(time.Now().Add(c.config.DialTimeOut)); err != nil {
				c.logger.Errorf("failed to set read deadline: %v", err)
				_ = conn.CloseWithError(0, "deadline failed")
				bo.Wait(c.state.Ctx())
				continue
			}

			message, answer, err := utils.ReceiveBinaryTransportString(control)
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

			if why, refused := refusalReason(message, answer); refused {
				// A server before v1.8.2 compares what it is sent with the
				// token and so refuses a proof as a wrong token. Said here
				// because it is the likelier reading right after an upgrade.
				c.logger.Errorf("%s. If the server runs a version older than v1.8.2, upgrade "+
					"it: since then this client proves the token instead of sending it, and an "+
					"older server reads the proof as a wrong token. Retrying...", why)
				_ = conn.CloseWithError(0, "refused")
				bo.Wait(c.state.Ctx())
				continue
			}
			want, err := network.QUICServerProof(conn, c.config.Token)
			if err != nil || subtle.ConstantTimeCompare([]byte(message), []byte(want)) != 1 {
				// Not the server's proof: whatever answered does not hold the
				// token, or holds a different TLS session from ours — which is
				// what something terminating the TLS in between looks like.
				c.logger.Errorf("the server did not prove it holds the token (a different token, or " +
					"something between here and the server terminating the TLS). Retrying...")
				_ = conn.CloseWithError(0, "bad proof")
				bo.Wait(c.state.Ctx())
				continue
			}

			c.setQUICConn(conn)
			if !c.state.SetConn(control) {
				_ = conn.CloseWithError(0, "generation ended before admission")
				return
			}
			// Cancellation can seal the generation before its control worker
			// is registered. Close the carrier in that gap as well, allowing
			// the same short grace period as the control loop's goodbye.
			context.AfterFunc(c.state.Ctx(), func() {
				time.AfterFunc(100*time.Millisecond, func() { _ = conn.CloseWithError(0, "generation ended") })
			})
			c.logger.Info("control channel established successfully")

			// Recorded on this side too, so the panel does not have to infer a
			// datagram tunnel's state from a socket table. See the KCP client's
			// channelDialer for what the inference got wrong.
			metrics.ReportPeer(conn.RemoteAddr().String())

			c.status.set("Connected (QUIC)")

			c.state.Go(c.poolMaintainer)
			// The connection is this generation's, streams and socket with it,
			// and ends with its control loop — after the goodbye, which a close
			// racing it would drop — whether or not a restart follows.
			loop := c.control()
			c.state.Go(func() {
				loop.run()
				_ = conn.CloseWithError(0, "generation ended")
			})

			return
		}
	}
}

// poolMaintainer keeps the pool the right size. The policy is poolSizer's,
// shared with every other client transport — see poolmaintain.go.
func (c *QuicTransport) poolMaintainer() {
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
		maxSize:    c.config.ConnPoolSize * poolGrowthLimit,
	}.maintain()
}

// control is this generation's control loop. See controlLoop.
func (c *QuicTransport) control() controlLoop {
	return c.lifecycle.control(controlwire.Net(c.state.Conn()), c.config.KeepAlive, c.tunnelDialer, c.Restart)
}

// tunnelDialer opens one data stream, announces it, and then waits for the
// server to name the backend it should carry.
func (c *QuicTransport) tunnelDialer() {
	ready, ok := c.beginPoolDial(c.config.ConnPoolSize * poolGrowthLimit)
	if !ok {
		return
	}
	defer ready()

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
	defer c.state.Own(data)()

	// Announce the stream with the connection's proof so the server can
	// authenticate it and file it as a data stream. The proof, not the token,
	// for the reason the control claim uses one.
	proof, err := network.QUICClientProof(qc, c.config.Token)
	if err != nil {
		c.logger.Errorf("could not bind the credential to the QUIC session: %v", err)
		data.Close()
		return
	}
	if err := utils.SendBinaryTransportStringWithin(data, proof, utils.SG_TCP, 10*time.Second); err != nil {
		c.logger.Errorf("failed to announce tunnel stream: %v", err)
		data.Close()
		return
	}

	atomic.AddInt32(&c.poolConnections, 1)
	ready()

	// Wait until the server assigns this stream a backend to reach.
	remoteAddr, err := utils.ReceiveBinaryString(data)
	atomic.AddInt32(&c.poolConnections, -1)
	if err != nil {
		c.logger.Tracef("tunnel stream closed before use: %v", err)
		data.Close()
		return
	}

	c.relayStream(data, remoteAddr, c.backend())
}

// backend is how this transport's users reach the local service. See
// backend.go.
func (c *QuicTransport) backend() backendOpts {
	return backendOpts{
		dialTimeout: c.config.DialTimeOut,
		keepAlive:   c.config.KeepAlive,
		rcvBuf:      c.config.SO_RCVBUF,
		sndBuf:      c.config.SO_SNDBUF,
		mss:         0,
		sniffer:     c.config.Sniffer,
	}
}

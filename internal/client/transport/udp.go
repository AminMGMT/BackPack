package transport

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/sirupsen/logrus"
)

type UdpTransport struct {
	// The generations of this transport and what outlives them: see
	// lifecycle.go.
	lifecycle

	config *UdpConfig
}
type UdpConfig struct {
	RemoteAddr string
	// Endpoints rotates through the server addresses (primary + fallbacks)
	// so a filtered IP or blocked port does not stop the tunnel.
	Endpoints      *network.Endpoints
	Token          string
	SnifferLog     string
	RetryInterval  time.Duration
	DialTimeOut    time.Duration
	ConnPoolSize   int
	WebPort        int
	Sniffer        bool
	AggressivePool bool
	// SO_RCVBUF/SO_SNDBUF size every UDP socket the client opens — the sockets
	// to the server and to the local backend. The kernel default is small enough
	// that a datagram flood overruns it and drops packets before they are read;
	// the preset's several MB is what carries a speed test without stalling.
	SO_RCVBUF int
	SO_SNDBUF int
}

func NewUDPClient(parentCtx context.Context, config *UdpConfig, logger *logrus.Logger) *UdpTransport {
	// Initialize the TcpTransport struct
	client := &UdpTransport{
		config: config,
	}

	client.firstGeneration(parentCtx, logger, usageSpec{webPort: config.WebPort, snifferLog: config.SnifferLog, sniffer: config.Sniffer})
	return client
}

func (c *UdpTransport) Start() {
	if c.config.WebPort > 0 {
		c.state.Go(c.state.Usage().Monitor)
	}

	c.status.set("Disconnected (UDP)")

	c.state.Go(c.channelDialer)
}

func (c *UdpTransport) Restart() {
	c.restart(nil, nil, c.Start)
}

func (c *UdpTransport) channelDialer() {
	c.logger.Info("attempting to establish a new control channel connection...")

	// One backoff for this reconnect loop (see backoff.go): fixed-interval
	// retries become exponential, so a sustained outage is probed a few times a
	// minute rather than every second.
	bo := newBackoff(c.config.RetryInterval)

	for {
		select {
		case <-c.state.Ctx().Done():
			return
		default:
			tunnelTCPConn, err := network.TcpDialer(c.state.Ctx(), c.config.Endpoints.Current(), c.config.DialTimeOut, 30, true, 3, 0, 0, 0)
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

			// Sending security token
			err = utils.SendBinaryTransportStringWithin(tunnelTCPConn, c.config.Token, utils.SG_Chan, 10*time.Second)
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
			message, _, err := utils.ReceiveBinaryTransportString(tunnelTCPConn)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					c.logger.Warn("timeout while waiting for control channel response")
				} else {
					c.logger.Errorf("failed to receive control channel response: %v", err)
				}
				tunnelTCPConn.Close() // Close connection on error or timeout
				bo.Wait(c.state.Ctx())
				continue
			}
			// Resetting the deadline (removes any existing deadline)
			tunnelTCPConn.SetReadDeadline(time.Time{})

			if message == c.config.Token {
				// See metrics.Snapshot.Connected.
				metrics.ReportPeer(tunnelTCPConn.RemoteAddr().String())
				c.state.SetConn(tunnelTCPConn)
				c.logger.Info("control channel established successfully")

				c.status.set("Connected (UDP)")

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
func (c *UdpTransport) poolMaintainer() {
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
func (c *UdpTransport) control() controlLoop {
	return c.lifecycle.control(controlwire.Net(c.state.Conn()), 0, c.tunnelDialer, c.Restart)
}

func (c *UdpTransport) tunnelDialer() {
	ready, ok := c.beginPoolDial(c.config.ConnPoolSize * poolGrowthLimit)
	if !ok {
		return
	}
	defer ready()

	c.logger.Debugf("initiating new connection to tunnel server at %s", c.config.RemoteAddr)
	ctx := c.state.Ctx()

	// Next() rather than Current(): with load balancing enabled the pool
	// spreads its connections over every configured endpoint, so one
	// congested route only slows its own share of the traffic.
	tunConn, err := dialUDPContext(ctx, c.config.DialTimeOut, c.config.Endpoints.Next())
	if err != nil {
		c.logger.Error("failed to connect to server:", err)
		return
	}

	c.applyBuffers(tunConn)

	defer tunConn.Close()

	done := make(chan struct{})

	// Start handleTunnelConn in a goroutine
	go func() {
		c.handleTunnelConnReady(tunConn, ready)
		close(done) // Signal that handleTunnelConn is done
	}()

	// Wait for either handleTunnelConn to finish or the context to be done
	select {
	case <-done:
	case <-c.state.Ctx().Done():
		tunConn.Close()
		<-done
	}
}

func (c *UdpTransport) handleTunnelConnReady(tunConn *net.UDPConn, ready func()) {
	// Send token message to the server
	_, err := tunConn.Write([]byte(c.config.Token))
	if err != nil {
		c.logger.Error("faliled to send token:", err)
		return
	}

	// Increment active connections counter
	atomic.AddInt32(&c.poolConnections, 1)
	ready()

	// Prepare a buffer to receive the server's response
	buffer := make([]byte, 47) // maximum buffer requried for store in IPv6:Port format

	for {
		n, _, err := tunConn.ReadFromUDP(buffer)
		if err != nil {
			c.logger.Error("failed to receive response from server:", err)

			atomic.AddInt32(&c.poolConnections, -1)

			return
		}

		// Compare the received bytes with the expected SG_Ping message
		if n == 1 && buffer[0] == utils.SG_Ping {
			c.logger.Tracef("ping signal recieved for %s", tunConn.LocalAddr().String())
			continue
		}

		port, remoteAddr, err := network.ResolveRemoteAddr(string(buffer[:n]))

		// Decrement active connections after successful or failed connection
		atomic.AddInt32(&c.poolConnections, -1)

		if err != nil {
			c.logger.Error("failed to find remote address:", err)
			return
		}

		c.localDialer(remoteAddr, port, tunConn)

		break
	}

}

func (c *UdpTransport) localDialer(remoteAddr string, port int, tunConn *net.UDPConn) {
	ctx := c.state.Ctx()
	// UDP backends cannot be health-checked with a TCP probe, so the pool does
	// not load-balance them; a configured list just uses the first entry.
	remoteAddr = firstBackend(remoteAddr)
	remoteConn, err := dialUDPContext(ctx, c.config.DialTimeOut, remoteAddr)
	if err != nil {
		c.logger.Errorf("failed to dial remote UDP address: %v", err)
		return
	}

	c.applyBuffers(remoteConn)

	defer c.state.Own(remoteConn)()

	started := time.Now()
	var activity atomic.Int64
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		tunConn.Close()
		remoteConn.Close()
	})
	defer stop()
	c.logger.Debugf("start to copy from tunnel %s to local %s", tunConn.LocalAddr(), remoteAddr)
	go func() {
		c.udpCopy(remoteConn, tunConn, port, true, started, &activity)
		tunConn.Close()
		remoteConn.Close()
		done <- struct{}{}
	}()

	c.udpCopy(tunConn, remoteConn, port, false, started, &activity)
	// Ending either direction must release the other reader before joining it.
	tunConn.Close()
	remoteConn.Close()
	<-done

}

// udpCopy forwards datagrams one way between the tunnel socket and a backend.
//
// dstIsTunnel says which way, and it is there for the traffic counters. The
// convention is the one CountedConn sets on every other transport and is about
// the tunnel rather than about this function: bytes read off the tunnel are
// inbound, bytes written to it are outbound, on both ends of the link. This
// transport counted neither, so however much it carried the panel, the CLI, the
// Telegram report and the traffic history all read it as an idle tunnel.
func (c *UdpTransport) udpCopy(srcConn, dstConn *net.UDPConn, port int, dstIsTunnel bool, started time.Time, activity *atomic.Int64) {
	buf := make([]byte, network.MaxDatagram)
	readTimeout := 60 * time.Second

	for {
		// Either direction keeps this flow alive. Use the remaining shared idle
		// budget, so a final packet does not leave the quiet reader another 60s.
		remaining := readTimeout - (time.Since(started) - time.Duration(activity.Load()))
		if remaining <= 0 {
			return
		}
		err := srcConn.SetReadDeadline(time.Now().Add(remaining))
		if err != nil {
			c.logger.Errorf("failed to set read deadline: %v", err)
			return
		}

		// Read from the UDP source connection
		n, _, err := srcConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				if time.Since(started)-time.Duration(activity.Load()) < readTimeout {
					continue
				}
				c.logger.Debug("read from UDP timed out")
				return // Exit on timeout
			}
			c.logger.Errorf("failed to read from UDP: %v", err)
			return
		}

		if !dstIsTunnel {
			// Read off the tunnel. Counted here rather than after the write:
			// these bytes crossed the tunnel whether or not the backend took
			// them, which is what a traffic figure is measuring.
			metrics.AddBytes(uint64(n), 0)
		}

		// One write preserves the packet boundary and forwards empty datagrams.
		totalWritten, err := dstConn.Write(buf[:n])
		if err == nil && totalWritten != n {
			err = io.ErrShortWrite
		}
		if err != nil {
			c.logger.Errorf("failed to write to UDP %s: %v", dstConn.RemoteAddr().String(), err)
			return
		}

		touchUDPActivity(started, activity)
		if dstIsTunnel {
			metrics.AddBytes(0, uint64(totalWritten))
		}

		// Optionally update the port usage stats if sniffing is enabled
		if c.config.Sniffer {
			c.state.Usage().AddOrUpdatePort(port, uint64(totalWritten))
		}

		if c.logger.IsLevelEnabled(logrus.DebugLevel) {
			c.logger.Debugf("forwarded %d bytes from %s to %s", n, srcConn.LocalAddr().String(), dstConn.RemoteAddr().String())
		}
	}
}

// Both copy workers share elapsed monotonic time; a delayed update must not
// overwrite a newer packet's activity.
func touchUDPActivity(started time.Time, activity *atomic.Int64) {
	now := time.Since(started).Nanoseconds()
	for previous := activity.Load(); now > previous; previous = activity.Load() {
		if activity.CompareAndSwap(previous, now) {
			return
		}
	}
}

// applyBuffers sizes a datagram socket to the configured SO_RCVBUF/SO_SNDBUF;
// see network.SizeUDPBuffers.
func (c *UdpTransport) applyBuffers(conn *net.UDPConn) {
	network.SizeUDPBuffers(conn, c.config.SO_RCVBUF, c.config.SO_SNDBUF, c.logger.Warnf)
}

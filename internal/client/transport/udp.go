package transport

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/backpack/backpack/internal/web"
	"github.com/sirupsen/logrus"
)

type UdpTransport struct {
	// The status shown in the panel. Behind a lock because the run being
	// replaced and the run replacing it both write it. See tunnelStatus.
	status          tunnelStatus
	config          *UdpConfig
	parentctx       context.Context
	state           clientState
	logger          *logrus.Logger
	restartMutex    sync.Mutex
	poolConnections int32
	loadConnections int32
	controlFlow     chan struct{}
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
	// Create a derived context from the parent context
	ctx, cancel := context.WithCancel(parentCtx)

	// Initialize the TcpTransport struct
	client := &UdpTransport{
		config:          config,
		parentctx:       parentCtx,
		logger:          logger,
		poolConnections: 0,
		loadConnections: 0,
		controlFlow:     make(chan struct{}, 100),
	}

	// Seed the first generation through the same path a restart uses, so
	// there is only one way this state is ever published.
	client.state.Reset(ctx, cancel, web.NewDataStore(fmt.Sprintf(":%v", config.WebPort), ctx, config.SnifferLog, config.Sniffer, client.status.get, logger))
	return client
}

func (c *UdpTransport) Start() {
	if c.config.WebPort > 0 {
		go c.state.Usage().Monitor()
	}

	c.status.set("Disconnected (UDP)")

	go c.channelDialer()
}

func (c *UdpTransport) Restart() {
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

	// close control channel connection
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
	drain(c.controlFlow)

	// set the log level again
	c.logger.SetLevel(level)

	go c.Start()

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
			err = utils.SendBinaryTransportString(tunnelTCPConn, c.config.Token, utils.SG_Chan)
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
				c.state.SetConn(tunnelTCPConn)
				c.logger.Info("control channel established successfully")

				c.status.set("Connected (UDP)")

				go c.poolMaintainer()
				go c.channelHandler()

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

func (c *UdpTransport) poolMaintainer() {
	for i := 0; i < c.config.ConnPoolSize; i++ { //initial pool filling
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

	newPoolSize := c.config.ConnPoolSize // intial value
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

				// Add a new connection to the pool
				go c.tunnelDialer()
			} else if float64(loadConnections+x) < float64(poolConnectionsAvg)*y && newPoolSize > c.config.ConnPoolSize {
				c.logger.Debugf("decreasing pool size: %d -> %d, avg pool conn: %d, avg load conn: %d", newPoolSize, newPoolSize-1, poolConnectionsAvg, loadConnections)
				newPoolSize--

				// send a signal to controlFlow
				c.controlFlow <- struct{}{}
			}
		}
	}

}

func (c *UdpTransport) channelHandler() {
	msgChan := make(chan byte, 1000)

	// Goroutine to handle the blocking ReceiveBinaryString
	go func() {
		for {
			select {
			case <-c.state.Ctx().Done():
				return
			default:
				// The worst case of the three: this control channel had no
				// deadline at all, and the datagram transports have no
				// keepalive story to fall back on, so a read on a tunnel whose
				// peer had gone blocked until the process was restarted. The
				// server heartbeats, so silence past the deadline is the peer
				// being gone. See controlDeadline; UdpConfig carries no
				// keepalive period, so this takes the fallback.
				if err := c.state.Conn().SetReadDeadline(time.Now().Add(controlDeadline(0))); err != nil {
					if c.state.Cancel() != nil {
						c.logger.Errorf("failed to set control channel deadline: %v", err)
						go c.Restart()
					}
					return
				}
				msg, err := utils.ReceiveBinaryByte(c.state.Conn())
				if err != nil {
					if c.state.Cancel() != nil {
						c.logger.Error("failed to read from control channel. ", err)
						go c.Restart()
					}
					return
				}
				msgChan <- msg
			}
		}
	}()

	// Main loop to listen for context cancellation or received messages
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

			case utils.SG_RTT:
				err := utils.SendBinaryByte(c.state.Conn(), utils.SG_RTT)
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

// applyBuffers sizes a datagram socket to the configured SO_RCVBUF/SO_SNDBUF. A
// zero value leaves the kernel default in place. Best effort: a socket that
// refuses the size — usually because net.core.rmem_max is lower — still works,
// it just has less headroom against a burst.
func (c *UdpTransport) applyBuffers(conn *net.UDPConn) {
	if c.config.SO_RCVBUF > 0 {
		if err := conn.SetReadBuffer(c.config.SO_RCVBUF); err != nil {
			c.logger.Warnf("failed to set UDP read buffer to %d: %v", c.config.SO_RCVBUF, err)
		}
	}
	if c.config.SO_SNDBUF > 0 {
		if err := conn.SetWriteBuffer(c.config.SO_SNDBUF); err != nil {
			c.logger.Warnf("failed to set UDP write buffer to %d: %v", c.config.SO_SNDBUF, err)
		}
	}
}

func (c *UdpTransport) tunnelDialer() {
	c.logger.Debugf("initiating new connection to tunnel server at %s", c.config.RemoteAddr)

	// Next() rather than Current(): with load balancing enabled the pool
	// spreads its connections over every configured endpoint, so one
	// congested route only slows its own share of the traffic.
	remoteAddr, err := net.ResolveUDPAddr("udp", c.config.Endpoints.Next())
	if err != nil {
		c.logger.Error("failed to resolve tunnel address:", err)
		return
	}

	tunConn, err := net.DialUDP("udp", nil, remoteAddr)
	if err != nil {
		c.logger.Error("failed to connect to server:", err)
		return
	}

	c.applyBuffers(tunConn)

	defer tunConn.Close()

	done := make(chan struct{})

	// Start handleTunnelConn in a goroutine
	go func() {
		c.handleTunnelConn(tunConn)
		close(done) // Signal that handleTunnelConn is done
	}()

	// Wait for either handleTunnelConn to finish or the context to be done
	select {
	case <-done:
	case <-c.state.Ctx().Done():
	}
}

func (c *UdpTransport) handleTunnelConn(tunConn *net.UDPConn) {
	// Send token message to the server
	_, err := tunConn.Write([]byte(c.config.Token))
	if err != nil {
		c.logger.Error("faliled to send token:", err)
		return
	}

	// Increment active connections counter
	atomic.AddInt32(&c.poolConnections, 1)

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
	// UDP backends cannot be health-checked with a TCP probe, so the pool does
	// not load-balance them; a configured list just uses the first entry.
	remoteAddr = firstBackend(remoteAddr)
	remoteResolvedAddr, err := net.ResolveUDPAddr("udp", remoteAddr)
	if err != nil {
		c.logger.Error("failed to resolve remote address:", err)
		return
	}

	// Dial the remote UDP server
	remoteConn, err := net.DialUDP("udp", nil, remoteResolvedAddr)
	if err != nil {
		// Falling through here dereferenced a nil remoteConn one line later and
		// took the whole client down with it; there is nothing to forward to, so
		// stop.
		c.logger.Errorf("failed to dial remote UDP address: %v", err)
		return
	}

	c.applyBuffers(remoteConn)

	defer remoteConn.Close()

	done := make(chan struct{})
	c.logger.Debugf("start to copy from tunnel %s to local %s", tunConn.LocalAddr(), remoteAddr)
	go func() {
		c.udpCopy(remoteConn, tunConn, port)
		done <- struct{}{}
	}()

	c.udpCopy(tunConn, remoteConn, port)

	<-done

}

func (c *UdpTransport) udpCopy(srcConn, dstConn *net.UDPConn, port int) {
	buf := make([]byte, 16*1024)
	readTimeout := 60 * time.Second

	for {
		// Set the read deadline to 60 seconds from now
		err := srcConn.SetReadDeadline(time.Now().Add(readTimeout))
		if err != nil {
			c.logger.Errorf("failed to set read deadline: %v", err)
			return
		}

		// Read from the UDP source connection
		n, _, err := srcConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				c.logger.Debug("read from UDP timed out")
				return // Exit on timeout
			}
			c.logger.Errorf("failed to read from UDP: %v", err)
			return
		}

		totalWritten := 0
		// Write the read data to the destination UDP connection
		for totalWritten < n {
			w, err := dstConn.Write(buf[totalWritten:n])
			if err != nil {
				c.logger.Errorf("failed to write to UDP %s: %v", dstConn.RemoteAddr().String(), err)
				return
			}
			totalWritten += w
		}

		// Optionally update the port usage stats if sniffing is enabled
		if c.config.Sniffer {
			c.state.Usage().AddOrUpdatePort(port, uint64(totalWritten))
		}

		c.logger.Debugf("forwarded %d bytes from %s to %s", n, srcConn.LocalAddr().String(), dstConn.RemoteAddr().String())
	}
}

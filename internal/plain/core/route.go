package core

import (
	"context"
	"net"
	"time"

	"github.com/backpack/backpack/internal/utils/network"
	"github.com/sirupsen/logrus"
)

// Route is where a plain client's tunnel connections go and how they get
// there: the server's addresses (remote_addr and fallback_addrs, with
// load_balance and health_failover), and the outbound path (proxy, local_addr,
// interface, so_mark). Backpack.
//
// The engine this was ported from dials remote_addr directly and knows of
// neither, so on its six transports those settings were accepted, saved and
// shown and never applied. A nil Route, or one with nothing set, dials as that
// engine did, by the same code.
//
// The connection to the forwarded service on the kharej never goes through a
// Route: that dial stays local, and a proxy or interface meant for the uplink
// would break it.
type Route struct {
	Endpoints *network.Endpoints
	Outbound  *network.Outbound
}

// Control is the address the control channel dials now: the current endpoint,
// or primary when there are none.
func (r *Route) Control(primary string) string {
	if r == nil || r.Endpoints.Len() == 0 {
		return primary
	}
	return r.Endpoints.Current()
}

// Data is the address a new data connection dials: under load_balance the next
// endpoint in turn, otherwise the control channel's.
func (r *Route) Data(primary string) string {
	if r == nil || r.Endpoints.Len() == 0 {
		return primary
	}
	return r.Endpoints.Next()
}

// ControlFailed moves the control channel to the next endpoint after it could
// not be reached at this one. A single address stays where it is.
func (r *Route) ControlFailed(logger *logrus.Logger) {
	if r == nil || r.Endpoints.Len() <= 1 {
		return
	}
	logger.Infof("the server could not be reached at this address; trying %s next", r.Endpoints.Rotate())
}

// Dial opens a tunnel connection to addr: by the outbound path when one is
// set, otherwise directly, exactly as TcpDialer does.
func (r *Route) Dial(ctx context.Context, addr string, timeout, keepAlive time.Duration, nodelay bool, retry, rcvBuf, sndBuf, mss int) (*net.TCPConn, error) {
	if r == nil || !r.Outbound.IsSet() {
		return TcpDialer(ctx, addr, "", timeout, keepAlive, nodelay, retry, rcvBuf, sndBuf, mss)
	}
	return network.TcpDialerVia(ctx, r.Outbound, addr, timeout, keepAlive, nodelay, retry, rcvBuf, sndBuf, mss)
}

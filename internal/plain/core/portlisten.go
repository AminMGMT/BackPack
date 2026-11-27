package core

import (
	"context"
	"net"
	"time"

	"github.com/sirupsen/logrus"
)

// Opening a forwarded port's listener, and opening it again until it can be.
//
// The engine this was ported from ended the process when a forwarded port
// could not be bound — another program held it, or the address was not on
// this server — and systemd started it again to fail the same way. One busy
// port took every other port of the tunnel down with it, over and over. Here
// that one port is tried again every PortRetry, and the rest stay up.
//
// The tunnel's own listener (bind_addr) still ends the process when it cannot
// be opened: without it nothing works, and a restart is the right answer.

// PortRetry is how often a forwarded port that could not be bound is tried
// again. A variable so a test can shorten it.
var PortRetry = 5 * time.Second

// ListenPort opens the TCP listener for a forwarded port. It returns nil only
// when ctx ends before the port could be bound.
func ListenPort(ctx context.Context, logger *logrus.Logger, localAddr string) net.Listener {
	l, _ := openRetrying(ctx, logger, localAddr, func() (net.Listener, error) {
		return net.Listen("tcp", localAddr)
	})
	return l
}

// ListenPortUDP is ListenPort for a forwarded UDP port.
func ListenPortUDP(ctx context.Context, logger *logrus.Logger, localAddr string) *net.UDPConn {
	l, _ := openRetrying(ctx, logger, localAddr, func() (*net.UDPConn, error) {
		addr, err := net.ResolveUDPAddr("udp", localAddr)
		if err != nil {
			return nil, err
		}
		return net.ListenUDP("udp", addr)
	})
	return l
}

func openRetrying[T any](ctx context.Context, logger *logrus.Logger, localAddr string, open func() (T, error)) (T, bool) {
	var zero T
	failed := false
	for {
		l, err := open()
		if err == nil {
			if failed {
				logger.Infof("port %s is free now; listening on it", localAddr)
			}
			return l, true
		}
		if !failed {
			// The first line in the shape the log explanations know, so a
			// busy port reads as one; then what happens next.
			logger.Errorf("failed to listen on %s: %v", localAddr, err)
			logger.Warnf("trying port %s again every %s; the tunnel's other ports stay up", localAddr, PortRetry)
			failed = true
		}
		t := time.NewTimer(PortRetry)
		select {
		case <-ctx.Done():
			t.Stop()
			return zero, false
		case <-t.C:
		}
	}
}

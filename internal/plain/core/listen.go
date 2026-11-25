package core

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// ListenWithBuffers opens the tunnel listener with the configured socket
// buffers, MSS and TCP_NODELAY (dis_nodelay true turns Nagle back on).
func ListenWithBuffers(network, address string, rcvBufSize, sndBufSize, mss int, keepAlivePeriod time.Duration, dis_nodelay bool) (net.Listener, error) {
	listenerCfg := &net.ListenConfig{
		Control: func(network, address string, s syscall.RawConn) error {
			err := ReusePortControl(network, address, s)
			if err != nil {
				return err
			}

			if rcvBufSize > 0 {
				err = s.Control(func(fd uintptr) {
					if err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, rcvBufSize); err != nil {
						err = fmt.Errorf("failed to set SO_RCVBUF: %v", err)
					}
				})
			}
			if err != nil {
				return err
			}

			if sndBufSize > 0 {
				err = s.Control(func(fd uintptr) {
					if err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, sndBufSize); err != nil {
						err = fmt.Errorf("failed to set SO_SNDBUF: %v", err)
					}
				})
			}

			if mss > 0 {
				err = s.Control(func(fd uintptr) {
					if err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_MAXSEG, mss); err != nil {
						err = fmt.Errorf("failed to set MSS: %v", err)
					}
				})
			}

			if !dis_nodelay {
				err = s.Control(func(fd uintptr) {
					if err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1); err != nil {
						err = fmt.Errorf("failed to set TCP_NODELAY: %v", err)
					}
				})
			} else {
				err = s.Control(func(fd uintptr) {
					if err = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 0); err != nil {
						err = fmt.Errorf("failed to unset TCP_NODELAY: %v", err)
					}
				})
			}

			return err
		},
		KeepAliveConfig: net.KeepAliveConfig{Enable: true, Interval: keepAlivePeriod, Count: 9, Idle: 0},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return listenerCfg.Listen(ctx, network, address)
}

// notSentLowat bounds what a mux session's socket holds in the kernel that
// has not been sent yet. Speed edit, for latency under load:
// without it a bulk stream fills the session socket's send queue with
// megabytes and every other stream on that session waits behind them — a
// small request on a busy tcpmux tunnel waited over a second (soak, 100 ms
// path, link full); with it, 116 ms at the same throughput. Only the backlog
// behind the data in flight is bounded.
//
// Mux sessions only. A plain tcp or ws tunnel gives each user a socket of its
// own, so there is no one to wait behind, and there the bound cost the
// upload: the kernel stopped growing the send buffer of the accepted
// connections and each held about this much per round trip — 60 Mbit/s where
// the path carries 800 (soak, 100 ms, BBR).
const notSentLowat = 128 * 1024

func setNotSentLowat(s syscall.RawConn) {
	_ = s.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT, notSentLowat)
	})
}

// MuxSocket bounds a mux session's socket; see notSentLowat.
func MuxSocket(c net.Conn) {
	if raw, ok := rawOf(c); ok {
		setNotSentLowat(raw)
	}
}

// Listen opens the TCP listener a websocket server serves on, rather than
// letting net/http open one; mux bounds every accepted socket (MuxSocket) for
// wsmux and wssmux.
func Listen(address string, mux bool) (net.Listener, error) {
	cfg := &net.ListenConfig{Control: func(_, _ string, s syscall.RawConn) error {
		if mux {
			setNotSentLowat(s)
		}
		return nil
	}}
	return cfg.Listen(context.Background(), "tcp", address)
}

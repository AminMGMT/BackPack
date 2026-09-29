// Package bridge joins two connections: the last step of both the direct and
// the layer-3 engines' port forwarding, which carried a copy each.
package bridge

import (
	"context"
	"io"
	"net"
)

// Join copies in both directions until either side is done, then closes both.
//
// The direction that finishes first half-closes what it was writing to, so
// that peer is sent an orderly end of stream before both connections are
// closed; Join does not wait for the other direction to drain.
func Join(ctx context.Context, a, b net.Conn) {
	done := make(chan struct{}, 2)
	copyOne := func(dst, src net.Conn) {
		defer func() { done <- struct{}{} }()
		_, _ = io.Copy(dst, src)
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}
	go copyOne(a, b)
	go copyOne(b, a)

	select {
	case <-done:
	case <-ctx.Done():
	}
	a.Close()
	b.Close()
	<-done // the second copy cannot outlive the closes above
}

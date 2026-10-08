// Package bridge joins two connections: the last step of both the direct and
// layer-3 engines' port forwarding.
package bridge

import (
	"context"
	"io"
	"net"

	"github.com/backpack/backpack/internal/metrics"
)

// Join copies both directions, preserving a TCP FIN until the response drains.
// Streams without directional EOF retain their existing full-close behavior.
func Join(ctx context.Context, a, b net.Conn) {
	done := make(chan struct{}, 2)
	aTCP, bTCP := plainTCP(a), plainTCP(b)
	copyOne := func(dst, src net.Conn, tcp *net.TCPConn) {
		defer func() { done <- struct{}{} }()
		_, err := io.Copy(dst, src)
		if err == nil && aTCP != nil && bTCP != nil && tcp.CloseWrite() == nil {
			return
		}
		a.Close()
		b.Close()
	}
	go copyOne(a, b, aTCP)
	go copyOne(b, a, bTCP)

	finished := 0
	for finished < 2 {
		select {
		case <-done:
			finished++
		case <-ctx.Done():
			a.Close()
			b.Close()
			for finished < 2 {
				<-done
				finished++
			}
		}
	}
	a.Close()
	b.Close()
}

// Unwrap only to send FIN; copying through the original wrappers keeps pacing
// and traffic accounting active in both directions.
func plainTCP(conn net.Conn) *net.TCPConn {
	for range 8 {
		if tcp, ok := conn.(*net.TCPConn); ok {
			return tcp
		}
		raw, counted := metrics.Uncount(conn)
		if counted {
			conn = raw
			continue
		}
		if wrapper, ok := conn.(interface{ UnderlyingConn() net.Conn }); ok {
			conn = wrapper.UnderlyingConn()
			continue
		}
		return nil
	}
	return nil
}

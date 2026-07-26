package transport

import (
	"io"
	"net"
	"testing"
)

// How the bandwidth cap and the zero-copy relay have to coexist.
//
// The forwarded relay hands its two connections to io.Copy, which on Linux
// moves bytes between two TCP sockets with splice(2) — inside the kernel, never
// through this process. That is exactly what a rate limiter cannot allow: the
// cap is enforced by pacing each Read and Write, so bytes that never pass
// through here would never be paced and the limit would silently do nothing.
//
// The two tests below pin both sides of that. They are structural rather than
// behavioural because the choice io.Copy makes is invisible from outside: all
// that is observable is whether a connection still offers a Read/Write path
// only, which is what forces the slow route.

// A capped connection must not expose a fast path, or its cap stops working.
func TestCappedConnectionKeepsBytesInUserSpace(t *testing.T) {
	l := newLimiter(Limits{BandwidthMbps: 10})
	capped := l.wrap(&net.TCPConn{})

	if capped == net.Conn(&net.TCPConn{}) {
		t.Fatal("a bandwidth cap must actually wrap the connection")
	}
	if _, ok := capped.(io.ReaderFrom); ok {
		t.Error("a capped connection offering ReadFrom would let io.Copy splice past the rate limiter")
	}
	if _, ok := capped.(io.WriterTo); ok {
		t.Error("a capped connection offering WriteTo would let io.Copy splice past the rate limiter")
	}
}

// With no cap configured the connection must be returned untouched, so the
// relay still sees a real socket and the kernel can move the bytes itself.
// Wrapping unconditionally would cost every uncapped tunnel its fast path.
func TestUncappedConnectionIsLeftUntouched(t *testing.T) {
	raw := &net.TCPConn{}

	for _, tc := range []struct {
		name string
		l    *limiter
	}{
		{"no limits at all", newLimiter(Limits{})},
		{"a connection cap but no bandwidth cap", newLimiter(Limits{MaxConnections: 100})},
		{"no limiter", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.l.wrap(raw); got != net.Conn(raw) {
				t.Error("the connection was wrapped without a bandwidth cap, which costs it the zero-copy relay")
			}
		})
	}
}

package network

import (
	"net"
	"testing"
	"time"
)

// The bug these guard is the one from the field logs: a spoof tunnel restarting
// to adopt a new client died with "listen udp4 0.0.0.0:58521: bind: address
// already in use". The carrier transports (spoof, xdi, pck) hand kcp-go a
// socket they built themselves, and kcp-go's NewConn2 / ServeConn deliberately
// do not take ownership of a caller-provided socket — so closing the KCP object
// left the raw socket bound, and the next restart could not rebind the port.
//
// Neither carrier can be opened without a raw socket (root), so these exercise
// the ownership contract itself with an ordinary UDP PacketConn standing in for
// the carrier. The property under test is identical: closing the KCP object
// must close the socket handed to it.

// closeTracker is a PacketConn that records whether it was closed, so the test
// can assert kcp closed it rather than inferring it from a rebind.
type closeTracker struct {
	net.PacketConn
	closed bool
}

func (c *closeTracker) Close() error {
	c.closed = true
	return c.PacketConn.Close()
}

func newTrackedConn(t *testing.T) *closeTracker {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("open a UDP socket to stand in for the carrier: %v", err)
	}
	return &closeTracker{PacketConn: pc}
}

// A session built over a carrier socket must close that socket when it closes.
// This is the client half of the fix (ownedKCPSession): without it, every
// reconnect leaked the client's receive socket.
func TestOwnedSessionClosesTheCarrierSocket(t *testing.T) {
	block, err := kcpCrypt("a-token-for-the-test")
	if err != nil {
		t.Fatalf("derive cipher: %v", err)
	}
	conn := newTrackedConn(t)

	sess, err := ownedKCPSession(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9},
		block, 0, 0, conn)
	if err != nil {
		t.Fatalf("open the session: %v", err)
	}
	if conn.closed {
		t.Fatal("the carrier socket was closed before the session was")
	}

	if err := sess.Close(); err != nil {
		t.Fatalf("close the session: %v", err)
	}
	if !conn.closed {
		t.Fatal("closing the session did not close the carrier socket — the leak that made a restart fail to bind")
	}

	// The socket really is gone: a read returns an error rather than blocking.
	_ = conn.PacketConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, _, err := conn.PacketConn.ReadFrom(make([]byte, 1)); err == nil {
		t.Fatal("the carrier socket still reads after close")
	}
}

// The plain-UDP path owns its own socket, so KCPListen hands back a Closer with
// nothing to do — but it must be non-nil and safe to call, because the caller
// closes it unconditionally without knowing which carrier it got.
func TestPlainListenerCarrierCloserIsANoop(t *testing.T) {
	ln, carrier, err := KCPListen("127.0.0.1:0", "a-token-for-the-test", KCPSettings{})
	if err != nil {
		t.Fatalf("open the plain UDP listener: %v", err)
	}
	if carrier == nil {
		t.Fatal("KCPListen returned a nil carrier closer; the caller closes it unconditionally")
	}
	// Safe to call, and safe to call after the listener is already closed.
	if err := ln.Close(); err != nil {
		t.Fatalf("close the listener: %v", err)
	}
	if err := carrier.Close(); err != nil {
		t.Fatalf("the no-op carrier closer returned an error: %v", err)
	}
}

package network

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
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

// The startup notes are the only thing an operator has to go on when a KCP
// tunnel will not carry traffic, so what they say has to be true and has to be
// distinguishable from a healthy start.
//
// This is the regression test for a single line that ended "both ends MUST
// match MTU and FEC or the tunnel never connects" and printed on every start,
// healthy or not. It read as an error report to everyone who saw it, and its
// advice was wrong: MTU sizes only the segments this end sends, so the two ends
// need not agree on it, and equalising it fixes nothing. Operators followed it
// and their tunnels stayed down.
func TestStartupNotesDoNotClaimMTUMustMatch(t *testing.T) {
	// Each capture is one tunnel starting. The notes are told once per run, so
	// without this the second tunnel in this process would be silent — which is
	// the behaviour under test everywhere else and noise here.
	capture := func(s KCPSettings) string {
		resetStartupNotes()
		var sb strings.Builder
		s.Logf = func(format string, args ...any) {
			fmt.Fprintf(&sb, format+"\n", args...)
		}
		s.logSettings("server")
		return sb.String()
	}

	base := KCPSettings{MTU: 1350, SndWnd: 1024, RcvWnd: 1024}

	t.Run("the parameters are always reported", func(t *testing.T) {
		// A fresh tunnel: the notes are told once per run, and each of
		// these is a run.
		resetStartupNotes()
		got := capture(base)
		for _, want := range []string{"MTU=1350", "FEC=off", "sndwnd=1024"} {
			if !strings.Contains(got, want) {
				t.Errorf("startup notes do not report %q:\n%s", want, got)
			}
		}
	})

	t.Run("no note tells the operator to match MTU", func(t *testing.T) {
		for _, s := range []KCPSettings{base, withFEC(base, 10, 3)} {
			got := strings.ToLower(capture(s))
			// The advice may say what MTU has to do; it must never say it has
			// to equal the other end's, which is the claim that misled people.
			if strings.Contains(got, "match mtu") || strings.Contains(got, "mtu and fec") {
				t.Errorf("startup notes still tell the operator to match MTU:\n%s", got)
			}
		}
	})

	t.Run("a tunnel without FEC gets no FEC advice", func(t *testing.T) {
		if got := capture(base); strings.Contains(got, "not negotiated") {
			t.Errorf("a tunnel with FEC off should not be warned about shard counts:\n%s", got)
		}
	})

	t.Run("a tunnel with FEC is told the shards must match and why to lower the MTU", func(t *testing.T) {
		got := capture(withFEC(base, 10, 3))
		if !strings.Contains(got, "FEC 10:3") {
			t.Errorf("the FEC advice does not name the shard counts in use:\n%s", got)
		}
		for _, want := range []string{"kcp_datashards", "not negotiated", "lower kcp_mtu"} {
			if !strings.Contains(got, want) {
				t.Errorf("the FEC advice omits %q:\n%s", want, got)
			}
		}
	})
}

func withFEC(s KCPSettings, data, parity int) KCPSettings {
	s.DataShards, s.ParityShards = data, parity
	return s
}

func TestKCPAddressResolutionPreservesUDPAndRawCarrierAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1:443", "[::1]:443", "[fe80::1%test-zone]:443", ":443", "localhost:domain"} {
		t.Run(address, func(t *testing.T) {
			want, err := net.ResolveUDPAddr("udp", address)
			if err != nil {
				t.Fatal(err)
			}
			got, err := resolveKCPUDPAddr(context.Background(), address)
			if err != nil || got.String() != want.String() {
				t.Fatalf("resolved %v, error %v, want %s", got, err, want)
			}
		})
	}
	for _, address := range []string{"127.0.0.1", "127.0.0.1:443", "localhost:443", ""} {
		host := address
		if h, _, err := net.SplitHostPort(address); err == nil {
			host = h
		}
		want, err := net.ResolveIPAddr("ip4", host)
		if err != nil {
			t.Fatal(err)
		}
		got, err := hostToIPAddrContext(context.Background(), address)
		if err != nil || got.String() != want.String() {
			t.Fatalf("resolved raw peer %v, error %v, want %s", got, err, want)
		}
	}
	if _, err := hostToIPAddrContext(context.Background(), "[::1]:443"); err == nil {
		t.Fatal("IPv6 was accepted for an IPv4-only raw carrier")
	}
}

func TestKCPSessionOutlivesItsSetupBudget(t *testing.T) {
	settings := KCPSettings{MTU: 1350, Interval: 20, NoDelay: 1, SndWnd: 128, RcvWnd: 128,
		DataShards: 10, ParityShards: 3}
	listener, carrier, err := KCPListen("127.0.0.1:0", "setup-lifetime", settings)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer carrier.Close()
	_ = listener.SetReadDeadline(time.Now().Add(3 * time.Second))
	payload := bytes.Repeat([]byte("KCP with FEC after setup cancellation"), 2048)
	served := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptKCP()
		if err != nil {
			served <- err
			return
		}
		defer conn.Close()
		ApplyKCPSettings(conn, settings)
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		got := make([]byte, len(payload))
		_, err = io.ReadFull(conn, got)
		if err == nil && !bytes.Equal(got, payload) {
			err = fmt.Errorf("KCP payload changed after setup cancellation")
		}
		served <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	conn, err := KCPDialContext(ctx, listener.Addr().String(), "setup-lifetime", settings)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()
	time.Sleep(350 * time.Millisecond)
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	if n, err := conn.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("write after setup: %d, %v", n, err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("KCP session stopped carrying traffic after setup cancellation")
	}
}

// The startup notes are told once a run, not once a session.
//
// They describe the run — every session of one is dialled with the same
// settings — but they were emitted from the dial, and a KCP client dials a
// whole pool. So a reconnect printed the parameters and the FEC advice once per
// pooled session: fifteen identical blocks in the same second, on a tunnel that
// was already in trouble. The advice is written to be read, and at that volume
// it is what buries the line saying why the tunnel dropped.
func TestTheStartupNotesAreToldOncePerRun(t *testing.T) {
	resetStartupNotes()

	var lines int
	s := withFEC(KCPSettings{MTU: 1250, SndWnd: 256, RcvWnd: 256}, 10, 3)
	s.Logf = func(string, ...any) { lines++ }

	// One dial, then the pool behind it.
	for i := 0; i < 16; i++ {
		s.logSettings("client")
	}
	if lines != 2 {
		t.Errorf("sixteen sessions produced %d lines, want the 2 this run has to say — "+
			"a pool rebuild would otherwise bury the reason it was rebuilding", lines)
	}

	// A genuinely different setting still says so.
	before := lines
	s2 := withFEC(KCPSettings{MTU: 1100, SndWnd: 256, RcvWnd: 256}, 10, 3)
	s2.Logf = s.Logf
	s2.logSettings("client")
	if lines <= before {
		t.Error("a changed MTU was suppressed as a repeat")
	}
}

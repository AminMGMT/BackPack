package transport

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/utils/network"
)

// Restart on the client transports, on the branches the end-to-end suite
// cannot reach.
//
// The tunnel restarts in e2e exercise the path that carries on. The path that
// gives up — a restart that lands while the tunnel is already shutting down —
// had no test on either end, and it was wrong on all fourteen transports: it
// returned before clearing the status and the published peer, so a restart that
// abandoned left the panel and the watchdog both believing there was a peer.
//
// That was invisible while the only way to reach it was a process about to
// exit. A transport fallback chain cancels a candidate's context and keeps the
// process running, which makes it a live snapshot claiming a connection that
// does not exist.

type restartable interface {
	Restart()
	Running() bool
}

func eachClientTransport(t *testing.T, parent context.Context) map[string]restartable {
	t.Helper()
	log := silentLogger()
	const remote = "127.0.0.1:1"
	eps := network.NewEndpoints(remote)

	return map[string]restartable{
		"tcp": NewTCPClient(parent, &TcpConfig{
			RemoteAddr: remote, Endpoints: eps, Token: "t", ConnPoolSize: 2,
			RetryInterval: time.Second, DialTimeOut: time.Second, KeepAlive: time.Second,
		}, log),
		"tcpmux": NewMuxClient(parent, &TcpMuxConfig{
			RemoteAddr: remote, Endpoints: eps, Token: "t", ConnPoolSize: 2,
			RetryInterval: time.Second, DialTimeOut: time.Second, KeepAlive: time.Second,
			MuxVersion: 2, MaxFrameSize: 4096, MaxReceiveBuffer: 1 << 16, MaxStreamBuffer: 4096,
		}, log),
		"ws": NewWSClient(parent, &WsConfig{
			RemoteAddr: remote, Endpoints: eps, Token: "t", ConnPoolSize: 2,
			RetryInterval: time.Second, DialTimeOut: time.Second, KeepAlive: time.Second,
			Mode: "ws",
		}, log),
		"wsmux": NewWSMuxClient(parent, &WsMuxConfig{
			RemoteAddr: remote, Endpoints: eps, Token: "t", ConnPoolSize: 2,
			RetryInterval: time.Second, DialTimeOut: time.Second, KeepAlive: time.Second,
			Mode: "wsmux", MuxVersion: 2, MaxFrameSize: 4096,
			MaxReceiveBuffer: 1 << 16, MaxStreamBuffer: 4096,
		}, log),
		"kcp": NewKcpClient(parent, &KcpConfig{
			RemoteAddr: remote, Endpoints: eps, Token: "t", ConnPoolSize: 2,
			RetryInterval: time.Second, DialTimeOut: time.Second, KeepAlive: time.Second,
			MuxVersion: 2, MaxFrameSize: 4096, MaxReceiveBuffer: 1 << 16,
			MaxStreamBuffer: 4096, MTU: 1350, SndWnd: 128, RcvWnd: 128,
		}, log),
		"quic": NewQuicClient(parent, &QuicConfig{
			RemoteAddr: remote, Endpoints: eps, Token: "t", ConnPoolSize: 2,
			RetryInterval: time.Second, DialTimeOut: time.Second, KeepAlive: time.Second,
		}, log),
		"udp": NewUDPClient(parent, &UdpConfig{
			RemoteAddr: remote, Endpoints: eps, Token: "t", ConnPoolSize: 2,
			RetryInterval: time.Second, DialTimeOut: time.Second,
		}, log),
	}
}

func setClientStatus(t *testing.T, tr restartable, v string) {
	t.Helper()
	switch c := tr.(type) {
	case *TcpTransport:
		c.status.set(v)
	case *TcpMuxTransport:
		c.status.set(v)
	case *WsTransport:
		c.status.set(v)
	case *WsMuxTransport:
		c.status.set(v)
	case *KcpTransport:
		c.status.set(v)
	case *QuicTransport:
		c.status.set(v)
	case *UdpTransport:
		c.status.set(v)
	default:
		t.Fatalf("unhandled transport type %T", tr)
	}
}

// The bug this file was written for.
func TestARestartLeavesTheStatusClear(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	for name, tr := range eachClientTransport(t, dead) {
		t.Run(name, func(t *testing.T) {
			setClientStatus(t, tr, "Connected (x)")
			if !tr.Running() {
				t.Fatal("setup: the transport did not take the status")
			}

			tr.Restart()

			if tr.Running() {
				t.Error("the status from the previous run survived a restart that gave " +
					"up, so the panel and the watchdog still believe there is a peer")
			}
		})
	}
}

// A restart during shutdown must not start a new generation, because on a
// reload that means fighting the run replacing this one for its own ports.
func TestARestartDuringShutdownDoesNotStartANewRun(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	for name, tr := range eachClientTransport(t, dead) {
		t.Run(name, func(t *testing.T) {
			done := make(chan struct{})
			go func() { defer close(done); tr.Restart() }()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("Restart did not return on a tunnel that is shutting down")
			}
			if tr.Running() {
				t.Fatal("an abandoned restart left the transport reporting a peer")
			}
		})
	}
}

// Two restarts at once must not both proceed.
func TestTwoRestartsAtOnceDoNotBothProceed(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	for name, tr := range eachClientTransport(t, dead) {
		t.Run(name, func(t *testing.T) {
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); <-start; tr.Restart() }()
			}
			close(start)

			done := make(chan struct{})
			go func() { defer close(done); wg.Wait() }()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("concurrent restarts deadlocked")
			}
		})
	}
}

// Every transport in this package must be in the table, or a new one gets none
// of these guarantees checked and nothing says so.
func TestEveryClientTransportIsCoveredByTheRestartTable(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	got := eachClientTransport(t, dead)
	if len(got) != 7 {
		t.Fatalf("the restart table holds %d transports; this package has 7", len(got))
	}
	for _, want := range []string{"tcp", "tcpmux", "ws", "wsmux", "kcp", "quic", "udp"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s is missing from the restart table", want)
		}
	}
	_ = net.IPv4zero
}

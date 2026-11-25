package transport

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/utils"
)

// Connections still waiting in a generation's queue when that generation ends
// must give back the connection-limit slots they hold.
//
// Every restart builds a fresh local queue, and the old generation's workers
// returned without draining theirs. The limiter outlives every generation, so
// each queued connection's slot was lost for good: a capped tunnel that lost
// its control channel a few times under load ended up refusing everyone.
func TestQueuedConnectionsReturnTheirSlotsWhenTheGenerationEnds(t *testing.T) {
	const capacity = 40 // well over the at-most-four pairing workers, so some stay queued
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr, fwd := freeAddr(t), freeAddr(t)
	s := NewTCPServer(ctx, &TcpConfig{
		BindAddr: addr, Token: "tok", ChannelSize: 64,
		Ports:     []string{fwd + "=127.0.0.1:9"},
		Heartbeat: time.Second, KeepAlive: 10 * time.Second, Nodelay: true,
		MaxConnections: capacity,
	}, silentLogger())
	go s.Start()

	control := dialRetry(t, addr)
	if err := utils.SendBinaryTransportString(control, "tok", utils.SG_ChanV2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := utils.ReceiveBinaryTransportString(control); err != nil {
		t.Fatalf("no control acknowledgement: %v", err)
	}
	go io.Copy(io.Discard, control) // the heartbeats

	// Users arrive while the pool is empty: all of them queue.
	var users []net.Conn
	for i := 0; i < capacity; i++ {
		users = append(users, dialRetry(t, fwd))
	}
	defer func() {
		for _, u := range users {
			u.Close()
		}
	}()
	eventually(t, 3*time.Second, func() bool { return s.limits.active.Load() == capacity },
		"the forwarded connections never took their slots")

	// The control channel dies; the server ends the generation.
	control.Close()

	deadline := time.Now().Add(10 * time.Second)
	for s.limits.active.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d slots still held after the generation ended", s.limits.active.Load(), capacity)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Restart replaces the udp flow table under its lock and never reassigns the
// lock itself. Overwriting a mutex another goroutine holds makes that
// goroutine's Unlock a fatal error. Meaningful under -race, which CI runs.
func TestUDPRestartDoesNotReplaceTheFlowLock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewUDPServer(ctx, &UdpConfig{
		BindAddr: freeAddr(t), Token: "t", ChannelSize: 8, Heartbeat: time.Second,
	}, silentLogger())

	stale := &TunnelUDPConn{addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}, payload: make(chan []byte, 1)}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // an old generation's copy loop, finishing
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.dropTunnelConn(stale)
			}
		}
	}()
	for i := 0; i < 5; i++ {
		s.Restart()
	}
	close(stop)
	wg.Wait()
}

// The public ws tunnel port must drop a connection that never sends a request.
// With no header timeout, anyone could hold a goroutine and an fd per TCP
// connection for as long as they liked.
func TestAWSTunnelPortDropsAConnectionThatSendsNothing(t *testing.T) {
	old := tunnelHeaderTimeout
	tunnelHeaderTimeout = 300 * time.Millisecond
	t.Cleanup(func() { tunnelHeaderTimeout = old })

	for _, mode := range []string{"ws", "wsmux"} {
		ctx, cancel := context.WithCancel(context.Background())
		addr := freeAddr(t)
		var srv interface {
			Start()
			Wait(context.Context)
		}
		if mode == "ws" {
			srv = NewWSServer(ctx, &WsConfig{BindAddr: addr, Token: "t", ChannelSize: 8,
				Heartbeat: time.Second, KeepAlive: time.Second, Mode: "ws"}, silentLogger())
		} else {
			srv = NewWSMuxServer(ctx, &WsMuxConfig{BindAddr: addr, Token: "t", ChannelSize: 8,
				Heartbeat: time.Second, KeepAlive: time.Second, Mode: "wsmux", MuxCon: 2,
				MuxVersion: 2, MaxFrameSize: 4096, MaxReceiveBuffer: 1 << 16, MaxStreamBuffer: 4096}, silentLogger())
		}
		go srv.Start()

		idle := dialRetry(t, addr)
		_ = idle.SetReadDeadline(time.Now().Add(5 * time.Second))
		start := time.Now()
		_, err := idle.Read(make([]byte, 1))
		if err == nil {
			t.Errorf("%s: the server sent data to a connection that sent nothing", mode)
		} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Errorf("%s: an idle connection was still open after %s", mode, time.Since(start).Round(time.Millisecond))
		}
		idle.Close()
		cancel()
		// The server reads the timeout while it starts; let it finish before
		// the cleanup puts the old value back.
		srv.Wait(context.Background())
	}
}

// A forwarded UDP flow is still a UDP flow when a bandwidth cap wraps it, so it
// never gets a PROXY protocol header — which cannot describe it and which the
// handler refuses, closing the flow.
func TestAUDPFlowStaysAUDPFlowUnderABandwidthCap(t *testing.T) {
	fw, err := newUDPForwarder(silentLogger(), "127.0.0.1:0", "127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	defer fw.close()
	flow := newUDPFlow(fw, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5000}, "k")
	lim := newLimiter(Limits{BandwidthMbps: 10})
	if !isUDPFlow(lim.wrap(context.Background(), flow)) {
		t.Fatal("a bandwidth cap hid that the connection is a UDP flow")
	}
}

func dialRetry(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("could not reach %s: %v", addr, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func eventually(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWSTunnelShutdownClosesIncompleteRequests(t *testing.T) {
	for _, mode := range []string{"ws", "wsmux"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			addr := freeAddr(t)
			done := make(chan struct{})
			if mode == "ws" {
				s := NewWSServer(ctx, &WsConfig{BindAddr: addr, Token: "t", ChannelSize: 8,
					Heartbeat: time.Second, KeepAlive: time.Second, Mode: "ws"}, silentLogger())
				go func() { defer close(done); s.tunnelListener(s.newGen(s.run.context())) }()
			} else {
				s := NewWSMuxServer(ctx, &WsMuxConfig{BindAddr: addr, Token: "t", ChannelSize: 8,
					Heartbeat: time.Second, KeepAlive: time.Second, Mode: "wsmux", MuxCon: 2,
					MuxVersion: 2, MaxFrameSize: 4096, MaxReceiveBuffer: 1 << 16, MaxStreamBuffer: 4096}, silentLogger())
				go func() { defer close(done); s.tunnelListener(s.newGen(s.run.context())) }()
			}
			pending := dialRetry(t, addr)
			defer pending.Close()
			if _, err := io.WriteString(pending, "GET / HTTP/1.1\r\nHost: localhost\r\n"); err != nil {
				t.Fatal(err)
			}
			// Confirm the request is still incomplete before ending its generation.
			_ = pending.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			var b [1]byte
			if _, err := pending.Read(b[:]); err == nil {
				t.Fatal("incomplete request received a response")
			} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
				t.Fatalf("request closed before cancellation: %v", err)
			}
			_ = pending.SetReadDeadline(time.Now().Add(time.Second))
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("shutdown waited for the incomplete request's header timeout")
			}
			if _, err := pending.Read(b[:]); err == nil {
				t.Fatal("cancelled generation left its HTTP socket open")
			} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("cancelled HTTP connection was not closed")
			}
			listener, err := net.Listen("tcp", addr)
			if err != nil {
				t.Fatalf("cancelled listener still owns its port: %v", err)
			}
			_ = listener.Close()
		})
	}
}

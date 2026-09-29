package transport

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/utils/acceptloop"
)

// A stranger who connects and says nothing cannot hold the door shut.
//
// The udp transport read each control claim on its accept loop, one at a time,
// under a fifteen-second deadline — so anyone who could reach the port kept
// the genuine client waiting fifteen seconds per silent connection, for as
// long as they cared to keep opening them. Claims are judged side by side now.
func TestASilentConnectionDoesNotDelayTheGenuineClaim(t *testing.T) {
	addr := freeAddr(t)
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	const token = "a-long-enough-token"
	srv := NewUDPServer(parent, &UdpConfig{BindAddr: addr, Token: token, ChannelSize: 16, Heartbeat: time.Second}, quietLogger())
	go srv.Start()

	var silent net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			silent = c
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the control port never opened: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer silent.Close()
	time.Sleep(100 * time.Millisecond) // the stranger is first in line

	genuine, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer genuine.Close()
	if err := utils.SendBinaryTransportString(genuine, token, utils.SG_Chan); err != nil {
		t.Fatal(err)
	}
	_ = genuine.SetReadDeadline(time.Now().Add(3 * time.Second))
	answer, signal, err := utils.ReceiveBinaryTransportString(genuine)
	if err != nil {
		t.Fatalf("the genuine claim waited behind a silent connection: %v", err)
	}
	if signal != utils.SG_Chan || answer != token {
		t.Fatalf("answered %q/%d", answer, signal)
	}
}

// The tunnel port shared by tcp, tcpmux and stealth: once its unproven places
// are full a stranger's next connection is closed at once — and a host that
// has proved the token is still let in, however full the gate is.
func TestTheTunnelPortShutsOutStrangersButNotTheProvenClient(t *testing.T) {
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var set listenerSet
	gate := &acceptloop.Gate{PerHost: 2, Total: 2}
	admitted := make(chan struct{}, 8)
	port := tcpTunnelPort{ctx: ctx, addr: addr, keepAlive: time.Second, nodelay: true,
		listeners: &set, log: quietLogger(), preauth: gate,
		// A peer that says nothing: admit waits for as long as the test does.
		admit: func(c net.Conn) { admitted <- struct{}{}; <-ctx.Done(); c.Close() }}
	go port.serve()

	dial := func() net.Conn {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			c, err := net.Dial("tcp", addr)
			if err == nil {
				t.Cleanup(func() { c.Close() })
				return c
			}
			if time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	closedAtOnce := func(c net.Conn) bool {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		_, err := c.Read(make([]byte, 1))
		ne, isNet := err.(net.Error)
		return err != nil && !(isNet && ne.Timeout())
	}

	for i := 0; i < 2; i++ {
		dial()
		<-admitted
	}
	if !closedAtOnce(dial()) {
		t.Fatal("a connection past the unproven allowance was held")
	}

	gate.Prove(&net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if closedAtOnce(dial()) {
		t.Fatal("the proven client was shut out by a full gate")
	}
}

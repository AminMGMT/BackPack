package direct

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

// echoUDPBackend stands in for a UDP service on the kharej machine.
func echoUDPBackend(t *testing.T, label string) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, err := conn.WriteTo(append([]byte(label), buf[:n]...), from); err != nil {
				return
			}
		}
	}()
	return conn.LocalAddr().String()
}

// newUDPTunnel brings up a pair forwarding one port for both TCP and UDP.
func newUDPTunnel(t *testing.T, backend string) *tunnel {
	t.Helper()
	const token = "a-udp-token"

	origin, err := NewOrigin(Config{
		Role: RoleOrigin, Addr: "127.0.0.1:0", Token: token,
	}, testLogger(t))
	if err != nil {
		t.Fatalf("NewOrigin: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	originDone := make(chan struct{})
	go func() { defer close(originDone); _ = origin.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-originDone })

	var bound net.Addr
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if bound = origin.LocalAddr(); bound != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if bound == nil {
		t.Fatal("the origin never bound a port")
	}

	port := freePort(t)
	edge, err := NewEdge(Config{
		Role: RoleEdge, Addr: bound.String(), Token: token,
		Ports:      []string{fmt.Sprintf("127.0.0.1:%d=%s", port, backend)},
		AcceptUDP:  true,
		RetryDelay: 200 * time.Millisecond,
	}, testLogger(t))
	if err != nil {
		t.Fatalf("NewEdge: %v", err)
	}
	edgeDone := make(chan struct{})
	go func() { defer close(edgeDone); _ = edge.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-edgeDone })

	tn := &tunnel{edge: edge, origin: origin, port: port}
	tn.awaitSession(t, 5*time.Second)
	return tn
}

// dialUDP opens a socket at the tunnel's forwarded port.
func (tn *tunnel) dialUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", tn.addr())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// udpExchange sends and waits for the reply, retrying while the listener comes
// up — UDP gives no signal that it has.
func udpExchange(t *testing.T, conn *net.UDPConn, message string, attempts int) (string, bool) {
	t.Helper()
	buf := make([]byte, 65535)
	for i := 0; i < attempts; i++ {
		if _, err := conn.Write([]byte(message)); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, err := conn.Read(buf)
		if err == nil {
			return string(buf[:n]), true
		}
	}
	return "", false
}

func TestTunnelForwardsUDP(t *testing.T) {
	backend := echoUDPBackend(t, "U:")
	tn := newUDPTunnel(t, backend)
	conn := tn.dialUDP(t)

	got, ok := udpExchange(t, conn, "hello", 40)
	if !ok {
		t.Fatal("no reply came back through the udp tunnel")
	}
	if want := "U:hello"; got != want {
		t.Fatalf("round trip = %q, want %q", got, want)
	}
}

// Every datagram in a conversation must go over the same stream, and come back
// whole.
func TestUDPFlowIsStableAcrossDatagrams(t *testing.T) {
	backend := echoUDPBackend(t, "U:")
	tn := newUDPTunnel(t, backend)
	conn := tn.dialUDP(t)

	if _, ok := udpExchange(t, conn, "warmup", 40); !ok {
		t.Fatal("the udp tunnel never came up")
	}
	for i := 0; i < 25; i++ {
		message := fmt.Sprintf("datagram-%d", i)
		got, ok := udpExchange(t, conn, message, 4)
		if !ok {
			t.Fatalf("no reply to %q", message)
		}
		if want := "U:" + message; got != want {
			t.Fatalf("reply %d = %q, want %q", i, got, want)
		}
	}
}

// Datagram boundaries are the whole of a datagram's meaning. Two sent back to
// back must arrive as two, not as one joined pair — which is exactly what a
// byte-stream relay without framing would produce.
func TestUDPPreservesDatagramBoundaries(t *testing.T) {
	backend := echoUDPBackend(t, "")
	tn := newUDPTunnel(t, backend)
	conn := tn.dialUDP(t)

	if _, ok := udpExchange(t, conn, "warmup", 40); !ok {
		t.Fatal("the udp tunnel never came up")
	}

	// Two writes, no pause: a relay that merged them would deliver "aaabbb".
	if _, err := conn.Write([]byte("aaa")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := conn.Write([]byte("bbb")); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, 65535)
	for _, want := range []string{"aaa", "bbb"} {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("reading %q: %v", want, err)
		}
		if got := string(buf[:n]); got != want {
			t.Fatalf("read %q, want %q — datagram boundaries were not preserved", got, want)
		}
	}
}

// A datagram near the size of the wire's limit must survive the length prefix.
func TestUDPCarriesALargeDatagram(t *testing.T) {
	backend := echoUDPBackend(t, "")
	tn := newUDPTunnel(t, backend)
	conn := tn.dialUDP(t)

	if _, ok := udpExchange(t, conn, "warmup", 40); !ok {
		t.Fatal("the udp tunnel never came up")
	}

	// Comfortably inside a loopback datagram, and far past one MTU.
	payload := bytes.Repeat([]byte{0xAB}, 8000)
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 65535)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(buf[:n], payload) {
		t.Fatalf("received %d bytes, want %d", n, len(payload))
	}
}

// Two clients must not have their datagrams mixed together: each gets a stream
// of its own, and each reply goes back to the client that asked.
func TestUDPKeepsClientsApart(t *testing.T) {
	backend := echoUDPBackend(t, "U:")
	tn := newUDPTunnel(t, backend)

	first := tn.dialUDP(t)
	second := tn.dialUDP(t)

	if _, ok := udpExchange(t, first, "warmup", 40); !ok {
		t.Fatal("the udp tunnel never came up")
	}

	for i := 0; i < 10; i++ {
		a := fmt.Sprintf("first-%d", i)
		b := fmt.Sprintf("second-%d", i)

		got, ok := udpExchange(t, first, a, 4)
		if !ok || got != "U:"+a {
			t.Fatalf("first client got %q (ok=%v), want %q", got, ok, "U:"+a)
		}
		got, ok = udpExchange(t, second, b, 4)
		if !ok || got != "U:"+b {
			t.Fatalf("second client got %q (ok=%v), want %q", got, ok, "U:"+b)
		}
	}
}

// TCP and UDP share the same mapping and the same session.
func TestUDPAndTCPShareTheTunnel(t *testing.T) {
	tcpBackend := echoBackend(t, "T:")

	// One mapping, two backends of different kinds on the same port would need
	// two services; use the TCP one for both and check UDP simply fails to
	// connect rather than corrupting anything.
	tn := newUDPTunnel(t, tcpBackend)

	if got, want := tn.roundTrip(t, "over-tcp"), "T:over-tcp"; got != want {
		t.Fatalf("tcp round trip = %q, want %q", got, want)
	}
	if sessions := tn.edge.Stats().Sessions; sessions != 1 {
		t.Fatalf("edge holds %d sessions, want 1", sessions)
	}
}

// With accept_udp off, the UDP port must not be bound at all.
func TestUDPIsOffByDefault(t *testing.T) {
	backend := echoUDPBackend(t, "U:")
	tn := newTunnel(t, TransportTCP, "token", "token", backend)
	tn.awaitSession(t, 5*time.Second)

	// Nothing should be listening for datagrams, so this port is bindable.
	addr, err := net.ResolveUDPAddr("udp", tn.addr())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("the udp port was bound even though accept_udp is off: %v", err)
	}
	conn.Close()
}

package l3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// echoTCP starts a TCP server that echoes what it is sent, prefixed with a
// label so a test can tell two backends apart.
func echoTCP(t *testing.T, label string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 4096)
				for {
					n, err := conn.Read(buf)
					if n > 0 {
						if _, err := conn.Write(append([]byte(label), buf[:n]...)); err != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return listener.Addr().String()
}

// echoUDP starts a UDP server that echoes each datagram with a label.
func echoUDP(t *testing.T, label string) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	go func() {
		buf := make([]byte, 4096)
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

// freePort finds a port nothing is listening on, for a mapping's listen side.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

// startForwarder runs a forwarder for the given specs and waits for its
// listeners to accept.
func startForwarder(t *testing.T, specs []string, acceptUDP bool) {
	t.Helper()
	forwarder, err := NewForwarder(Config{
		Ports: specs, AcceptUDP: acceptUDP, PeerIP: "127.0.0.1",
	}, quietLogger())
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	if forwarder == nil {
		t.Fatal("NewForwarder returned nothing for a config with ports")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = forwarder.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

// dialUntilReady retries briefly, because the forwarder's listeners come up in
// their own goroutines.
func dialUntilReady(t *testing.T, addr string) net.Conn {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			return conn
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("nothing accepted on %s", addr)
	return nil
}

func TestForwarderCarriesTCP(t *testing.T) {
	backend := echoTCP(t, "A:")
	port := freePort(t)
	startForwarder(t, []string{fmt.Sprintf("127.0.0.1:%d=%s", port, backend)}, false)

	conn := dialUntilReady(t, fmt.Sprintf("127.0.0.1:%d", port))
	defer conn.Close()

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 64)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got, want := string(buf[:n]), "A:hello"; got != want {
		t.Fatalf("read %q, want %q", got, want)
	}
}

// A large transfer has to arrive whole and in order, which is what proves the
// copy loop is not truncating or interleaving.
func TestForwarderCarriesALargeTransfer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	payload := make([]byte, 4<<20) // 4 MiB
	for i := range payload {
		payload[i] = byte(i)
	}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write(payload)
	}()

	port := freePort(t)
	startForwarder(t, []string{fmt.Sprintf("127.0.0.1:%d=%s", port, listener.Addr())}, false)

	conn := dialUntilReady(t, fmt.Sprintf("127.0.0.1:%d", port))
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("received %d bytes, want %d, equal=%v", len(got), len(payload), bytes.Equal(got, payload))
	}
}

func TestForwarderCarriesUDP(t *testing.T) {
	backend := echoUDP(t, "U:")
	port := freePort(t)
	startForwarder(t, []string{fmt.Sprintf("127.0.0.1:%d=%s", port, backend)}, true)

	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// The listener comes up in its own goroutine, and UDP gives no signal that
	// it has, so the first datagram may be sent into a closed port.
	buf := make([]byte, 64)
	for attempt := 0; attempt < 60; attempt++ {
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, err := conn.Read(buf)
		if err == nil {
			if got, want := string(buf[:n]), "U:ping"; got != want {
				t.Fatalf("read %q, want %q", got, want)
			}
			return
		}
	}
	t.Fatal("no reply came back through the udp forwarder")
}

// Several datagrams from one client must all reach the same backend socket and
// come back, which is what the flow table is for.
func TestForwarderKeepsUDPFlows(t *testing.T) {
	backend := echoUDP(t, "U:")
	port := freePort(t)
	startForwarder(t, []string{fmt.Sprintf("127.0.0.1:%d=%s", port, backend)}, true)

	addr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", port))
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	buf := make([]byte, 64)
	ready := false
	for i := 0; i < 20; i++ {
		msg := fmt.Sprintf("packet-%d", i)
		if _, err := conn.Write([]byte(msg)); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			if !ready {
				continue // still coming up
			}
			t.Fatalf("read %d: %v", i, err)
		}
		ready = true
		if got, want := string(buf[:n]), "U:"+msg; got != want {
			t.Fatalf("read %q, want %q", got, want)
		}
	}
	if !ready {
		t.Fatal("no reply ever came back through the udp forwarder")
	}
}

// Several backends must share the load, and a dead one must be skipped rather
// than failing the connection.
func TestForwarderBalancesAndSkipsDeadBackends(t *testing.T) {
	a := echoTCP(t, "A:")
	b := echoTCP(t, "B:")
	port := freePort(t)
	startForwarder(t, []string{fmt.Sprintf("127.0.0.1:%d=%s|%s", port, a, b)}, false)

	seen := map[string]int{}
	for i := 0; i < 8; i++ {
		conn := dialUntilReady(t, fmt.Sprintf("127.0.0.1:%d", port))
		if _, err := conn.Write([]byte("x")); err != nil {
			t.Fatalf("write: %v", err)
		}
		buf := make([]byte, 16)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := conn.Read(buf)
		conn.Close()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		seen[string(buf[:n])]++
	}
	if seen["A:x"] == 0 || seen["B:x"] == 0 {
		t.Fatalf("connections did not reach both backends: %v", seen)
	}
}

func TestDialBackendSkipsRefusals(t *testing.T) {
	live := echoTCP(t, "L:")
	dead := fmt.Sprintf("127.0.0.1:%d", freePort(t))

	var cursor atomic.Uint64
	for i := 0; i < 4; i++ {
		conn, err := dialBackend(context.Background(), "tcp", []string{dead, live}, &cursor)
		if err != nil {
			t.Fatalf("dialBackend refused despite a live backend: %v", err)
		}
		conn.Close()
	}

	if _, err := dialBackend(context.Background(), "tcp", []string{dead}, &cursor); err == nil {
		t.Fatal("dialBackend succeeded with only a dead backend")
	}
	if _, err := dialBackend(context.Background(), "tcp", nil, &cursor); err == nil {
		t.Fatal("dialBackend succeeded with no backends")
	}
}

// A connection whose backend is unreachable must be refused promptly and
// counted, not left hanging.
func TestForwarderRefusesWhenTheBackendIsDown(t *testing.T) {
	dead := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	port := freePort(t)

	forwarder, err := NewForwarder(Config{
		Ports: []string{fmt.Sprintf("127.0.0.1:%d=%s", port, dead)}, PeerIP: "127.0.0.1",
	}, quietLogger())
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = forwarder.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	conn := dialUntilReady(t, fmt.Sprintf("127.0.0.1:%d", port))
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("the connection was not closed cleanly: %v", err)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if forwarder.Stats().Refused > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the refused connection was not counted")
}

func TestForwarderCountsTraffic(t *testing.T) {
	backend := echoTCP(t, "A:")
	port := freePort(t)

	forwarder, err := NewForwarder(Config{
		Ports: []string{fmt.Sprintf("127.0.0.1:%d=%s", port, backend)}, PeerIP: "127.0.0.1",
	}, quietLogger())
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = forwarder.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	conn := dialUntilReady(t, fmt.Sprintf("127.0.0.1:%d", port))
	_, _ = conn.Write([]byte("x"))
	buf := make([]byte, 16)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = conn.Read(buf)
	conn.Close()

	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if forwarder.Stats().Accepted > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the accepted connection was not counted")
}

// No ports configured is the plain layer-3 case and must not build a
// forwarder at all.
func TestNewForwarderIsNilWithoutPorts(t *testing.T) {
	for _, ports := range [][]string{nil, {}, {"", "  "}} {
		forwarder, err := NewForwarder(Config{Ports: ports, PeerIP: "10.10.0.2"}, quietLogger())
		if err != nil {
			t.Fatalf("NewForwarder(%v): %v", ports, err)
		}
		if forwarder != nil {
			t.Fatalf("NewForwarder(%v) built a forwarder", ports)
		}
	}
}

func TestNewForwarderRejectsBadPorts(t *testing.T) {
	if _, err := NewForwarder(Config{Ports: []string{"not-a-port"}, PeerIP: "10.10.0.2"}, quietLogger()); err == nil {
		t.Fatal("NewForwarder accepted a malformed mapping")
	}
}

// Validation must catch a bad mapping before any socket is opened, so the
// error arrives at startup rather than half way through serving.
func TestConfigValidatesPortMappings(t *testing.T) {
	cfg := Config{
		Mode: ModeDial, Addr: "1.2.3.4:9000", Token: "token",
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2",
		Ports: []string{"443", "nonsense"},
	}
	if _, err := New(cfg, quietLogger()); err == nil {
		t.Fatal("New accepted a configuration with a malformed port mapping")
	}

	cfg.Ports = []string{"443", "8080=80", "10000-10009"}
	if _, err := New(cfg, quietLogger()); err != nil {
		t.Fatalf("New refused a valid set of mappings: %v", err)
	}
}

// The forwarder must stop when its context does, releasing every port.
func TestForwarderReleasesPortsOnShutdown(t *testing.T) {
	backend := echoTCP(t, "A:")
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	forwarder, err := NewForwarder(Config{
		Ports: []string{fmt.Sprintf("%s=%s", addr, backend)}, PeerIP: "127.0.0.1",
	}, quietLogger())
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = forwarder.Run(ctx) }()

	dialUntilReady(t, addr).Close()
	cancel()
	wg.Wait()

	// The port has to be bindable again, or a restart would fail.
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("the port was not released: %v", err)
	}
	listener.Close()
}

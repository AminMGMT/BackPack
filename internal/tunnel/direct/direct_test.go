package direct

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/testport"
	"github.com/sirupsen/logrus"
)

func quietLogger() *logrus.Logger {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return log
}

// capturedLog holds what a tunnel said, for a test to print if it fails.
//
// The engine reports a port it could not bind, and every one of these tests
// used to throw that away. What CI showed instead was "the udp tunnel never
// came up" twenty seconds later, with nothing to say why — and a failure whose
// cause is not in its own output is one that gets answered by raising a
// timeout, which is not an answer.
type capturedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *capturedLog) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *capturedLog) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// testLogger keeps the tunnel's own account of itself and prints it only when
// the test has failed, so a passing run stays quiet.
func testLogger(t *testing.T) *logrus.Logger {
	log, _ := testLoggerCapturing(t)
	return log
}

// testLoggerCapturing is testLogger for a test that wants to read what was
// logged rather than only have it printed on failure.
func testLoggerCapturing(t *testing.T) (*logrus.Logger, *capturedLog) {
	t.Helper()
	captured := &capturedLog{}
	log := logrus.New()
	log.SetOutput(captured)
	t.Cleanup(func() {
		if t.Failed() {
			if out := captured.String(); out != "" {
				t.Logf("tunnel log:\n%s", out)
			}
		}
	})
	return log, captured
}

// echoBackend stands in for the real service on the kharej machine. It echoes
// what it is sent behind a label, so a test can tell two backends apart.
func echoBackend(t *testing.T, label string) string {
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
				buf := make([]byte, 32768)
				for {
					n, err := conn.Read(buf)
					if n > 0 {
						if _, werr := conn.Write(append([]byte(label), buf[:n]...)); werr != nil {
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

// freePort draws from the shared allocator, which exists because the obvious
// way to pick a test port is wrong in two ways and this package hit both. See
// internal/testport.
func freePort(t *testing.T) int {
	t.Helper()
	return testport.Free(t)
}

func portFree(port int) bool { return testport.IsFree(port) }

// tunnel is a running edge/origin pair on loopback.
type tunnel struct {
	edge   *Edge
	origin *Origin
	port   int // the edge's forwarded port
}

// newTunnel brings up an origin and an edge forwarding one port to backend.
func newTunnel(t *testing.T, transport, edgeToken, originToken, backend string) *tunnel {
	t.Helper()

	origin, err := NewOrigin(Config{
		Role: RoleOrigin, Addr: "127.0.0.1:0", Token: originToken, Transport: transport,
	}, quietLogger())
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
		Role: RoleEdge, Addr: bound.String(), Token: edgeToken, Transport: transport,
		Ports:      []string{fmt.Sprintf("127.0.0.1:%d=%s", port, backend)},
		RetryDelay: 200 * time.Millisecond,
	}, quietLogger())
	if err != nil {
		t.Fatalf("NewEdge: %v", err)
	}

	edgeDone := make(chan struct{})
	go func() { defer close(edgeDone); _ = edge.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-edgeDone })

	return &tunnel{edge: edge, origin: origin, port: port}
}

// awaitBind waits for the origin to report the port it actually got, which is
// how a test using port 0 finds out where to dial.
func awaitBind(t *testing.T, origin *Origin) net.Addr {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if addr := origin.LocalAddr(); addr != nil {
			return addr
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the origin never bound a port")
	return nil
}

// awaitSession waits for the edge to have a live session.
func (tn *tunnel) awaitSession(t *testing.T, within time.Duration) {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); {
		if tn.edge.Stats().Sessions > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no session was established within %s", within)
}

func (tn *tunnel) addr() string { return fmt.Sprintf("127.0.0.1:%d", tn.port) }

// roundTrip sends a message through the forwarded port and returns the reply.
func (tn *tunnel) roundTrip(t *testing.T, message string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", tn.addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", tn.addr(), err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(message)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(buf[:n])
}

// allTransports is every transport a direct tunnel can run on. Each one is
// only a different way of opening the connection; everything above it — the
// token proof, the mux, the streams — is identical, and the tests below hold
// all four to the same behaviour.
var allTransports = []string{TransportTCP, TransportStealth, TransportWS, TransportWSS}

// The whole point: a user reaching a port on the Iran side gets the service on
// the kharej side, with the tunnel dialled the other way round.
func TestTunnelForwardsAPort(t *testing.T) {
	for _, transport := range allTransports {
		t.Run(transport, func(t *testing.T) {
			backend := echoBackend(t, "S:")
			tn := newTunnel(t, transport, "shared-token", "shared-token", backend)
			tn.awaitSession(t, 5*time.Second)

			if got, want := tn.roundTrip(t, "hello"), "S:hello"; got != want {
				t.Fatalf("round trip = %q, want %q", got, want)
			}
			// The session is reused: a second connection is another stream on
			// it, not another dial.
			if got, want := tn.roundTrip(t, "again"), "S:again"; got != want {
				t.Fatalf("second round trip = %q, want %q", got, want)
			}
			if sessions := tn.edge.Stats().Sessions; sessions != 1 {
				t.Fatalf("edge holds %d sessions, want 1", sessions)
			}
		})
	}
}

// A large transfer must arrive whole and in order through the mux.
func TestTunnelCarriesALargeTransfer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	payload := make([]byte, 8<<20) // 8 MiB
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write(payload)
	}()

	tn := newTunnel(t, TransportStealth, "token", "token", listener.Addr().String())
	tn.awaitSession(t, 5*time.Second)

	conn, err := net.DialTimeout("tcp", tn.addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))

	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("received %d bytes of %d, and they %s",
			len(got), len(payload), map[bool]string{true: "match", false: "differ"}[bytes.Equal(got, payload)])
	}
}

// Many connections at once must all be served, each on its own stream.
func TestTunnelServesConcurrentConnections(t *testing.T) {
	backend := echoBackend(t, "S:")
	tn := newTunnel(t, TransportTCP, "token", "token", backend)
	tn.awaitSession(t, 5*time.Second)

	const count = 64
	var wg sync.WaitGroup
	errs := make(chan error, count)

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", tn.addr(), 10*time.Second)
			if err != nil {
				errs <- fmt.Errorf("dial %d: %w", n, err)
				return
			}
			defer conn.Close()

			message := fmt.Sprintf("connection-%d", n)
			if _, err := conn.Write([]byte(message)); err != nil {
				errs <- fmt.Errorf("write %d: %w", n, err)
				return
			}
			buf := make([]byte, 128)
			_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
			read, err := conn.Read(buf)
			if err != nil {
				errs <- fmt.Errorf("read %d: %w", n, err)
				return
			}
			if got, want := string(buf[:read]), "S:"+message; got != want {
				errs <- fmt.Errorf("connection %d got %q, want %q", n, got, want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	if accepted := tn.edge.Stats().Accepted; accepted != count {
		t.Fatalf("edge accepted %d connections, want %d", accepted, count)
	}
}

// A token mismatch must establish nothing, on any transport. On stealth the
// Noise handshake fails first; on the others the proof exchange does.
func TestTunnelRejectsTheWrongToken(t *testing.T) {
	for _, transport := range allTransports {
		t.Run(transport, func(t *testing.T) {
			backend := echoBackend(t, "S:")
			tn := newTunnel(t, transport, "the-wrong-token", "the-right-token", backend)

			time.Sleep(1500 * time.Millisecond)

			if sessions := tn.edge.Stats().Sessions; sessions != 0 {
				t.Fatalf("the edge established %d sessions with the wrong token", sessions)
			}
			if rejected := tn.origin.Stats().Rejected; rejected == 0 {
				t.Fatal("the origin did not reject the handshake")
			}

			// The port is still bound, and refuses rather than hanging.
			conn, err := net.DialTimeout("tcp", tn.addr(), 3*time.Second)
			if err != nil {
				t.Fatalf("the forwarded port is not accepting: %v", err)
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.ReadAll(conn); err != nil {
				t.Fatalf("the connection was not closed cleanly: %v", err)
			}
			if refused := tn.edge.Stats().Refused; refused == 0 {
				t.Fatal("the refused connection was not counted")
			}
		})
	}
}

// A backend that is down must be reported as a failure the edge can act on,
// not as a stream that opens and immediately closes.
func TestTunnelRefusesWhenTheBackendIsDown(t *testing.T) {
	dead := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	tn := newTunnel(t, TransportTCP, "token", "token", dead)
	tn.awaitSession(t, 5*time.Second)

	conn, err := net.DialTimeout("tcp", tn.addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("the connection was not closed cleanly: %v", err)
	}

	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if tn.origin.Stats().Failed > 0 && tn.edge.Stats().Refused > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the failure was not recorded: origin=%+v edge=%+v",
		tn.origin.Stats(), tn.edge.Stats())
}

// The edge must reconnect on its own when the origin goes away and comes back.
func TestEdgeReconnectsAfterTheOriginRestarts(t *testing.T) {
	backend := echoBackend(t, "S:")

	// A fixed port, so the replacement origin can take the same one.
	originPort := freePort(t)
	originAddr := fmt.Sprintf("127.0.0.1:%d", originPort)

	startOrigin := func() (context.CancelFunc, chan struct{}) {
		origin, err := NewOrigin(Config{
			Role: RoleOrigin, Addr: originAddr, Token: "token",
		}, quietLogger())
		if err != nil {
			t.Fatalf("NewOrigin: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = origin.Run(ctx) }()
		return cancel, done
	}

	stopFirst, firstDone := startOrigin()

	port := freePort(t)
	edge, err := NewEdge(Config{
		Role: RoleEdge, Addr: originAddr, Token: "token",
		Ports:      []string{fmt.Sprintf("127.0.0.1:%d=%s", port, backend)},
		RetryDelay: 200 * time.Millisecond,
	}, quietLogger())
	if err != nil {
		t.Fatalf("NewEdge: %v", err)
	}
	edgeCtx, stopEdge := context.WithCancel(context.Background())
	edgeDone := make(chan struct{})
	go func() { defer close(edgeDone); _ = edge.Run(edgeCtx) }()
	t.Cleanup(func() { stopEdge(); <-edgeDone })

	tn := &tunnel{edge: edge, port: port}
	tn.awaitSession(t, 5*time.Second)
	if got, want := tn.roundTrip(t, "before"), "S:before"; got != want {
		t.Fatalf("before the restart: %q, want %q", got, want)
	}

	// Take the origin away and wait for the edge to notice.
	stopFirst()
	<-firstDone
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if edge.Stats().Sessions == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if edge.Stats().Sessions != 0 {
		t.Fatal("the edge did not notice the origin had gone")
	}

	// Bring it back on the same port; the edge should redial by itself.
	stopSecond, secondDone := startOrigin()
	t.Cleanup(func() { stopSecond(); <-secondDone })

	tn.awaitSession(t, 15*time.Second)
	if got, want := tn.roundTrip(t, "after"), "S:after"; got != want {
		t.Fatalf("after the restart: %q, want %q", got, want)
	}
}

// Several mux sessions must all be used, so a shaped single connection is not
// the whole tunnel.
func TestEdgeSpreadsAcrossSessions(t *testing.T) {
	backend := echoBackend(t, "S:")

	origin, err := NewOrigin(Config{
		Role: RoleOrigin, Addr: "127.0.0.1:0", Token: "token",
	}, quietLogger())
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

	port := freePort(t)
	edge, err := NewEdge(Config{
		Role: RoleEdge, Addr: bound.String(), Token: "token", Sessions: 4,
		Ports:      []string{fmt.Sprintf("127.0.0.1:%d=%s", port, backend)},
		RetryDelay: 200 * time.Millisecond,
	}, quietLogger())
	if err != nil {
		t.Fatalf("NewEdge: %v", err)
	}
	edgeDone := make(chan struct{})
	go func() { defer close(edgeDone); _ = edge.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-edgeDone })

	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if edge.Stats().Sessions == 4 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := edge.Stats().Sessions; got != 4 {
		t.Fatalf("edge holds %d sessions, want 4", got)
	}
	if got := origin.Stats().Sessions; got != 4 {
		t.Fatalf("origin holds %d sessions, want 4", got)
	}

	tn := &tunnel{edge: edge, port: port}
	for i := 0; i < 8; i++ {
		if got, want := tn.roundTrip(t, "x"), "S:x"; got != want {
			t.Fatalf("round trip %d = %q, want %q", i, got, want)
		}
	}
}

// A bare mapping targets the loopback of the far machine, which is where the
// real service lives.
func TestBareMappingTargetsTheOriginLoopback(t *testing.T) {
	cfg := Config{
		Role: RoleEdge, Addr: "1.2.3.4:9000", Token: "token",
		Ports: []string{"443"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	edge, err := NewEdge(cfg, quietLogger())
	if err != nil {
		t.Fatalf("NewEdge: %v", err)
	}
	if len(edge.mappings) != 1 {
		t.Fatalf("produced %d mappings, want 1", len(edge.mappings))
	}
	if got, want := edge.mappings[0].String(), ":443 -> 127.0.0.1:443"; got != want {
		t.Fatalf("mapping = %q, want %q", got, want)
	}
}

func TestValidateRejectsBadConfigurations(t *testing.T) {
	base := func() Config {
		return Config{
			Role: RoleEdge, Addr: "1.2.3.4:9000", Token: "token",
			Ports: []string{"443"},
		}
	}
	cases := map[string]func(*Config){
		"no role":                func(c *Config) { c.Role = "" },
		"unknown role":           func(c *Config) { c.Role = "middle" },
		"no address":             func(c *Config) { c.Addr = "" },
		"address without a port": func(c *Config) { c.Addr = "1.2.3.4" },
		"no token":               func(c *Config) { c.Token = "" },
		"unknown transport":      func(c *Config) { c.Transport = "carrier-pigeon" },
		"edge with no ports":     func(c *Config) { c.Ports = nil },
		"malformed mapping":      func(c *Config) { c.Ports = []string{"not-a-port"} },
		"too many sessions":      func(c *Config) { c.Sessions = 100 },
	}
	for name, mutate := range cases {
		cfg := base()
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s: the configuration was accepted", name)
		}
	}
}

// The origin needs no ports of its own: every target arrives on the stream
// that asks for it.
func TestOriginNeedsNoPorts(t *testing.T) {
	cfg := Config{Role: RoleOrigin, Addr: "0.0.0.0:9000", Token: "token"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("an origin with no ports was refused: %v", err)
	}
	if cfg.Transport != TransportTCP {
		t.Fatalf("transport defaulted to %q, want %q", cfg.Transport, TransportTCP)
	}
	if cfg.Sessions != 1 {
		t.Fatalf("sessions defaulted to %d, want 1", cfg.Sessions)
	}
}

// A connection cap has to be enforced where the users arrive, and refusing
// must not consume a slot — or the tunnel would close itself down.
func TestEdgeEnforcesConnectionCap(t *testing.T) {
	backend := echoBackend(t, "S:")

	origin, err := NewOrigin(Config{
		Role: RoleOrigin, Addr: "127.0.0.1:0", Token: "token",
	}, quietLogger())
	if err != nil {
		t.Fatalf("NewOrigin: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	originDone := make(chan struct{})
	go func() { defer close(originDone); _ = origin.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-originDone })

	port := freePort(t)
	edge, err := NewEdge(Config{
		Role: RoleEdge, Addr: awaitBind(t, origin).String(), Token: "token",
		Ports:          []string{fmt.Sprintf("127.0.0.1:%d=%s", port, backend)},
		MaxConnections: 2,
		RetryDelay:     200 * time.Millisecond,
	}, quietLogger())
	if err != nil {
		t.Fatalf("NewEdge: %v", err)
	}
	edgeDone := make(chan struct{})
	go func() { defer close(edgeDone); _ = edge.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-edgeDone })

	tn := &tunnel{edge: edge, origin: origin, port: port}
	tn.awaitSession(t, 5*time.Second)

	// Hold two connections open, which is the whole allowance.
	var held []net.Conn
	for i := 0; i < 2; i++ {
		conn, err := net.DialTimeout("tcp", tn.addr(), 5*time.Second)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		if _, err := conn.Write([]byte("hold")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		buf := make([]byte, 32)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Read(buf); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		held = append(held, conn)
	}
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()

	// A third must be accepted by the listener and then closed without data.
	third, err := net.DialTimeout("tcp", tn.addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial (third): %v", err)
	}
	defer third.Close()
	_ = third.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := third.Read(make([]byte, 32)); err == nil && n > 0 {
		t.Fatal("a third connection was served past a cap of two")
	}

	// Freeing one must let another through — a refusal that consumed a slot
	// would erode the cap to nothing.
	held[0].Close()
	held = held[1:]

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		conn, err := net.DialTimeout("tcp", tn.addr(), 2*time.Second)
		if err != nil {
			continue
		}
		_, _ = conn.Write([]byte("after"))
		buf := make([]byte, 32)
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		n, err := conn.Read(buf)
		conn.Close()
		if err == nil && string(buf[:n]) == "S:after" {
			return
		}
	}
	t.Fatal("the cap did not recover after a connection was freed")
}

package network

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// maxPayloadOn1280Path is what a UDP datagram may carry over a link whose MTU is
// 1280 bytes: the MTU less a 20-byte IPv4 header and an 8-byte UDP header. An
// IPv6 path is 20 bytes tighter still.
const maxPayloadOn1280Path = 1280 - 20 - 8

// narrowPath is a UDP relay that emulates a link with a fixed MTU: anything that
// would not fit is dropped silently, exactly as a router with no room for it
// does. It is the only way to reproduce the failure in a unit test, because the
// loopback interface has an MTU of 65536 and lets every packet size through.
type narrowPath struct {
	addr     *net.UDPAddr
	dropped  int
	mu       sync.Mutex
	conn     *net.UDPConn
	upstream *net.UDPConn
	done     chan struct{}
}

// newNarrowPath starts a relay in front of server that drops any datagram whose
// payload exceeds maxPayload.
func newNarrowPath(t *testing.T, server *net.UDPAddr, maxPayload int) *narrowPath {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("relay listen: %v", err)
	}
	upstream, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		conn.Close()
		t.Fatalf("relay upstream listen: %v", err)
	}

	p := &narrowPath{
		addr:     conn.LocalAddr().(*net.UDPAddr),
		conn:     conn,
		upstream: upstream,
		done:     make(chan struct{}),
	}
	t.Cleanup(p.close)

	var clientAddr *net.UDPAddr
	var clientMu sync.Mutex

	// client -> server
	go func() {
		buf := make([]byte, 65536)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			clientMu.Lock()
			clientAddr = from
			clientMu.Unlock()
			if !p.admit(n, maxPayload) {
				continue
			}
			_, _ = upstream.WriteToUDP(buf[:n], server)
		}
	}()

	// server -> client
	go func() {
		buf := make([]byte, 65536)
		for {
			n, _, err := upstream.ReadFromUDP(buf)
			if err != nil {
				return
			}
			clientMu.Lock()
			to := clientAddr
			clientMu.Unlock()
			if to == nil || !p.admit(n, maxPayload) {
				continue
			}
			_, _ = conn.WriteToUDP(buf[:n], to)
		}
	}()

	return p
}

// admit reports whether a datagram of n bytes fits, counting the ones that do
// not so a test can tell "the path was never narrow" from "the path was narrow
// and we coped".
func (p *narrowPath) admit(n, maxPayload int) bool {
	if n <= maxPayload {
		return true
	}
	p.mu.Lock()
	p.dropped++
	p.mu.Unlock()
	return false
}

func (p *narrowPath) dropCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dropped
}

func (p *narrowPath) close() {
	select {
	case <-p.done:
		return
	default:
	}
	close(p.done)
	p.conn.Close()
	p.upstream.Close()
}

// TestQUICCrossesA1280BytePath is the regression test for the failure the netns
// transport matrix found: quic-go's default first packet is 1280 bytes of
// payload, which is 1308 on the wire over IPv4, so every Initial packet is
// dropped by a 1280-byte path and the handshake never completes. 1280 is the
// ordinary MTU of an IPv6 tunnel and of a great many mobile routes, so this is
// not an exotic case.
func TestQUICCrossesA1280BytePath(t *testing.T) {
	settings := QUICSettings{
		KeepAlivePeriod: time.Second,
		MaxIdleTimeout:  10 * time.Second,
	}

	listener, err := QUICListen("127.0.0.1:0", settings)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	path := newNarrowPath(t, listener.Addr().(*net.UDPAddr), maxPayloadOn1280Path)

	const payload = "the tunnel carried this"
	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept(context.Background())
		if err != nil {
			served <- err
			return
		}
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			served <- err
			return
		}
		if _, err := stream.Read(make([]byte, 1)); err != nil {
			served <- err
			return
		}
		if _, err := io.WriteString(stream, payload); err != nil {
			served <- err
			return
		}
		served <- stream.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := QUICDial(ctx, path.addr.String(), settings)
	if err != nil {
		t.Fatalf("dial across a 1280-byte path: %v", err)
	}
	defer conn.CloseWithError(0, "")

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	// A stream only exists for the peer once something has been written on it,
	// and a zero-byte write puts nothing on the wire — so the client sends one
	// byte to open it and the server answers.
	if _, err := stream.Write([]byte{1}); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A stream read obeys its own deadline, not the dial context, so without
	// this a regression would hang the package instead of failing it.
	if err := stream.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}

	got, err := io.ReadAll(stream)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("read %q over a 1280-byte path, want %q", got, payload)
	}

	if err := <-served; err != nil {
		t.Fatalf("server side: %v", err)
	}

	// Nothing should have been dropped: the whole point of the fix is that
	// every packet now fits. A drop here means something still oversteps.
	if n := path.dropCount(); n != 0 {
		t.Fatalf("%d datagrams were too big for a 1280-byte path", n)
	}
}

// TestNarrowPathDropsOversizeDatagrams checks the emulation itself. Without it
// TestQUICCrossesA1280BytePath would pass just as happily against a relay that
// forwards everything, which is to say against no test at all.
func TestNarrowPathDropsOversizeDatagrams(t *testing.T) {
	echo, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echo.Close()

	arrived := make(chan int, 4)
	go func() {
		buf := make([]byte, 65536)
		for {
			n, _, err := echo.ReadFromUDP(buf)
			if err != nil {
				return
			}
			arrived <- n
		}
	}()

	path := newNarrowPath(t, echo.LocalAddr().(*net.UDPAddr), maxPayloadOn1280Path)

	client, err := net.DialUDP("udp", nil, path.addr)
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	defer client.Close()

	if _, err := client.Write(make([]byte, maxPayloadOn1280Path+1)); err != nil {
		t.Fatalf("write oversize: %v", err)
	}
	if _, err := client.Write(make([]byte, maxPayloadOn1280Path)); err != nil {
		t.Fatalf("write at the limit: %v", err)
	}

	select {
	case n := <-arrived:
		if n != maxPayloadOn1280Path {
			t.Fatalf("an oversize datagram of %d bytes crossed a %d-byte path", n, maxPayloadOn1280Path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the datagram that fits never arrived")
	}

	if path.dropCount() != 1 {
		t.Fatalf("the path dropped %d datagrams, want 1", path.dropCount())
	}
}

// TestQUICInitialPacketFitsTheNarrowestCommonPath states the budget in one
// place. A future quic-go upgrade that changes the default cannot silently
// widen it past what a 1280-byte IPv6 path carries.
func TestQUICInitialPacketFitsTheNarrowestCommonPath(t *testing.T) {
	const ipv6Budget = 1280 - 40 - 8
	if QUICInitialPacketSize > ipv6Budget {
		t.Fatalf("initial packet size %d exceeds the %d bytes a 1280-byte IPv6 path carries",
			QUICInitialPacketSize, ipv6Budget)
	}
	got := (QUICSettings{}).quicConfig().InitialPacketSize
	if got != QUICInitialPacketSize {
		t.Fatalf("quicConfig set InitialPacketSize to %d, want %d", got, QUICInitialPacketSize)
	}
}

func TestQUICDialDNSStopsWithContext(t *testing.T) {
	if kind := os.Getenv("BACKPACK_REVERSE_QUIC_DNS_AUDIT"); kind != "" {
		release := make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		defer unblock()
		started := make(chan struct{}, 1)
		net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return nil, errors.New("resolver released")
			}
		}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if kind == "timeout" {
			ctx, cancel = context.WithTimeout(ctx, 80*time.Millisecond)
			defer cancel()
		}
		done := make(chan error, 1)
		go func() {
			settings := QUICSettings{}
			if kind == "configured-timeout" {
				settings.DialTimeout = 80 * time.Millisecond
			}
			conn, err := QUICDial(ctx, "stalled-quic-dns.invalid:443", settings)
			if conn != nil {
				conn.CloseWithError(0, "test finished")
			}
			done <- err
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("DNS lookup never started")
		}
		if kind == "cancel" {
			cancel()
		}
		select {
		case err := <-done:
			want := context.Canceled
			if kind != "cancel" {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Fatalf("DNS returned %v, want %v", err, want)
			}
		case <-time.After(400 * time.Millisecond):
			unblock()
			<-done
			t.Fatal("QUIC DNS ignored parent cancellation or deadline")
		}
		return
	}
	// Isolate resolver replacement from other tests and their socket cleanup.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"cancel", "timeout", "configured-timeout"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestQUICDialDNSStopsWithContext$", "-test.timeout=6s")
			cmd.Env = append(os.Environ(), "BACKPACK_REVERSE_QUIC_DNS_AUDIT="+kind)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("isolated %s lookup: %v\n%s", kind, err, output)
			}
		})
	}
}

func TestQUICAddressResolutionPreservesLiteralAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1:443", "[::1]:443", "[fe80::1%test-zone]:443", ":443", "localhost:domain"} {
		t.Run(address, func(t *testing.T) {
			want, err := net.ResolveUDPAddr("udp", address)
			if err != nil {
				t.Fatal(err)
			}
			got, err := resolveQUICAddr(context.Background(), address)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != want.String() {
				t.Fatalf("resolved %s, want %s", got, want)
			}
		})
	}
}

func TestQUICConnectionOutlivesItsSetupBudget(t *testing.T) {
	listener, err := QUICListen("127.0.0.1:0", QUICSettings{})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverCtx, stopServer := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopServer()
	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept(serverCtx)
		if err != nil {
			served <- err
			return
		}
		defer conn.CloseWithError(0, "finished")
		stream, err := conn.AcceptStream(serverCtx)
		if err != nil {
			served <- err
			return
		}
		_ = stream.SetDeadline(time.Now().Add(2 * time.Second))
		var value [1]byte
		_, err = io.ReadFull(stream, value[:])
		served <- err
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := QUICDial(ctx, listener.Addr().String(), QUICSettings{DialTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseWithError(0, "finished")
	cancel()
	time.Sleep(350 * time.Millisecond)
	stream, err := conn.OpenStreamSync(serverCtx)
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := stream.Write([]byte{42}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-serverCtx.Done():
		t.Fatal("established connection did not survive setup cancellation")
	}
}

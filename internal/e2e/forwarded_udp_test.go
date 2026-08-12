package e2e

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/client"
	"github.com/backpack/backpack/internal/server"
)

// A forwarded port has to carry UDP as well as TCP, on every transport.
//
// It did not. Only the plain TCP transport could do it, only with accept_udp
// turned on, and that was off by default and undocumented — so a tunnel on
// tcpmux, ws, wss, wsmux, kcp or quic bound no UDP socket at all and the
// datagrams were refused by the kernel with nothing logged. Xray, 3x-ui and
// Shadowsocks users hit it constantly: the browser worked, UDP did not, and the
// usual workaround was to build a GRE tunnel underneath and run Backpack over
// that, which is a whole second tunnel to work around a missing listener.
//
// These run a real tunnel per transport and send a datagram through it.

// startUDPEcho is a UDP backend that echoes whatever it is sent.
//
// startUDPEchoBackend in udp_test.go does the same for the raw UDP transport;
// this one is separate only so the two files stay independent.
func startUDPEcho(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot start the udp echo backend: %v", err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], addr)
		}
	}()
	return pc.LocalAddr().String()
}

// startForwardingTunnel brings up both ends of a tunnel whose forwarded port
// points at backendAddr, and returns the entry address on the server side.
func startForwardingTunnel(t *testing.T, transport, backendAddr string) string {
	t.Helper()

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "forwarded-udp-token-0123456789ab"

	srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backendAddr, token)
	// UDP forwarding is opt-in — off unless asked for — so a test about
	// forwarded UDP has to turn it on, exactly as an operator who needs UDP now
	// does. Without this the server would carry TCP only and these tests would
	// be asserting against a port that never bound UDP.
	on := true
	srvCfg.AcceptUDP = &on
	cliCfg := baseClientConfig(transport, fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })

	srv := server.NewServer(srvCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); srv.Start() }()

	time.Sleep(300 * time.Millisecond)

	cli := client.NewClient(cliCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); cli.Start() }()

	return fmt.Sprintf("127.0.0.1:%d", entryPort)
}

// forwardedUDPTransports are the stream transports, all of which now carry
// datagrams on a forwarded port. The raw udp transport is not here: it forwards
// only UDP and has its own test.
var forwardedUDPTransports = []string{"tcp", "tcpmux", "ws", "wsmux", "kcp", "quic"}

func TestForwardedUDPWorksOnEveryTransport(t *testing.T) {
	for _, transport := range forwardedUDPTransports {
		t.Run(transport, func(t *testing.T) {
			backend := startUDPEcho(t)
			entry := startForwardingTunnel(t, transport, backend)

			payload := []byte("forwarded-udp-over-" + transport)
			deadline := time.Now().Add(tunnelReadyTimeout)
			var lastErr error
			for time.Now().Before(deadline) {
				if err := udpRoundTrip(entry, payload); err == nil {
					return
				} else {
					lastErr = err
				}
				time.Sleep(250 * time.Millisecond)
			}
			t.Fatalf("a forwarded port on %s never carried a datagram: %v", transport, lastErr)
		})
	}
}

// A datagram is a message, not a stream: what goes in as one packet has to come
// out as one packet of exactly that size. The framing is what guarantees it, and
// a large payload is where a framing mistake shows — it is the one that gets
// split across several reads on the tunnel and has to be put back together.
func TestForwardedUDPPreservesDatagramBoundaries(t *testing.T) {
	backend := startUDPEcho(t)
	entry := startForwardingTunnel(t, "tcpmux", backend)

	// 8 KB is the largest that can be sent as one datagram everywhere the tests
	// run — macOS caps a UDP send at net.inet.udp.maxdgram, 9216 by default,
	// and refuses anything larger before it reaches the tunnel at all. It is
	// well past the 1400-odd bytes where a frame first has to be reassembled
	// across reads, which is the thing being checked.
	sizes := []int{1, 64, 1400, 8000}
	deadline := time.Now().Add(tunnelReadyTimeout)
	for time.Now().Before(deadline) {
		if err := udpRoundTrip(entry, []byte("ready")); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	conn, err := net.Dial("udp", entry)
	if err != nil {
		t.Fatalf("cannot reach the forwarded port: %v", err)
	}
	defer conn.Close()

	for _, size := range sizes {
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte(i % 251)
		}
		if err := conn.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(payload); err != nil {
			t.Fatalf("%d byte datagram could not be sent: %v", size, err)
		}
		got := make([]byte, size+16)
		n, err := conn.Read(got)
		if err != nil {
			t.Fatalf("%d byte datagram never came back: %v", size, err)
		}
		if n != size {
			t.Fatalf("sent %d bytes as one datagram, got %d back — the boundary was lost", size, n)
		}
		for i := range payload {
			if got[i] != payload[i] {
				t.Fatalf("%d byte datagram came back corrupted at offset %d", size, i)
			}
		}
	}
}

// Several sources at once is the real case: an Xray inbound has a flow per
// client, each from its own port, and they must not be mixed up. Every reply
// has to go back to the socket that sent it — a NAT table keyed wrongly, or
// shared, would cross the wires and each peer would see the other's traffic.
func TestForwardedUDPKeepsSourcesApart(t *testing.T) {
	// A backend that answers with the sender's own port, so a crossed reply is
	// not merely wrong data but provably the other peer's.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot start the udp backend: %v", err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], addr)
		}
	}()

	entry := startForwardingTunnel(t, "tcpmux", pc.LocalAddr().String())
	deadline := time.Now().Add(tunnelReadyTimeout)
	for time.Now().Before(deadline) {
		if err := udpRoundTrip(entry, []byte("ready")); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	const peers = 8
	var wg sync.WaitGroup
	errs := make(chan error, peers)
	for i := 0; i < peers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each peer says something only it would say, several times, so a
			// reply landing on the wrong socket is caught rather than being
			// mistaken for one of its own.
			conn, err := net.Dial("udp", entry)
			if err != nil {
				errs <- err
				return
			}
			defer conn.Close()
			mine := fmt.Sprintf("peer-%02d", i)
			for round := 0; round < 5; round++ {
				msg := fmt.Sprintf("%s-round-%d", mine, round)
				if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					errs <- err
					return
				}
				if _, err := conn.Write([]byte(msg)); err != nil {
					errs <- fmt.Errorf("%s: %w", mine, err)
					return
				}
				buf := make([]byte, 128)
				n, err := conn.Read(buf)
				if err != nil {
					errs <- fmt.Errorf("%s: no reply: %w", mine, err)
					return
				}
				if string(buf[:n]) != msg {
					errs <- fmt.Errorf("%s received %q, which belongs to another flow", mine, buf[:n])
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// Turning it off has to mean off: the forwarded port keeps carrying TCP and
// binds no UDP socket, which is what a tunnel that predates this did.
func TestForwardedUDPCanBeTurnedOff(t *testing.T) {
	backend := startUDPEcho(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "forwarded-udp-off-0123456789abcd"

	srvCfg := baseServerConfig("tcpmux", tunnelPort, entryPort, backend, token)
	off := false
	srvCfg.AcceptUDP = &off
	cliCfg := baseClientConfig("tcpmux", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })

	srv := server.NewServer(srvCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); srv.Start() }()
	time.Sleep(300 * time.Millisecond)
	cli := client.NewClient(cliCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); cli.Start() }()
	time.Sleep(1500 * time.Millisecond)

	// Nothing should be listening on the UDP side of the forwarded port.
	pc, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", entryPort))
	if err != nil {
		t.Fatalf("accept_udp = false still bound the UDP side of the forwarded port: %v", err)
	}
	pc.Close()
}

// The default — no accept_udp line at all — is off. This is the regression the
// opt-in change fixes: a plain web tunnel silently forwarded every QUIC flow
// and let those long-lived datagram flows crowd the connection pool its TCP
// forwards shared, so a site would half-load until a restart. A tunnel that
// says nothing about UDP must carry TCP only.
func TestForwardedUDPIsOffByDefault(t *testing.T) {
	backend := startUDPEcho(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	token := "forwarded-udp-default-0123456789"

	srvCfg := baseServerConfig("ws", tunnelPort, entryPort, backend, token)
	// AcceptUDP is left nil — a config with no accept_udp line, which is what a
	// hand-written config and every tunnel that predates the feature look like.
	if srvCfg.AcceptUDP != nil {
		t.Fatal("baseServerConfig should not set accept_udp; this test needs the default")
	}
	if srvCfg.ForwardsUDP() {
		t.Fatal("the default (no accept_udp line) must not forward UDP")
	}
	cliCfg := baseClientConfig("ws", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })

	srv := server.NewServer(srvCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); srv.Start() }()
	time.Sleep(300 * time.Millisecond)
	cli := client.NewClient(cliCfg, ctx)
	wg.Add(1)
	go func() { defer wg.Done(); cli.Start() }()
	time.Sleep(1500 * time.Millisecond)

	// The UDP side of the forwarded port must be free, because nothing bound it.
	pc, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", entryPort))
	if err != nil {
		t.Fatalf("the default forwarded UDP even though no accept_udp was set: %v", err)
	}
	pc.Close()
}

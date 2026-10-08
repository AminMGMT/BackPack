package l3

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

func openQuicPair(t *testing.T, token, addr string) (listener *quicCarrier) {
	t.Helper()
	c, _, err := listenQuic(Config{Mode: ModeListen, Addr: addr, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	return c.(*quicCarrier)
}

func dialQuicCarrier(t *testing.T, token, addr string) *quicCarrier {
	t.Helper()
	c, _, err := dialQuic(Config{Mode: ModeDial, Addr: addr, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	return c.(*quicCarrier)
}

// readWithin reads one datagram or fails the test.
func readWithin(t *testing.T, c *quicCarrier, d time.Duration) []byte {
	t.Helper()
	got, _ := readFromWithin(t, c, d)
	return got
}

func readFromWithin(t *testing.T, c *quicCarrier, d time.Duration) ([]byte, net.Addr) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 2048)
	n, from, err := c.ReadFrom(buf)
	if err != nil {
		t.Fatalf("no datagram within %s: %v", d, err)
	}
	return buf[:n], from
}

// A dialler that crashes and comes back dials a listener that is still holding
// the dead connection. The listener has to take the new one.
func TestTheQuicListenerTakesAReturningDialler(t *testing.T) {
	const token = "a-quic-carrier-token"
	ln := openQuicPair(t, token, "127.0.0.1:0")
	defer ln.Close()
	addr := ln.LocalAddr().String()

	first := dialQuicCarrier(t, token, addr)
	first.WriteTo([]byte("from the first"), nil)
	if got := readWithin(t, ln, 3*time.Second); !bytes.Equal(got, []byte("from the first")) {
		t.Fatalf("got %q", got)
	}

	// The first dialler "crashes": its socket goes without a word.
	first.conn.CloseWithError(0, "")

	second := dialQuicCarrier(t, token, addr)
	defer second.Close()
	second.WriteTo([]byte("from the second"), nil)
	got, from := readFromWithin(t, ln, 3*time.Second)
	if !bytes.Equal(got, []byte("from the second")) {
		t.Fatalf("got %q", got)
	}

	// And the answer goes back to the one that is there now.
	ln.WriteTo([]byte("answer"), from)
	if got := readWithin(t, second, 3*time.Second); !bytes.Equal(got, []byte("answer")) {
		t.Fatalf("got %q", got)
	}
}

// A listener that crashes and restarts on the same port has lost the dialler's
// connection. With a reset key both lives share, the new one resets the old
// connection at the dialler's next keepalive, instead of the dialler waiting
// out its idle timeout.
func TestADiallerHearsARestartedListenerAtOnce(t *testing.T) {
	const token = "a-quic-reset-token"
	ln := openQuicPair(t, token, "127.0.0.1:0")
	addr := ln.LocalAddr().String()

	d := dialQuicCarrier(t, token, addr)
	defer d.Close()
	d.WriteTo([]byte("hello"), nil)
	readWithin(t, ln, 3*time.Second)

	// A crash: the socket goes, no CONNECTION_CLOSE is sent.
	ln.udp.Close()
	ln.tr.Close()

	again := openQuicPair(t, token, addr)
	defer again.Close()

	// A live tunnel keeps sending. quic-go answers with a stateless reset
	// only a packet longer than 42 bytes (anything shorter could be used to
	// make it amplify), so it is data that draws the reset, not the bare
	// keepalive — an idle dialler waits out its idle timeout instead.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(200 * time.Millisecond):
				d.WriteTo(bytes.Repeat([]byte("x"), 200), nil)
			}
		}
	}()

	began := time.Now()
	d.SetReadDeadline(time.Now().Add(quicIdleTimeout))
	_, _, err := d.ReadFrom(make([]byte, 2048))
	if err == nil {
		t.Fatal("read a datagram on a connection the listener no longer has")
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("the dialler waited out its whole idle timeout (%s) instead of being reset", quicIdleTimeout)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("took %s to notice; the next data packet should have drawn the reset", took)
	}
}

// A listener with nobody connected says so rather than pretending the write
// went out — the tunnel counts it dropped, and a failed write never ends a
// generation, so saying so costs nothing and keeps the counters true.
func TestAnIdleQuicListenerReportsUnsentWrites(t *testing.T) {
	ln := openQuicPair(t, "t", "127.0.0.1:0")
	defer ln.Close()
	if n, err := ln.WriteTo([]byte("x"), &net.UDPAddr{}); err == nil || n != 0 {
		t.Fatalf("WriteTo = %d, %v; want nothing sent and an error", n, err)
	}
}

// Anyone who can reach the port can open a QUIC connection — no token is
// needed for that, only for the tunnel inside it. Such a connection must not
// take anything from the dialler the tunnel is talking to: an earlier version
// let the newest connection win, and a stranger could close the real one as
// often as it liked.
func TestAStrangersConnectionLeavesTheRealDiallerAlone(t *testing.T) {
	ln := openQuicPair(t, "the-token", "127.0.0.1:0")
	defer ln.Close()
	addr := ln.LocalAddr().String()

	real := dialQuicCarrier(t, "the-token", addr)
	defer real.Close()
	real.WriteTo([]byte("real"), nil)
	_, realAddr := readFromWithin(t, ln, 3*time.Second)

	for i := 0; i < quicMaxPeers+4; i++ {
		stranger := dialQuicCarrier(t, "no-token-at-all", addr)
		stranger.WriteTo([]byte("stranger"), nil)
		readWithin(t, ln, 3*time.Second)
		// The tunnel keeps writing to the peer it trusts, which is what
		// keeps that connection from being the one evicted.
		if _, err := ln.WriteTo([]byte("still there?"), realAddr); err != nil {
			t.Fatalf("after %d strangers the real dialler is gone: %v", i+1, err)
		}
		if got := readWithin(t, real, 3*time.Second); !bytes.Equal(got, []byte("still there?")) {
			t.Fatalf("got %q", got)
		}
		defer stranger.Close()
	}
}

func TestAClosedQuicPeerIsRetiredWithAFullInbox(t *testing.T) {
	ln := openQuicPair(t, "reader-lifecycle", "127.0.0.1:0")
	defer ln.Close()
	d := dialQuicCarrier(t, "reader-lifecycle", ln.LocalAddr().String())
	defer d.Close()
	// Backpressure from the tunnel can fill the bounded inbox. Closing the
	// connection must release its reader without waiting for that queue.
	for deadline := time.Now().Add(3 * time.Second); len(ln.in) < cap(ln.in) && time.Now().Before(deadline); {
		if _, err := d.WriteTo([]byte("queued"), nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if len(ln.in) != cap(ln.in) {
		t.Fatal("the QUIC inbox did not fill")
	}
	if _, err := d.WriteTo([]byte("blocked"), nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	ln.mu.Lock()
	var peer *quicPeer
	for _, p := range ln.peers {
		peer = p
	}
	ln.mu.Unlock()
	if peer == nil {
		t.Fatal("the listener has no peer")
	}
	d.Close()
	select {
	case <-peer.conn.Context().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the closed connection did not end")
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		ln.mu.Lock()
		remaining := len(ln.peers)
		ln.mu.Unlock()
		if remaining == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the closed peer is retained while its reader waits on a full inbox")
}

// A stalled initial handshake must end with Run, before any device is opened.
func TestQuicInitialDialStopsWithItsParent(t *testing.T) {
	sink, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	tun, err := New(Config{Mode: ModeDial, Addr: sink.LocalAddr().String(), Token: "cancel-initial-quic", Carrier: CarrierQuic, LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1300}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	tun.openDevice = func(deviceSpec) (packetDevice, error) {
		t.Error("TUN opened before QUIC handshake completed")
		return newFakeDevice(1300), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- tun.Run(ctx) }()
	if err := sink.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sink.ReadFromUDP(make([]byte, 4096)); err != nil {
		t.Fatalf("QUIC dial never sent its initial packet: %v", err)
	}
	begin := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected cancellation result: %v", err)
		}
		t.Logf("QUIC initial dial cancelled after %s", time.Since(begin))
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Tunnel.Run ignores parent cancellation while QUIC initial handshake waits up to 12 seconds")
	}
}

func TestQuicDNSLookupStopsWithContext(t *testing.T) {
	if kind := os.Getenv("BACKPACK_QUIC_DNS_CHILD"); kind != "" {
		started := make(chan struct{}, 1)
		net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if kind == "timeout" {
			ctx, cancel = context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
		}
		done := make(chan error, 1)
		go func() {
			_, _, err := dialQuicContext(ctx, Config{Mode: ModeDial, Addr: "quic-dns-audit.example:443", Carrier: CarrierQuic})
			done <- err
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("DNS resolver was not reached")
		}
		began := time.Now()
		if kind == "cancel" {
			cancel()
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("DNS unexpectedly succeeded")
			}
			want := context.Canceled
			if kind == "timeout" {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Fatalf("DNS error %v does not preserve %v", err, want)
			}
			t.Logf("blocked DNS %s released in %s", kind, time.Since(began))
		case <-time.After(300 * time.Millisecond):
			t.Fatalf("QUIC DNS lookup ignores %s context while DialAddr resolves synchronously", kind)
		}
		return
	}
	for _, kind := range []string{"cancel", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQuicDNSLookupStopsWithContext$", "-test.v")
			cmd.Env = append(os.Environ(), "BACKPACK_QUIC_DNS_CHILD="+kind)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("isolated DNS %s: %v\n%s", kind, err, out)
			}
			t.Logf("%s", out)
		})
	}
}

func TestQuicEndpointKeepsResolutionSemantics(t *testing.T) {
	for _, address := range []string{"127.0.0.1:443", "[::1]:443", "[::]:443", "[::ffff:127.0.0.1]:443", "[fe80::1%testzone]:domain", "localhost:domain", ":443"} {
		expected, err := net.ResolveUDPAddr("udp", address)
		if err != nil {
			t.Fatal(err)
		}
		got, host, err := resolveQuicEndpoint(context.Background(), address)
		if err != nil {
			t.Fatal(err)
		}
		originalHost, _, _ := net.SplitHostPort(address)
		if got.String() != expected.String() || host != originalHost {
			t.Fatalf("%s got %s host%q, expected%s host%q", address, got, host, expected, originalHost)
		}
	}
	for _, address := range []string{"127.0.0.1:65536", "127.0.0.1:-1", "127.0.0.1:unknown-service-audit", "invalid"} {
		if _, _, err := resolveQuicEndpoint(context.Background(), address); err == nil {
			t.Fatalf("invalid endpoint %s accepted", address)
		}
	}
}

package l3

import (
	"bytes"
	"errors"
	"net"
	"os"
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

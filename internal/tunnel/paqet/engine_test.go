package paqet

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestATCPConnectionCrossesTheTunnelAndComesBack(t *testing.T) {
	p := newPair(t, nil)
	p.start(t)
	p.waitUp(t)
	echo := tcpEcho(t)

	conn, err := p.client.Dial(context.Background(), "tcp", echo.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	msg := make([]byte, 600_000)
	rand.Read(msg)
	go conn.Write(msg)
	got := make([]byte, len(msg))
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatal("the echo differs from what was sent")
	}
}

func TestUDPDatagramsKeepTheirBoundariesAcrossTheTunnel(t *testing.T) {
	p := newPair(t, nil)
	p.start(t)
	p.waitUp(t)
	echo := udpEcho(t)

	conn, err := p.client.Dial(context.Background(), "udp", echo.LocalAddr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sizes := []int{1, 17, 1200, 1500, 9000, 40000}
	for _, n := range sizes {
		d := make([]byte, n)
		rand.Read(d)
		if _, err := conn.Write(d); err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		buf := make([]byte, 65535)
		got, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("datagram of %d: %v", n, err)
		}
		if !bytes.Equal(buf[:got], d) {
			t.Fatalf("a datagram of %d bytes came back as %d different bytes", n, got)
		}
	}
}

func TestATargetThatRefusesIsReportedNotSilentlyReset(t *testing.T) {
	p := newPair(t, nil)
	p.start(t)
	p.waitUp(t)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := l.Addr().String()
	l.Close()

	reported := make(chan error, 1)
	conn, err := p.client.Dial(context.Background(), "tcp", dead, func(err error) { reported <- err })
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if sp, ok := conn.(StatusPending); !ok || !sp.StatusPending() {
		t.Fatal("a fresh connection must say its verdict is still pending")
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("reading a refused connection must fail")
	}
	var se statusError
	select {
	case err := <-reported:
		if !errors.As(err, &se) || byte(se) != statusRefused {
			t.Fatalf("reported %v, want a refusal", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was reported")
	}
}

func TestSeveralSessionsShareTheLoadAndEachHasItsOwnSourcePort(t *testing.T) {
	p := newPair(t, func(c, s *Config) { c.Params.Conns = 3 })
	p.start(t)
	p.waitUp(t)
	deadline := time.Now().Add(15 * time.Second)
	for p.cLease.inUse() < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := p.cLease.inUse(); n != 3 {
		t.Fatalf("3 sessions should hold 3 source ports, hold %d", n)
	}
	echo := tcpEcho(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := p.client.Dial(context.Background(), "tcp", echo.Addr().String(), nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			c.Write([]byte("ping"))
			buf := make([]byte, 4)
			c.SetReadDeadline(time.Now().Add(10 * time.Second))
			if _, err := io.ReadFull(c, buf); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestATunnelOverALossyPathStillDeliversEverything(t *testing.T) {
	p := newPair(t, nil)
	p.cEnd.setLoss(0.05)
	p.sEnd.setLoss(0.05)
	p.start(t)
	p.waitUp(t)
	echo := tcpEcho(t)
	conn, err := p.client.Dial(context.Background(), "tcp", echo.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	msg := make([]byte, 400_000)
	rand.Read(msg)
	go conn.Write(msg)
	got := make([]byte, len(msg))
	conn.SetReadDeadline(time.Now().Add(40 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("5%% loss each way broke the transfer: %v", err)
	}
}

func TestARestartedServerIsFoundAgainWithoutTouchingTheClient(t *testing.T) {
	p := newPair(t, nil)
	p.start(t)
	p.waitUp(t)

	// Kill the server generation and start another on the same fake wire.
	p.server.gen.cancel()
	deadline := time.Now().Add(20 * time.Second)
	for p.client.Up() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if p.client.Up() {
		t.Fatal("the client never noticed the server was gone")
	}
	select {
	case <-p.serr:
	case <-time.After(10 * time.Second):
		t.Fatal("the stopped server generation did not return")
	}
	// A restarted process has a fresh wire; the fake end is reopened to match.
	p.sEnd.reopen()
	p.wg.Add(1)
	go func() { defer p.wg.Done(); p.serr <- p.server.Run(context.Background()) }()
	t.Cleanup(func() {
		p.server.mu.RLock()
		g := p.server.gen
		p.server.mu.RUnlock()
		if g != nil {
			g.cancel()
		}
	})
	deadline = time.Now().Add(40 * time.Second)
	for !p.client.Up() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !p.client.Up() {
		t.Fatal("the client did not reconnect to the restarted server")
	}
}

func TestAnUnansweredFlowEndsTheGenerationSoItIsReopenedFromNewPorts(t *testing.T) {
	old := flowStuckAfter
	flowStuckAfter = 2 * time.Second
	defer func() { flowStuckAfter = old }()

	p := newPair(t, nil)
	p.cEnd.setLoss(1) // nothing the client sends arrives
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The client only: nobody to answer.
	err := p.client.Run(ctx)
	if !errors.Is(err, ErrFlowStuck) {
		t.Fatalf("Run returned %v, want ErrFlowStuck", err)
	}
	if n := p.cLease.inUse(); n != 0 {
		t.Fatalf("%d source ports were left claimed after the generation ended", n)
	}
}

func TestTheOldPCKHandshakeIsNamedNotIgnored(t *testing.T) {
	p := newPair(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.server.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	// What the old l3 dialler's first datagram looks like: kind 1, session,
	// counter 0, then a handshake body.
	old := make([]byte, 13+64)
	old[0] = 1
	old[4] = 7
	p.cEnd.Inject(41000, old, &net.UDPAddr{IP: p.sEnd.ip, Port: testPort})
	deadline := time.Now().Add(5 * time.Second)
	for p.server.Stats().Foreign == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if p.server.Stats().Foreign == 0 {
		t.Fatal("the old handshake was not recognised")
	}
}

func TestByteCountersMoveWithTheKeepalivesSoAnIdleTunnelIsNotStalled(t *testing.T) {
	p := newPair(t, nil)
	p.start(t)
	p.waitUp(t)
	a := p.client.Stats()
	time.Sleep(3 * time.Second)
	b := p.client.Stats()
	if b.BytesIn <= a.BytesIn || b.BytesOut <= a.BytesOut {
		t.Fatalf("an idle but healthy tunnel showed no wire traffic in 3s: %+v -> %+v", a, b)
	}
}

// io.Copy is what the forwarder's bridge uses, and it prefers WriterTo to Read.
// smux's stream has one, which handed the server's verdict byte to the user as
// the first byte of their data — found by the live test, not by reading.
func TestCopyingFromAForwardedConnectionNeverYieldsTheVerdictByte(t *testing.T) {
	p := newPair(t, nil)
	p.start(t)
	p.waitUp(t)
	echo := tcpEcho(t)
	conn, err := p.client.Dial(context.Background(), "tcp", echo.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("ping"))
	var mu sync.Mutex
	var got bytes.Buffer
	done := make(chan struct{})
	go func() { io.Copy(lockedWriter{&mu, &got}, conn); close(done) }()
	length := func() int { mu.Lock(); defer mu.Unlock(); return got.Len() }
	deadline := time.Now().Add(10 * time.Second)
	for length() < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	conn.Close()
	<-done
	if got.String() != "ping" {
		t.Fatalf("io.Copy delivered %q, want %q", got.String(), "ping")
	}

	// And a UDP flow, whose datagrams must come out one per Read even when the
	// copier is io.Copy-shaped.
	u := udpEcho(t)
	dc, err := p.client.Dial(context.Background(), "udp", u.LocalAddr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Close()
	dc.Write([]byte("one"))
	dc.Write([]byte("two"))
	var parts []string
	buf := make([]byte, 100)
	dc.SetReadDeadline(time.Now().Add(10 * time.Second))
	for len(parts) < 2 {
		n, err := dc.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, string(buf[:n]))
	}
	if parts[0] != "one" || parts[1] != "two" {
		t.Fatalf("datagrams came back as %q", parts)
	}
}

// kcp-go closes a session whose PacketConn write fails, so a segment the wire
// could not send (a full transmit queue) must not reach it as an error.
func TestAFailedSendIsALostPacketNotABrokenSocket(t *testing.T) {
	a, b := newFakePair()
	w := NewWire(&failingEnd{fakeEnd: a}, &failingEnd{fakeEnd: a}, a.ip)
	defer w.Close()
	_ = b
	s, err := w.Open(40000, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.WriteTo([]byte("hello"), &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 9})
	if err != nil || n != 5 {
		t.Fatalf("WriteTo = %d, %v; a send error must be swallowed", n, err)
	}
	if st := w.Stats(); st.TxErrors != 1 || st.PacketsOut != 0 {
		t.Fatalf("stats %+v: the failure should be counted as an error, not as a packet out", st)
	}
}

type failingEnd struct{ *fakeEnd }

func (f *failingEnd) Inject(uint16, []byte, *net.UDPAddr) error { return syscall.ENOBUFS }

type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// A peer that finishes sending with shutdown(SHUT_WR) and then waits for the
// answer must get the answer: the end of the request is not the end of the
// connection. The target here only replies once it has read EOF.
func TestHalfCloseKeepsTheReplyFlowing(t *testing.T) {
	p := newPair(t, nil)
	p.start(t)
	p.waitUp(t)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				n, _ := io.Copy(io.Discard, c) // returns at the client's CloseWrite
				c.Write([]byte("got " + itoa(n) + " bytes"))
			}()
		}
	}()

	for _, size := range []int{5, 3_000_000} {
		conn, err := p.client.Dial(context.Background(), "tcp", l.Addr().String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		cw, ok := conn.(interface{ CloseWrite() error })
		if !ok {
			t.Fatal("a forwarded TCP connection must be able to half-close")
		}
		msg := make([]byte, size)
		go func() {
			conn.Write(msg)
			cw.CloseWrite()
		}()
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		reply, err := io.ReadAll(conn)
		if err != nil {
			t.Fatalf("%d bytes then CloseWrite: reading the reply: %v", size, err)
		}
		if want := "got " + itoa(int64(size)) + " bytes"; string(reply) != want {
			t.Fatalf("%d bytes then CloseWrite: reply %q, want %q", size, reply, want)
		}
		conn.Close()
	}
}

// The other direction: a target that sends and half-closes first leaves the
// client free to keep writing, and the client reads the data and then the end.
func TestTheTargetsHalfCloseIsSeenAsAnEndAndTheClientCanStillWrite(t *testing.T) {
	p := newPair(t, nil)
	p.start(t)
	p.waitUp(t)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte("hello"))
		c.(*net.TCPConn).CloseWrite()
		b, _ := io.ReadAll(c)
		got <- string(b)
	}()

	conn, err := p.client.Dial(context.Background(), "tcp", l.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	b, err := io.ReadAll(conn)
	if err != nil || string(b) != "hello" {
		t.Fatalf("read %q, %v; want hello then an end", b, err)
	}
	conn.Write([]byte("late words"))
	conn.(interface{ CloseWrite() error }).CloseWrite()
	select {
	case s := <-got:
		if s != "late words" {
			t.Fatalf("the target read %q after its own half-close", s)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the client's data after the target's half-close never arrived")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

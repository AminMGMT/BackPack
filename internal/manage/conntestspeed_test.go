package manage

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSpeedPeakIsTheBestSecondNotTheBestTick(t *testing.T) {
	half := 500 * time.Millisecond
	mb := func(mbit float64) uint64 { return uint64(mbit * 1e6 / 8 * half.Seconds()) }
	// 10, 10, 40, 10, 10 Mb/s in half-second ticks. One tick at 40 is a
	// burst; the best whole second is (40+10)/2 = 25.
	ticks := []ctSpeedTick{{mb(10), half}, {mb(10), half}, {mb(40), half}, {mb(10), half}, {mb(10), half}}
	if got := ctSpeedPeak(ticks); got < 24.9 || got > 25.1 {
		t.Errorf("peak = %.2f Mb/s, want 25", got)
	}
	if got := ctSpeedPeak(ticks[:1]); got != 0 {
		t.Errorf("peak of half a second = %.2f, want 0 (shorter than the window)", got)
	}
}

// speedServer is a kharej answering speed streams on loopback, the way the
// test tunnel's far end does.
func speedServer(t *testing.T) (dial func() (net.Conn, error), conns *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	conns = &atomic.Int32{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			go func() { defer c.Close(); ctServeSpeedOrEcho(c) }()
		}
	}()
	return func() (net.Conn, error) { return net.Dial("tcp", ln.Addr().String()) }, conns
}

func TestSpeedRunsEveryStageAndReportsAMaximum(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the real speed test for about twenty seconds")
	}
	dial, conns := speedServer(t)
	r, err := ctSpeed(context.Background(), dial)
	if err != nil {
		t.Fatal(err)
	}
	// Two directions, each at four and then sixteen connections at least,
	// and a stage past those only where the one before it paid.
	if got, least := conns.Load(), int32(2*(4+16)); got < least || got > int32(2*(4+16+32+64)) {
		t.Errorf("%d connections opened, want %d to %d — a stage was skipped", got, least, 2*(4+16+32+64))
	}
	if r.download <= 0 || r.upload <= 0 {
		t.Fatalf("download %.1f, upload %.1f", r.download, r.upload)
	}
	if r.downloadPeak < r.download || r.uploadPeak < r.upload {
		t.Errorf("max ↓ %.1f / ↑ %.1f is below the sustained ↓ %.1f / ↑ %.1f",
			r.downloadPeak, r.uploadPeak, r.download, r.upload)
	}
	if !slices.Contains(ctSpeedStages, r.streams) {
		t.Errorf("streams = %d", r.streams)
	}
}

// The climb: four and sixteen connections always, then on while a stage is a
// tenth faster than the best before it, so the figure kept is where more
// connections stopped helping.
func TestTheSpeedTestClimbsUntilMoreConnectionsStopHelping(t *testing.T) {
	for _, c := range []struct {
		stage      int
		last, best float64
		want       bool
	}{
		{0, 10, 0, true},     // after four: sixteen always runs
		{0, 0, 0, true},      // even when four carried nothing
		{1, 200, 100, true},  // sixteen doubled four: try thirty-two
		{1, 105, 100, false}, // sixteen barely beat four: the tunnel is full
		{1, 80, 100, false},  // sixteen was slower: stop
		{2, 330, 300, true},  // thirty-two a tenth faster: try sixty-four
		{2, 320, 300, false},
	} {
		if got := ctSpeedClimbs(c.stage, c.last, c.best); got != c.want {
			t.Errorf("after stage %d at %.0f with %.0f before: climb %v, want %v", c.stage, c.last, c.best, got, c.want)
		}
	}
	if ctSpeedStages[0] != 4 || ctSpeedStages[1] != 16 || ctSpeedStages[len(ctSpeedStages)-1] != 64 {
		t.Fatalf("stages %v, want 4 and 16 first and up to 64", ctSpeedStages)
	}
}

// A path that keeps getting faster with more connections is measured at all
// of them, and the figure kept is the loaded one; one that is full at sixteen
// stops there.
func TestTheSpeedTestMeasuresTheConnectionCountThePathWants(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the real speed test")
	}
	for _, c := range []struct {
		name    string
		perConn float64 // bytes per second each connection may carry
		total   float64 // what the path carries in all, 0 for no cap
		want    []int   // the connection counts the figure may come from
		opened  int32   // the connections every stage run opens between them
	}{
		// Each stage twice the one before: the climb goes to the last.
		{"wants more connections", 1 << 20, 0, []int{64}, 4 + 16 + 32 + 64},
		// Thirty-two carries what sixteen did, so sixty-four never runs; the
		// two are level, and either may be the one kept.
		{"full at sixteen", 1 << 20, 16 << 20, []int{16, 32}, 4 + 16 + 32},
	} {
		t.Run(c.name, func(t *testing.T) {
			dial, conns := speedServer(t)
			shaped := shapedDialer(dial, c.perConn, c.total)
			run, err := ctSpeedStaged(context.Background(), shaped, 'D')
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(c.want, run.streams) {
				t.Errorf("the figure came from %d connections, want one of %v (%.1f Mb/s)", run.streams, c.want, run.avg)
			}
			if got := conns.Load(); got != c.opened {
				t.Errorf("%d connections opened, want %d — the climb ran the wrong stages", got, c.opened)
			}
		})
	}
}

// speedStagesUpTo16 runs the speed test at the two stages that always run, for
// a test about something else on a fast path where the climb would run to the
// last stage on every tunnel.
func speedStagesUpTo16(t *testing.T) {
	previous := ctSpeedStages
	ctSpeedStages = ctSpeedStages[:ctSpeedAlways]
	t.Cleanup(func() { ctSpeedStages = previous })
}

// A carrier that will not open sixteen connections still has a speed: the
// first stage's.
func TestSpeedKeepsTheFirstStageWhenALaterOneFails(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the real speed test")
	}
	dial, _ := speedServer(t)
	var open atomic.Int32
	limited := func() (net.Conn, error) {
		if open.Add(1) > 4 {
			open.Add(-1)
			return nil, net.ErrClosed
		}
		c, err := dial()
		if err != nil {
			open.Add(-1)
			return nil, err
		}
		return &countedConn{Conn: c, open: &open}, nil
	}
	r, err := ctSpeed(context.Background(), limited)
	if err != nil {
		t.Fatalf("a refused second stage failed the whole test: %v", err)
	}
	if r.streams != 4 || r.download <= 0 || r.upload <= 0 {
		t.Errorf("result = %+v, want the four-connection figures", r)
	}
}

type countedConn struct {
	net.Conn
	open   *atomic.Int32
	closed atomic.Bool
}

func (c *countedConn) Close() error {
	if !c.closed.Swap(true) {
		c.open.Add(-1)
	}
	return c.Conn.Close()
}

// An upload's receiver reports its running count every ctSpeedAckEvery rather
// than after every read — a count per 32 KB read was half the CPU of a fast
// upload, and the test measured that rather than the tunnel. The counts still
// only ever grow, the last one is everything that arrived, and the stream
// still ends with the marker.
func TestUploadCountsArePacedAndComplete(t *testing.T) {
	dial, _ := speedServer(t)
	c, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	payload := make([]byte, ctSpeedBlock)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	if _, err := c.Write(append([]byte(ctSpeedMagic+"U"), payload...)); err != nil {
		t.Fatal(err)
	}
	ack := make([]byte, len(ctSpeedAck))
	if _, err := io.ReadFull(c, ack); err != nil || string(ack) != ctSpeedAck {
		t.Fatalf("handshake: %q %v", ack, err)
	}
	if _, err := c.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	const span = time.Second
	wrote := make(chan uint64, 1)
	go func() {
		var sent uint64
		defer func() { wrote <- sent }()
		deadline := time.Now().Add(span)
		for time.Now().Before(deadline) {
			n, err := c.Write(payload)
			sent += uint64(n)
			if err != nil {
				return
			}
		}
		c.(*net.TCPConn).CloseWrite()
	}()
	var counts []uint64
	var msg [8]byte
	for {
		if _, err := io.ReadFull(c, msg[:]); err != nil {
			t.Fatalf("the stream ended without the marker after %d counts: %v", len(counts), err)
		}
		n := binary.BigEndian.Uint64(msg[:])
		if n == math.MaxUint64 {
			break
		}
		if len(counts) > 0 && n < counts[len(counts)-1] {
			t.Fatalf("count went back from %d to %d", counts[len(counts)-1], n)
		}
		counts = append(counts, n)
	}
	sent := <-wrote
	if len(counts) == 0 || counts[len(counts)-1] != sent {
		t.Fatalf("last count %v, sent %d", counts[len(counts)-1:], sent)
	}
	reads := sent / ctSpeedBlock
	if limit := uint64(span/ctSpeedAckEvery) * 2; uint64(len(counts)) > limit || (reads > 4*limit && uint64(len(counts)) >= reads) {
		t.Errorf("%d counts for %d bytes (%d reads' worth) in %s: not paced", len(counts), sent, reads, span)
	}
}

// The second pass keeps each tunnel's better figures: a slower second
// measurement does not lower the first, and one that cannot run at all leaves
// the first pass's standing.
func TestTheSecondSpeedPassKeepsTheBetterFigures(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the real speed test")
	}
	dial, conns := speedServer(t)
	high := ctSpeedResult{download: 1e6, upload: 1e6, downloadPeak: 1e6, uploadPeak: 1e6, streams: 4, uploadStreams: 16}
	got := ctSpeedAgain(context.Background(), dial, high)
	if got.download != high.download || got.upload != high.upload || got.downloadPeak != high.downloadPeak {
		t.Errorf("a slower second pass replaced the first: %+v", got)
	}
	if n := conns.Load(); n != 4+16 {
		t.Errorf("%d connections, want each direction once at the count it did best with", n)
	}

	low := ctSpeedResult{download: 0.001, upload: 0.001, streams: 4, uploadStreams: 4}
	if got := ctSpeedAgain(context.Background(), dial, low); got.download <= low.download || got.upload <= low.upload ||
		got.downloadPeak < got.download {
		t.Errorf("a faster second pass was not kept: %+v", got)
	}

	refused := func() (net.Conn, error) { return nil, io.ErrClosedPipe }
	if got := ctSpeedAgain(context.Background(), refused, high); got != high {
		t.Errorf("a failed second pass changed the result: %+v", got)
	}
}

// The panel's countdown in the speed phase: a full measurement for each
// tunnel still to be measured the first time, the second pass for each
// measured once, nothing before the phase starts.
func TestTheSpeedPhaseCountdown(t *testing.T) {
	first, second := ctSpeedFirstPass(), ctSpeedSecondPass()
	for _, c := range []struct {
		phases []string
		want   time.Duration
	}{
		{[]string{"", "speed-queued", "speed-queued"}, 0},
		{[]string{"speed", "speed-queued", ""}, 2 * (first + second)},
		{[]string{ctPhaseAgain, "speed", "speed-queued"}, second + 2*(first+second)},
		{[]string{ctPhaseAgain, ctPhaseSecond, ""}, 2 * second},
		{[]string{"", ctPhaseSecond}, second},
	} {
		if got := ConnTestSpeedLeft(c.phases); got != c.want {
			t.Errorf("%q: %v left, want %v", c.phases, got, c.want)
		}
	}
}

// shapedDialer is dial with each connection's reads held to perConn bytes a
// second, and all of them together to total (0 for no cap): a path whose
// speed depends on how many connections share it, as a long fat one's does.
func shapedDialer(dial func() (net.Conn, error), perConn, total float64) func() (net.Conn, error) {
	shared := &rateLimit{rate: total}
	return func() (net.Conn, error) {
		c, err := dial()
		if err != nil {
			return nil, err
		}
		return &shapedConn{Conn: c, own: &rateLimit{rate: perConn}, shared: shared}, nil
	}
}

type shapedConn struct {
	net.Conn
	own, shared *rateLimit
}

func (c *shapedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	// Held until both limits allow it, not one after the other: the two
	// waits overlap, as a slow connection on a full path's do.
	time.Sleep(max(c.own.reserve(n), c.shared.reserve(n)))
	return n, err
}

// rateLimit paces bytes at rate a second; a zero rate is no limit.
type rateLimit struct {
	mu   sync.Mutex
	rate float64
	next time.Time
}

// reserve books n bytes and returns how long to wait before they are taken.
func (l *rateLimit) reserve(n int) time.Duration {
	if l.rate == 0 || n <= 0 {
		return 0
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	l.next = l.next.Add(time.Duration(float64(n) / l.rate * float64(time.Second)))
	d := l.next.Sub(now)
	l.mu.Unlock()
	return d
}

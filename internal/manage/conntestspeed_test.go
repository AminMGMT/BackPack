package manage

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"net"
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
	// Two directions, each at four and then sixteen connections.
	if got, want := conns.Load(), int32(2*(4+16)); got != want {
		t.Errorf("%d connections opened, want %d — a stage was skipped", got, want)
	}
	if r.download <= 0 || r.upload <= 0 {
		t.Fatalf("download %.1f, upload %.1f", r.download, r.upload)
	}
	if r.downloadPeak < r.download || r.uploadPeak < r.upload {
		t.Errorf("max ↓ %.1f / ↑ %.1f is below the sustained ↓ %.1f / ↑ %.1f",
			r.downloadPeak, r.uploadPeak, r.download, r.upload)
	}
	if r.streams != 4 && r.streams != 16 {
		t.Errorf("streams = %d", r.streams)
	}
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

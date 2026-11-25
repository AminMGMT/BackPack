package transport

import (
	"net"
	"strings"
	"testing"
	"time"
)

// A connection that cannot be handled goes back on the queue — and putting it
// back must never block.
//
// The send was a bare one, made from inside the goroutine that drains the very
// channel it sends into. In the mux transports there is one such goroutine per
// session, and the one making the send is a session that has just failed, so it
// is frequently the only one running. A full channel is then a goroutine
// waiting for itself: the queue holds channel_size connections while the loop
// that empties it is parked on a send into it, and the tunnel stops for good
// with every socket still open and nothing in the log to say why.
func TestRequeueingALocalConnectionNeverBlocks(t *testing.T) {
	ch := make(chan LocalTCPConn, 1)
	lim := newLimiter(Limits{MaxConnections: 4})
	log := quietLogger()

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	// Room for one: it goes back.
	if !lim.acquire() {
		t.Fatal("the limiter refused the first slot")
	}
	if !requeueLocal(ch, LocalTCPConn{conn: a}, lim, log) {
		t.Fatal("a connection was dropped while the queue had room")
	}

	// No room for the second. The call has to return rather than wait, and it
	// has to leave nothing behind.
	if !lim.acquire() {
		t.Fatal("the limiter refused the second slot")
	}
	before := lim.active.Load()

	c, d := net.Pipe()
	defer d.Close()

	done := make(chan bool, 1)
	go func() { done <- requeueLocal(ch, LocalTCPConn{conn: c}, lim, log) }()

	select {
	case ok := <-done:
		if ok {
			t.Fatal("a full queue reported the connection as re-queued")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("re-queueing blocked on a full channel — this send is made from " +
			"the only goroutine that drains it, so blocking here is the tunnel " +
			"stopping for good")
	}

	if after := lim.active.Load(); after != before-1 {
		t.Errorf("a dropped connection left its slot taken: %d before, %d after", before, after)
	}
	// And it is closed, or the socket outlives the run.
	if _, err := c.Write([]byte("x")); err == nil {
		t.Error("a dropped connection was left open")
	}
}

// A limiter that is not configured must not be a nil dereference on this path.
func TestRequeueingWorksWithNoLimitsConfigured(t *testing.T) {
	ch := make(chan LocalTCPConn, 1)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if !requeueLocal(ch, LocalTCPConn{conn: a}, nil, quietLogger()) {
		t.Fatal("an unlimited tunnel could not re-queue a connection")
	}
	c, d := net.Pipe()
	defer d.Close()
	if requeueLocal(ch, LocalTCPConn{conn: c}, nil, quietLogger()) {
		t.Fatal("a full queue reported success")
	}
}

// No mux transport may put a connection back with a blocking send, and each has
// to give back the mux slot the failed attempt took.
//
// The slot was the second half of the same stall. A session that failed to send
// the address never popped its counter, so after MuxCon such failures the loop
// blocked at the top on a counter only it could empty — the same deadlock by a
// different channel.
// It is read in one place now, because there is one copy. The three transports
// that used to hold it are checked for still delegating rather than for holding
// it correctly — which is the stronger statement of the two, and the one that
// stops a fourth copy appearing.
func TestNoMuxTransportBlocksPuttingAConnectionBack(t *testing.T) {
	for _, name := range []string{"tcpmux", "wsmux", "kcp"} {
		t.Run(name+" delegates", func(t *testing.T) {
			src := readTransportSource(t, name+".go")
			if !strings.Contains(src, ".run(session)") {
				t.Errorf("%s.go no longer hands its session to the shared loop — if it "+
					"has a copy of it again, every fault fixed in muxsession.go has to "+
					"be found and fixed here as well", name)
			}
			for _, line := range strings.Split(src, "\n") {
				trimmed := strings.TrimSpace(line)
				// `case g.localChannel <- …` is the accept path offering a new
				// connection and is allowed to be a select arm. A bare send is
				// the one that blocks.
				if strings.HasPrefix(trimmed, "g.localChannel <-") {
					t.Errorf("%s.go puts a connection back with a blocking send:\n  %s",
						name, trimmed)
				}
			}
		})
	}

	for _, name := range []string{"muxsession"} {
		t.Run(name, func(t *testing.T) {
			src := readTransportSource(t, name+".go")
			for _, line := range strings.Split(src, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") {
					continue
				}
				if strings.HasPrefix(trimmed, "m.local <-") {
					t.Errorf("%s.go puts a connection back with a blocking send:\n  %s",
						name, trimmed)
				}
			}
			if strings.Count(src, "requeueLocal(") != 2 {
				t.Errorf("%s.go does not use requeueLocal on both of its re-queue paths", name)
			}
			// The failed-address path has to return its mux slot and close the
			// stream it cannot use.
			i := strings.Index(src, "failed to send address over stream")
			if i < 0 {
				t.Fatalf("%s.go no longer has the failed-address path", name)
			}
			block := src[i:]
			if j := strings.Index(block, "continue"); j > 0 {
				block = block[:j]
			}
			if !strings.Contains(block, "<-counter") {
				t.Errorf("%s.go does not give back the mux slot when it fails to send "+
					"the address — after MuxCon failures the session stops taking "+
					"connections at all", name)
			}
			if !strings.Contains(block, "stream.Close()") {
				t.Errorf("%s.go leaks the stream it could not use", name)
			}
		})
	}
}

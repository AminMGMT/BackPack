package transport

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A goroutine dying during a teardown must not queue a restart of the tunnel
// that is being torn down.
//
// Every client engine guarded its restart with `c.state.Cancel() != nil`, which
// is a condition that cannot be false: the constructor installs a cancel
// function before any of this code can run, and Reset installs another on every
// restart. So the guard was open in every case it was written to close. A
// tunnel coming down — a reload, a stop, a restart already in flight — has
// several goroutines fail at once, and each one logged an error and queued
// another Restart of a transport that was already going away.
//
// The server transports were corrected to ask their generation's own context
// instead, with the reasoning written out at internal/server/transport/tcp.go.
// The client ones kept the original. This is the same check on this side.
func TestNoClientEngineRestartsOnAVestigialGuard(t *testing.T) {
	for _, engine := range clientEngines {
		lines := codeLines(readEngine(t, "client", engine))
		for i, line := range lines {
			if !strings.Contains(line, "c.state.Cancel() != nil") {
				continue
			}
			// Restart's own check before calling the cancel function is a real
			// nil check and the one legitimate use: it guards the very next
			// line, which is the call.
			if i+1 < len(lines) && strings.Contains(lines[i+1], "c.state.Cancel()()") {
				continue
			}
			t.Errorf("%s guards a restart with c.state.Cancel() != nil, which is always "+
				"true — every goroutine dying during a teardown queues another restart "+
				"of a tunnel that is already going down:\n  %s", engine, line)
		}
	}
}

// codeLines drops comments and blank lines, so a check reads what the engine
// does rather than what it says about itself.
func codeLines(src string) []string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		out = append(out, t)
	}
	return out
}

// Every byte the control channel carries is one byte, and none of them is worth
// waiting a quarter of an hour for.
//
// A write into a peer that has stopped reading fills the kernel's send buffer
// and then blocks until the retransmit timer gives up — around fifteen minutes
// on Linux defaults. The server side grew SendBinaryByteWithin and writeControl
// for precisely that failure, and the client kept writing unbounded: the
// shutdown notice, which stalls a restart, and the RTT probe, which parks a
// goroutine on a timer forever and quietly stops the figure the panel shows.
func TestEveryClientEngineBoundsItsControlWrites(t *testing.T) {
	for _, engine := range clientEngines {
		src := readEngine(t, "client", engine)
		if strings.Contains(src, "utils.SendBinaryByte(c.state.Conn()") {
			t.Errorf("%s writes to its control channel with no bound — a peer that "+
				"stopped reading can hold this tunnel down for a quarter of an hour", engine)
		}
		if strings.Contains(src, "c.state.WSConn().WriteMessage(") {
			t.Errorf("%s writes to its websocket control channel with no bound — "+
				"gorilla's WriteMessage takes the deadline from the connection, and "+
				"nothing sets one", engine)
		}
	}
}

// And the bound has to be one both sides agree on, or a channel one end has
// given up on is one the other is still waiting for.
func TestTheControlWriteBoundMatchesTheServer(t *testing.T) {
	srv, err := os.ReadFile(filepath.Join("..", "..", "server", "transport", "control.go"))
	if err != nil {
		t.Fatalf("reading the server's control channel: %v", err)
	}
	want := regexp.MustCompile(`controlWriteTimeout = (\d+) \* time\.Second`).FindStringSubmatch(string(srv))
	if want == nil {
		t.Fatal("the server no longer names a control-write bound")
	}
	n, err := strconv.Atoi(want[1])
	if err != nil {
		t.Fatal(err)
	}
	if controlWriteTimeout != time.Duration(n)*time.Second {
		t.Errorf("the client bounds a control write at %v and the server at %ds — one end "+
			"gives up on a channel the other is still waiting for", controlWriteTimeout, n)
	}
}

// A pooled transport has to say what its pool is doing.
//
// The pool is allowed to outgrow its configured size, which from outside is
// indistinguishable from a leak — so every transport that maintains one
// publishes what it has, what it is aiming for and what it was asked for, and
// the panel draws a card from it. ws did not, so that card was not empty or
// zero on a ws or wss tunnel: it was absent, while every other transport had
// one.
func TestEveryPooledClientEngineReportsItsPool(t *testing.T) {
	// udp is not pooled in this sense — it has no pool maintainer at all.
	for _, engine := range []string{"tcp", "tcpmux", "ws", "wsmux", "kcp", "quic"} {
		src := readEngine(t, "client", engine)
		if !strings.Contains(src, "poolMaintainer") {
			t.Errorf("%s no longer maintains a pool; take it out of this list", engine)
			continue
		}
		if !strings.Contains(src, "metrics.ReportPool(") {
			t.Errorf("%s maintains a connection pool and never reports it, so the panel's "+
				"pool card is missing entirely on that transport", engine)
		}
	}
}

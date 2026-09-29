package metrics

import (
	"sync/atomic"
	"testing"
)

// Reported from the panel: "carried since this server was set up" went back
// down after every update. Three ways the figure lost traffic, each pinned here.

// The layer-3 engine counts its own traffic and hands the collector its
// counters. Those start from zero in every process, and they used to replace
// the carried-over total rather than add to it — so a direct tunnel's history
// was gone after every update, every restart, every config edit.
func TestAnEngineCountingItsOwnTrafficKeepsItsTotalAcrossARestart(t *testing.T) {
	resetCounters(t)
	dir := t.TempDir()

	var in, out atomic.Uint64
	first := NewCollector(dir, "pck", "l3-pck", "server", in.Load, out.Load)
	in.Store(7000)
	out.Store(3000)
	if err := first.Write(); err != nil {
		t.Fatal(err)
	}

	// The update: a new process, counters from zero.
	var in2, out2 atomic.Uint64
	second := NewCollector(dir, "pck", "l3-pck", "server", in2.Load, out2.Load)
	if got := second.Snapshot(); got.BytesIn != 7000 || got.BytesOut != 3000 {
		t.Fatalf("after a restart: in %d, out %d; want the previous 7000/3000", got.BytesIn, got.BytesOut)
	}
	in2.Store(100)
	out2.Store(50)
	if got := second.Snapshot(); got.BytesIn != 7100 || got.BytesOut != 3050 {
		t.Errorf("in %d, out %d; want 7100/3050 (previous plus new)", got.BytesIn, got.BytesOut)
	}
}

// A reload — a config edit, a restart asked for over the control socket —
// starts a new collector inside the same process. The process-wide counters
// are not reset by that, and the new collector's baseline already holds them:
// adding them again counted every byte of the process twice.
func TestAReloadInsideOneProcessDoesNotCountTwice(t *testing.T) {
	resetCounters(t)
	dir := t.TempDir()

	first := NewCollector(dir, "t", "tcp", "server", nil, nil)
	AddBytes(1000, 500)
	if err := first.Write(); err != nil {
		t.Fatal(err)
	}

	second := NewCollector(dir, "t", "tcp", "server", nil, nil)
	if got := second.Snapshot(); got.BytesIn != 1000 || got.BytesOut != 500 {
		t.Fatalf("after a reload: in %d, out %d; want 1000/500", got.BytesIn, got.BytesOut)
	}
	AddBytes(10, 5)
	if got := second.Snapshot(); got.BytesIn != 1010 || got.BytesOut != 505 {
		t.Errorf("in %d, out %d; want 1010/505", got.BytesIn, got.BytesOut)
	}
}

// Deleting a tunnel took everything it had carried out of the server's total,
// and a tunnel made again under the same name started from the old one's
// figure. What a deleted tunnel carried moves into the server's own ledger.
func TestADeletedTunnelsTrafficStaysInTheServerTotal(t *testing.T) {
	resetCounters(t)
	dir := t.TempDir()

	for _, name := range []string{"a", "b"} {
		bytesIn.Store(0)
		bytesOut.Store(0)
		c := NewCollector(dir, name, "tcp", "server", nil, nil)
		AddBytes(1000, 400)
		if err := c.Write(); err != nil {
			t.Fatal(err)
		}
	}
	if err := Retire(dir, "a"); err != nil {
		t.Fatal(err)
	}
	if err := Retire(dir, "b"); err != nil {
		t.Fatal(err)
	}
	if err := Retire(dir, "never-ran"); err != nil {
		t.Errorf("retiring a tunnel with no history: %v", err)
	}

	in, out := Retired(dir)
	if in != 2000 || out != 800 {
		t.Errorf("the ledger holds in %d, out %d; want 2000/800", in, out)
	}
	if _, err := Read(dir, "a"); err == nil {
		t.Error("the deleted tunnel's own file is still there; a new tunnel named a would inherit it")
	}

	bytesIn.Store(0)
	bytesOut.Store(0)
	again := NewCollector(dir, "a", "tcp", "server", nil, nil)
	if got := again.Snapshot(); got.BytesIn != 0 {
		t.Errorf("a new tunnel named like a deleted one started at %d", got.BytesIn)
	}
}

package l3

import "testing"

// acceptCommit is the sequence the data path uses: ask, then record only on
// success. Returns whether the counter was taken.
func (w *replayWindow) acceptCommit(counter uint64) bool {
	if !w.accept(counter) {
		return false
	}
	w.commit(counter)
	return true
}

func TestReplayWindowAcceptsEachCounterOnce(t *testing.T) {
	var w replayWindow
	for i := uint64(0); i < 5000; i++ {
		if !w.acceptCommit(i) {
			t.Fatalf("counter %d refused on first use", i)
		}
	}
	// Everything still inside the window is now a duplicate.
	for i := uint64(5000 - replayWindowBits); i < 5000; i++ {
		if w.accept(i) {
			t.Fatalf("counter %d accepted twice", i)
		}
	}
}

func TestReplayWindowAcceptsCounterZero(t *testing.T) {
	var w replayWindow
	if !w.acceptCommit(0) {
		t.Fatal("counter 0 refused as the first packet")
	}
	if w.accept(0) {
		t.Fatal("counter 0 accepted twice")
	}
}

func TestReplayWindowHandlesReordering(t *testing.T) {
	var w replayWindow
	order := []uint64{10, 3, 7, 1, 9, 0, 5}
	for _, c := range order {
		if !w.acceptCommit(c) {
			t.Fatalf("counter %d refused when arriving out of order", c)
		}
	}
	for _, c := range order {
		if w.accept(c) {
			t.Fatalf("counter %d accepted a second time", c)
		}
	}
	// The gaps are still open.
	for _, c := range []uint64{2, 4, 6, 8} {
		if !w.acceptCommit(c) {
			t.Fatalf("counter %d in a gap was refused", c)
		}
	}
}

func TestReplayWindowRejectsCountersBelowTheWindow(t *testing.T) {
	var w replayWindow
	w.commit(replayWindowBits * 4)

	if w.accept(0) {
		t.Fatal("a counter far below the window was accepted")
	}
	if w.accept(replayWindowBits * 3) {
		t.Fatal("a counter exactly one window back was accepted")
	}
	// The oldest counter still inside the window is fine.
	oldest := uint64(replayWindowBits*4) - replayWindowBits + 1
	if !w.accept(oldest) {
		t.Fatal("the oldest in-window counter was refused")
	}
}

// A large forward jump must clear the whole bitmap, or bits left over from a
// previous lap would refuse counters that have never been seen.
func TestReplayWindowClearsStaleBitsOnALargeJump(t *testing.T) {
	var w replayWindow
	for i := uint64(0); i < 500; i++ {
		w.commit(i)
	}
	jump := uint64(replayWindowBits * 10)
	if !w.acceptCommit(jump) {
		t.Fatal("a large forward jump was refused")
	}
	// These map onto the same ring slots as the counters committed above.
	for i := uint64(1); i < 400; i++ {
		c := jump - i
		if !w.accept(c) {
			t.Fatalf("counter %d refused because of a stale bit from an earlier lap", c)
		}
	}
}

// A jump shorter than the window clears only the slots it passes, and must
// leave the rest of the window intact.
func TestReplayWindowPreservesWindowOnASmallJump(t *testing.T) {
	var w replayWindow
	for i := uint64(0); i < 100; i++ {
		w.commit(i)
	}
	if !w.acceptCommit(150) {
		t.Fatal("a small forward jump was refused")
	}
	for i := uint64(0); i < 100; i++ {
		if w.accept(i) {
			t.Fatalf("counter %d lost its bit after a small jump", i)
		}
	}
	for i := uint64(100); i < 150; i++ {
		if !w.accept(i) {
			t.Fatalf("counter %d in the skipped range was refused", i)
		}
	}
}

func TestReplayWindowNearTheTopOfTheRange(t *testing.T) {
	var w replayWindow
	const high = ^uint64(0) - 10
	if !w.acceptCommit(high) {
		t.Fatal("a counter near the top of the range was refused")
	}
	if w.accept(high) {
		t.Fatal("that counter was accepted twice")
	}
	if w.accept(0) {
		t.Fatal("counter 0 was accepted after a counter near the top of the range")
	}
	if !w.acceptCommit(high + 1) {
		t.Fatal("the next counter was refused")
	}
}

package network

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A full local transmit queue is a lost packet. KCP ends a session for good on
// any error from WriteTo, so reporting ENOBUFS as one stopped a reverse pck
// tunnel dead the first time it was pushed hard — a 1 MB echo stalled at two
// thirds, every time. It has to look the way a UDP socket makes it look: sent.
func TestAFullTransmitQueueIsADropNotAnError(t *testing.T) {
	c := &pckConn{}
	for _, err := range []error{
		unix.ENOBUFS,
		&os.SyscallError{Syscall: "sendto", Err: unix.ENOBUFS},
		fmt.Errorf("write: %w", unix.EAGAIN),
	} {
		n, got := c.writeFailed(1200, err)
		if got != nil || n != 1200 {
			t.Errorf("%v: returned (%d, %v); want (1200, nil) so KCP retransmits instead of closing", err, n, got)
		}
	}
	if c.QueueDrops() != 3 {
		t.Errorf("counted %d drops, want 3", c.QueueDrops())
	}

	for _, err := range []error{unix.EPERM, errors.New("closed")} {
		if n, got := c.writeFailed(1200, err); got == nil || n != 0 {
			t.Errorf("%v was swallowed; only a full queue is", err)
		}
	}
}

// A link that is down is a lost packet as well, not the end of the session: the
// raw send path fails with ENETUNREACH once the kernel drops the route, and a
// packet socket with ENETDOWN. KCP closes the session on either if it is
// returned, and a reverse pck tunnel then never came back from a NIC down/up.
func TestADownLinkIsADropNotAnError(t *testing.T) {
	c := &pckConn{}
	errs := []error{
		unix.ENETDOWN,
		&os.SyscallError{Syscall: "sendmsg", Err: unix.ENETUNREACH},
		fmt.Errorf("write ip 0.0.0.0->10.99.0.2: %w", &os.SyscallError{Syscall: "sendmsg", Err: unix.EHOSTUNREACH}),
	}
	for _, err := range errs {
		if n, got := c.writeFailed(1200, err); got != nil || n != 1200 {
			t.Errorf("%v: returned (%d, %v); want (1200, nil)", err, n, got)
		}
	}
	if c.LinkDrops() != uint64(len(errs)) {
		t.Errorf("counted %d link drops, want %d", c.LinkDrops(), len(errs))
	}
	if c.QueueDrops() != 0 {
		t.Errorf("a down link was counted as a full queue (%d); path control would throttle for it", c.QueueDrops())
	}
}

// The packet socket reports ENETDOWN once when its interface goes down and is
// re-armed by the kernel on the way up. The carrier has to read on through it:
// kcp-go's listener stops reading for good at the first error, which left a
// reverse pck server deaf after a NIC down/up. Anything else still ends the read.
func TestReceiveReadsOnThroughALinkDown(t *testing.T) {
	c := &pckConn{}
	streak := 0
	start := time.Now()
	for i := 0; i < 3; i++ {
		err := &os.PathError{Op: "read", Path: "pck-rx", Err: unix.ENETDOWN}
		if !c.rxLinkDown(err, &streak) {
			t.Fatalf("ENETDOWN #%d was not absorbed", i)
		}
	}
	// 10 + 20 + 40 ms: the wait grows with each repeat, so a link that stays
	// down does not make the reader spin.
	if d := time.Since(start); d < 70*time.Millisecond {
		t.Errorf("three repeats took %v; the backoff did not grow", d)
	}
	streak = 100
	w := time.Now()
	c.rxLinkDown(unix.ENETDOWN, &streak)
	if d := time.Since(w); d > 2*pckLinkWaitMax {
		t.Errorf("one wait took %v, over the %v bound", d, pckLinkWaitMax)
	}
	for _, err := range []error{os.ErrClosed, unix.EBADF, unix.ENODEV, errors.New("boom")} {
		if c.rxLinkDown(err, &streak) {
			t.Errorf("%v was absorbed; only ENETDOWN is", err)
		}
	}
	c.closed.Store(true)
	if c.rxLinkDown(unix.ENETDOWN, &streak) {
		t.Error("ENETDOWN on a closed carrier was absorbed; the read must end")
	}
}

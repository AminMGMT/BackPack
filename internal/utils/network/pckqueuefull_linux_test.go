package network

import (
	"errors"
	"fmt"
	"net"
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
		n, got := c.writeFailed(nil, 1200, err)
		if got != nil || n != 1200 {
			t.Errorf("%v: returned (%d, %v); want (1200, nil) so KCP retransmits instead of closing", err, n, got)
		}
	}
	if c.QueueDrops() != 3 {
		t.Errorf("counted %d drops, want 3", c.QueueDrops())
	}

	for _, err := range []error{unix.EPERM, errors.New("closed")} {
		if n, got := c.writeFailed(nil, 1200, err); got == nil || n != 0 {
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
		if n, got := c.writeFailed(nil, 1200, err); got != nil || n != 1200 {
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
	l := loLink(t)
	streak := 0
	start := time.Now()
	for i := 0; i < 3; i++ {
		err := &os.PathError{Op: "read", Path: "pck-rx", Err: unix.ENETDOWN}
		if !c.rxLinkDown(l, err, &streak) {
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
	c.rxLinkDown(l, unix.ENETDOWN, &streak)
	if d := time.Since(w); d > 2*pckLinkWaitMax {
		t.Errorf("one wait took %v, over the %v bound", d, pckLinkWaitMax)
	}
	for _, err := range []error{os.ErrClosed, unix.EBADF, unix.ENODEV, errors.New("boom")} {
		if c.rxLinkDown(l, err, &streak) {
			t.Errorf("%v was absorbed; only ENETDOWN is", err)
		}
	}
	c.closed.Store(true)
	if c.rxLinkDown(l, unix.ENETDOWN, &streak) {
		t.Error("ENETDOWN on a closed carrier was absorbed; the read must end")
	}
}

// loLink is a link on the loopback interface, which is always there, so the
// receive path's "is the interface gone?" check says no.
func loLink(t *testing.T) *pckLink {
	t.Helper()
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		t.Skipf("no loopback interface: %v", err)
	}
	return &pckLink{egress: &pckEgress{Iface: lo}}
}

// An interface that was deleted answers a send with ENXIO (a packet socket
// naming an index that no longer exists) or ENODEV. That is a lost segment while
// the carrier reopens, not the end of KCP's session.
func TestADeletedInterfaceIsADropNotAnError(t *testing.T) {
	c := &pckConn{}
	c.closed.Store(true) // no relink in the background: only the verdict is tested
	for _, err := range []error{
		&os.SyscallError{Syscall: "sendto", Err: unix.ENXIO},
		fmt.Errorf("sendmmsg: %w", unix.ENODEV),
	} {
		if n, got := c.writeFailed(nil, 1200, err); got != nil || n != 1200 {
			t.Errorf("%v: returned (%d, %v); want (1200, nil)", err, n, got)
		}
	}
	if c.LinkDrops() != 2 {
		t.Errorf("counted %d link drops, want 2", c.LinkDrops())
	}
}

// A send that fails because relink closed the old socket under it is a lost
// segment too; the same failure on the link still in use is an error.
func TestASendOnASwappedOutLinkIsADrop(t *testing.T) {
	c := &pckConn{}
	old, cur := &pckLink{}, &pckLink{}
	c.link.Store(cur)
	if n, err := c.writeFailed(old, 1200, os.ErrClosed); err != nil || n != 1200 {
		t.Errorf("a send on the replaced link returned (%d, %v); want (1200, nil)", n, err)
	}
	if n, err := c.writeFailed(cur, 1200, os.ErrClosed); err == nil || n != 0 {
		t.Errorf("a closed socket on the current link was swallowed")
	}
}

func TestPckLinkGone(t *testing.T) {
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		t.Skip("no loopback interface")
	}
	if pckLinkGone(&pckLink{egress: &pckEgress{Iface: lo}}) {
		t.Error("lo is there, and was reported gone")
	}
	renamed := *lo
	renamed.Name = "bp-not-lo"
	if !pckLinkGone(&pckLink{egress: &pckEgress{Iface: &renamed}}) {
		t.Error("another interface now holds the index, and that was not reported as gone")
	}
	missing := net.Interface{Index: 1 << 30, Name: "bp-gone"}
	if !pckLinkGone(&pckLink{egress: &pckEgress{Iface: &missing}}) {
		t.Error("no interface has the index, and that was not reported as gone")
	}
}

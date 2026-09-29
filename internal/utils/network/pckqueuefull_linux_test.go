package network

import (
	"errors"
	"fmt"
	"os"
	"testing"

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

	for _, err := range []error{unix.EPERM, unix.ENETUNREACH, errors.New("closed")} {
		if n, got := c.writeFailed(1200, err); got == nil || n != 0 {
			t.Errorf("%v was swallowed; only a full queue is", err)
		}
	}
}

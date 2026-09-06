package transport

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/utils"
)

// A control channel that cannot be written to has to be given up on.
//
// This is the bug report: a tunnel that broke and stayed broken until it was
// restarted by hand. The log is thousands of "the queue is full, dropping a
// client" a second, interleaved with "a control channel is already established;
// refusing the claim", and then — minutes later — one
// "write: connection timed out" followed by a restart that fixed everything.
//
// The order of those is the whole story. The control channel's path had gone,
// but a write to it does not fail: it lands in the kernel's send buffer, and
// once that fills the write blocks until the kernel stops retransmitting, which
// is about fifteen minutes on Linux defaults. Throughout that window the server
// still held a control channel, so:
//
//   - it refused every attempt by the client to establish a new one, because
//     one was "already established";
//   - it could not ask for pool connections, because the request went into the
//     same buffer nobody was draining;
//   - and it therefore dropped every user connection with the pool empty.
//
// The tunnel was down and nothing it could do would bring it back. The operator
// restarting it was the only thing that ever cleared the state.
func TestAControlWriteGivesUpRatherThanWaitingForTheKernel(t *testing.T) {
	// A peer that accepts the connection and never reads a byte. An unbuffered
	// pipe is the same shape as a filled send buffer: the write cannot make
	// progress and nothing errors on its own.
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- utils.SendBinaryByteWithin(a, utils.SG_HB, 300*time.Millisecond) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a write to a peer that reads nothing reported success")
		}
		if !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "deadline") {
			t.Errorf("gave up for the wrong reason: %v", err)
		}
		if took := time.Since(start); took > 3*time.Second {
			t.Errorf("took %s to give up on a 300ms bound", took)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the control write is still blocked — the server would go on " +
			"believing it has a control channel, refusing the client's attempts " +
			"to replace it, for as long as the kernel takes")
	}
}

// The bound is long enough that a healthy channel is never given up on.
func TestAHealthyControlWriteIsNotDisturbed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	read := make(chan byte, 4)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		for {
			b, err := utils.ReceiveBinaryByte(c)
			if err != nil {
				return
			}
			read <- b
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	for i := 0; i < 3; i++ {
		if err := utils.SendBinaryByteWithin(conn, utils.SG_HB, controlWriteTimeout); err != nil {
			t.Fatalf("a healthy control channel refused a heartbeat: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		select {
		case b := <-read:
			if b != utils.SG_HB {
				t.Errorf("read %v, want a heartbeat", b)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("a heartbeat never arrived")
		}
	}

	// And the deadline is cleared afterwards, so a later write on the same
	// connection is not refused by a deadline that has since passed.
	time.Sleep(50 * time.Millisecond)
	if err := utils.SendBinaryByteWithin(conn, utils.SG_HB, controlWriteTimeout); err != nil {
		t.Errorf("a leftover deadline broke the next write: %v", err)
	}
}

// The bound is applied by every transport that holds a control channel, not
// just the one the report came in on.
func TestEveryTransportBoundsItsControlWrites(t *testing.T) {
	for _, f := range []string{"tcp.go", "tcpmux.go", "quic.go", "kcp.go", "udp.go"} {
		src := readTransportSource(t, f)
		if strings.Contains(src, "utils.SendBinaryByte(s.controlChannel.Get()") {
			t.Errorf("%s still writes to its control channel with no bound — the "+
				"tunnel it carries can be held down by a peer that stopped reading", f)
		}
	}
	for _, f := range []string{"ws.go", "wsmux.go"} {
		src := readTransportSource(t, f)
		if strings.Contains(src, "s.controlChannel.Get().WriteMessage(") {
			t.Errorf("%s still writes to its control channel with no bound", f)
		}
	}
}

func readTransportSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("./" + name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}

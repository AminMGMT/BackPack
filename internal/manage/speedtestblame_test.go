package manage

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
)

// The report this exists for: "the speed test does not work even though the
// server is connected and there is a tunnel — it says you have to go to the
// server and turn on receive."
//
// They did, and it changed nothing, because the other server was never
// involved. A forwarding measurement dials a port on *this* machine: the
// tunnel's own listener, which is what carries the bytes across. A refused
// connection there means nothing is listening here — the tunnel is stopped, or
// never bound the port — and the far end had no part in it. The failure was
// reported with the receiver hint appended to it regardless, so every local
// fault read as a remote one and sent the operator to the wrong machine.

func TestARefusedLocalPortDoesNotBlameTheOtherServer(t *testing.T) {
	err := refusedLocally("nl-ws", 8080)
	msg := err.Error()

	for _, wrong := range []string{"receiver", "other server", "Speed Test"} {
		if strings.Contains(msg, wrong) {
			t.Errorf("a refusal on this machine's own port mentions %q: %s", wrong, msg)
		}
	}
	if !strings.Contains(msg, "8080") {
		t.Errorf("the message does not say which port: %s", msg)
	}
	if !strings.Contains(msg, "this server") {
		t.Errorf("the message does not say the fault is on this server: %s", msg)
	}
}

// The classification has to recognise the one error that means it, and not
// swallow the others — a timeout or a reset is a different fault with a
// different answer.
func TestOnlyARefusalIsTreatedAsLocal(t *testing.T) {
	if !isRefused(syscall.ECONNREFUSED) {
		t.Error("a refused connection is not recognised")
	}
	if !isRefused(&os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}) {
		t.Error("a refusal wrapped by the net package is not recognised")
	}
	for _, other := range []error{syscall.ETIMEDOUT, syscall.ECONNRESET, errors.New("eof")} {
		if isRefused(other) {
			t.Errorf("%v was treated as a refused connection", other)
		}
	}
}

// And the measurement has to make the call at all.
func TestTheMeasurementClassifiesTheRefusal(t *testing.T) {
	b, err := os.ReadFile("speedtestapi.go")
	if err != nil {
		t.Fatalf("cannot read speedtestapi.go: %v", err)
	}
	if !strings.Contains(string(b), "refusedLocally(") {
		t.Error("RunSpeedTest no longer distinguishes a refusal on this machine's own " +
			"port from a far end that is not sinking, so a stopped tunnel reads as " +
			"somebody else's fault")
	}

	h, err := os.ReadFile("../webui/handlers_speedtest.go")
	if err != nil {
		t.Fatalf("cannot read handlers_speedtest.go: %v", err)
	}
	src := string(h)
	i := strings.Index(src, "check that the receiver is running on the")
	if i < 0 {
		return // the hint is gone entirely, which is also fine
	}
	// It may still be offered — but only as one branch, never appended to
	// every failure the measurement can return.
	before := src[max(0, i-700):i]
	if !strings.Contains(before, "switch {") {
		t.Error("the receiver hint is added to every speed-test failure again, so a " +
			"fault on this server is reported as a fault on the other one")
	}
}

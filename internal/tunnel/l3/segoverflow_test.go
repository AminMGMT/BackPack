package l3

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	wgtun "golang.zx2c4.com/wireguard/tun"
)

// A run that held more segments than there were buffers is a short read.
//
// From the field: a layer-3 tunnel logging "reading from bp0: too many
// segments" and restarting, over and over. The pump took any error from the
// device as the device having failed, so a condition that costs a few packets
// cost the whole tunnel instead — every few seconds, for as long as the traffic
// that caused it kept flowing.
func TestTooManySegmentsIsAShortReadNotAFailure(t *testing.T) {
	kept, overflow := segmentOverflow(127, wgtun.ErrTooManySegments)
	if !overflow {
		t.Fatal("too many segments was not recognised, so the pump will tear the tunnel down")
	}
	if kept != 127 {
		t.Fatalf("kept %d packets, want the 127 the device managed to split", kept)
	}
}

// Wrapped, because the device does not promise to hand the sentinel back bare.
func TestTooManySegmentsIsRecognisedThroughAWrap(t *testing.T) {
	wrapped := fmt.Errorf("reading from bp0: %w", wgtun.ErrTooManySegments)
	if _, overflow := segmentOverflow(3, wrapped); !overflow {
		t.Fatal("a wrapped ErrTooManySegments was not recognised")
	}
}

// A count of -1 is what the split returns when it had no room for even one
// segment. Handing that to a loop bound would be worse than useless.
func TestTooManySegmentsNeverReportsANegativeCount(t *testing.T) {
	kept, overflow := segmentOverflow(-1, wgtun.ErrTooManySegments)
	if !overflow {
		t.Fatal("not recognised")
	}
	if kept != 0 {
		t.Fatalf("kept %d, want 0", kept)
	}
}

// Every other error still is one. A device that has actually stopped working
// must still bring the tunnel down to be rebuilt.
func TestRealDeviceErrorsAreStillFailures(t *testing.T) {
	for _, err := range []error{io.EOF, errors.New("device gone"), nil} {
		if _, overflow := segmentOverflow(0, err); overflow {
			t.Fatalf("%v was treated as a survivable short read", err)
		}
	}
}

// The report is rate-limited, and the first one always gets through.
func TestOverflowReportSpeaksOnceThenHoldsItsPeace(t *testing.T) {
	r := reportEvery{every: time.Minute}
	base := time.Now()

	if n, say := r.allow(base); !say || n != 1 {
		t.Fatalf("first report: n=%d say=%v, want 1/true", n, say)
	}
	if n, say := r.allow(base.Add(time.Second)); say || n != 2 {
		t.Fatalf("second report a second later: n=%d say=%v, want 2/false", n, say)
	}
	if n, say := r.allow(base.Add(90 * time.Second)); !say || n != 3 {
		t.Fatalf("report after the window: n=%d say=%v, want 3/true", n, say)
	}
}

// The Linux device must actually route its reads through the decision above.
//
// Read is behind a build tag and needs a real TUN interface and root, so no
// test on a developer's machine reaches it — which is exactly how a fix can sit
// in a package, fully tested, while the one caller that matters never calls it.
// This asserts the wiring from the source instead.
func TestTheLinuxDeviceUsesTheOverflowDecision(t *testing.T) {
	src, err := os.ReadFile("tun_linux.go")
	if err != nil {
		t.Fatalf("reading tun_linux.go: %v", err)
	}
	const sig = "func (t *tunDevice) Read(bufs [][]byte, sizes []int) (int, error) {"
	start := strings.Index(string(src), sig)
	if start < 0 {
		t.Fatalf("could not find %s — if it was renamed, point this test at the new name", sig)
	}
	body := string(src)[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "segmentOverflow(") {
		t.Errorf("tunDevice.Read does not go through segmentOverflow, so a run with more "+
			"segments than buffers will read as the device failing and tear the tunnel "+
			"down — the restart loop reported from the field. Body:\n%s", body)
	}
}

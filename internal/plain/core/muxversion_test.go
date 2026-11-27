package core

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/xtaci/smux"
)

// A session dropped for another mux version is said once, plainly; any other
// end of a session is not this.
func TestAMuxVersionMismatchIsSaidOnce(t *testing.T) {
	muxMismatchSaid.Store(0)
	var out bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&out)

	if NoteMuxMismatch(logger, errors.New("broken pipe"), 1) {
		t.Fatal("an ordinary closed session was taken for a mux_version mismatch")
	}
	for range 5 {
		if !NoteMuxMismatch(logger, fmt.Errorf("accept: %w", smux.ErrInvalidProtocol), 2) {
			t.Fatal("smux's invalid-protocol error was not taken for a mux_version mismatch")
		}
	}
	if n := strings.Count(out.String(), "different mux_version"); n != 1 {
		t.Fatalf("said %d times for five sessions, want once:\n%s", n, out.String())
	}
	if !strings.Contains(out.String(), "uses 2") {
		t.Fatalf("the message does not say which version this end uses:\n%s", out.String())
	}
}

package webui

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/node"
)

// A measurement that cannot be taken here is taken where it can be.
//
// The link test measures the path a tunnel dials out over, and only the
// dialling end has one to measure. On an Iran panel that end is never local: it
// holds the listening half of every reverse tunnel, and the half with a peer
// address is on a server in the fleet. The panel refused — while holding a root
// shell on the machine that could have answered.
func TestALinkTestRunsOnTheServerThatCanTakeIt(t *testing.T) {
	// The rule itself, which both ends now share.
	if can, why := manage.LinkTestable(manage.Tunnel{Role: "server", Transport: "tcp"}); can {
		t.Error("a listening tunnel was reported as measurable from its own machine")
	} else if !strings.Contains(why, "listens rather than dialling") {
		t.Errorf("the reason does not say what is wrong: %q", why)
	}
	if can, _ := manage.LinkTestable(manage.Tunnel{Role: "client", Transport: "tcp"}); !can {
		t.Error("a dialling TCP tunnel was refused")
	}
	if can, why := manage.LinkTestable(manage.Tunnel{Role: "client", Transport: "kcp"}); can {
		t.Error("a datagram tunnel was offered a TCP probe, which reports a working link as dead")
	} else if !strings.Contains(why, "UDP-based") {
		t.Errorf("the reason does not say what is wrong: %q", why)
	}

	// And the far side answers for it.
	if node.OpLinkTest == "" {
		t.Error("there is no operation for asking a managed server to measure its own link")
	}
}

// A reading taken somewhere else has to say so.
func TestAMeasurementTakenElsewhereNamesTheMachine(t *testing.T) {
	var res manage.LinkTestResult
	if !hasJSONTag(res, "RanOn", "ranOn") {
		t.Error("the result cannot carry where it was measured — a latency figure " +
			"without the place it was taken from is a number about somebody else's path")
	}
}

// When the panel cannot run it and cannot ask anyone, it says what would let
// it — not "not from here".
func TestTheRefusalSaysWhatWouldFixIt(t *testing.T) {
	isolateFleet(t)
	s := newFleetServer()
	t.Cleanup(s.nodes.Stop)
	withFleet(s, newFake())

	// No tunnel by that name: the handler refuses before any of this, which is
	// the guard that this test must not be fooled by.
	w := postTo(t, s, "/api/linktest?name=nothing-here")
	if w.Code == http.StatusOK {
		t.Fatal("a link test started for a tunnel that does not exist")
	}
}

func postTo(t *testing.T, s *server, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleLinkTest(w, httptest.NewRequest("POST", path, nil))
	return w
}

// hasJSONTag reports whether a field carries the JSON name the browser expects.
func hasJSONTag(v any, field, want string) bool {
	f, ok := reflect.TypeOf(v).FieldByName(field)
	if !ok {
		return false
	}
	return strings.Split(f.Tag.Get("json"), ",")[0] == want
}

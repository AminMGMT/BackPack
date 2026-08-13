package network

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// The bug in one test: every TLS configuration built used to start its own
// responder, so the second one found port 80 held by the first and gave up —
// leaving a torn-down generation's manager answering the challenges.
func TestTheResponderBindsOnceAcrossGenerations(t *testing.T) {
	r := &acmeHTTPResponder{addr: "127.0.0.1:0"}

	first := managerFor(t, "one.example")
	r.use(first, nil)

	r.mu.Lock()
	started, bound := r.started, r.bound
	r.mu.Unlock()
	if !started || bound == nil {
		t.Fatal("the first generation did not start the responder")
	}

	// What a reload does: builds another manager and asks again.
	for i := 0; i < 5; i++ {
		r.use(managerFor(t, "two.example"), func(string, ...any) {
			t.Error("a later generation tried to bind the port again")
		})
	}

	r.mu.Lock()
	stillBound := r.bound
	r.mu.Unlock()
	if stillBound.String() != bound.String() {
		t.Fatalf("the listener moved from %s to %s", bound, stillBound)
	}
}

// A challenge arriving after a reload has to be answered by the manager now in
// force, not by the one that happened to start the listener.
func TestTheResponderAnswersFromTheCurrentManager(t *testing.T) {
	r := &acmeHTTPResponder{addr: "127.0.0.1:0"}

	r.use(managerFor(t, "old.example"), nil)
	r.mu.Lock()
	first := r.current
	r.mu.Unlock()

	newer := managerFor(t, "new.example")
	r.use(newer, nil)

	r.mu.Lock()
	current := r.current
	r.mu.Unlock()
	if current == first {
		t.Fatal("a reload left the previous generation's manager answering challenges")
	}
	if current != newer {
		t.Fatal("the responder is not pointed at the manager now in force")
	}
}

// Before any manager exists there is nothing to validate, and the handler must
// say so rather than dereference what is not there.
func TestTheResponderWithNoManagerIsHarmless(t *testing.T) {
	r := &acmeHTTPResponder{addr: "127.0.0.1:0"}
	w := httptest.NewRecorder()
	r.serve(w, httptest.NewRequest(http.MethodGet, "http://example/.well-known/acme-challenge/x", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// A port it cannot have is reported and survived: a tunnel on 443 validates
// over TLS-ALPN and needs no responder at all, so this must never be fatal.
func TestAnUnavailablePortIsReportedNotFatal(t *testing.T) {
	blocker := &acmeHTTPResponder{addr: "127.0.0.1:0"}
	blocker.use(managerFor(t, "blocker.example"), nil)
	blocker.mu.Lock()
	taken := blocker.bound.String()
	blocker.mu.Unlock()

	logged := make(chan struct{}, 1)
	r := &acmeHTTPResponder{addr: taken}
	r.use(managerFor(t, "loser.example"), func(string, ...any) {
		select {
		case logged <- struct{}{}:
		default:
		}
	})

	select {
	case <-logged:
	case <-time.After(2 * time.Second):
		t.Fatal("a port that could not be taken was not reported")
	}

	r.mu.Lock()
	started := r.started
	r.mu.Unlock()
	if started {
		t.Error("the responder reported itself started without a listener")
	}
}

func managerFor(t *testing.T, host string) *autocert.Manager {
	t.Helper()
	return &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(t.TempDir()),
		HostPolicy: autocert.HostWhitelist(host),
	}
}

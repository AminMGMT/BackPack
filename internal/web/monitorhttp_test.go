package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The bind has to default to loopback with nothing configured: a caller that
// never set it is a caller that never thought about it, and this page has no
// authentication to fall back on.
func TestMonitorListenAddrDefaultsToLoopback(t *testing.T) {
	withBind(t, "")

	for _, addr := range []string{":2060", "0.0.0.0:2060", "[::]:2060"} {
		if got := monitorListenAddr(addr); got != "127.0.0.1:2060" {
			t.Errorf("monitorListenAddr(%q) = %q, want 127.0.0.1:2060", addr, got)
		}
	}
}

// An operator who asks for it gets it, including the old every-interface
// behaviour — this is a default, not a prohibition.
func TestMonitorListenAddrHonoursTheConfiguredBind(t *testing.T) {
	withBind(t, "0.0.0.0")
	if got := monitorListenAddr(":2060"); got != "0.0.0.0:2060" {
		t.Fatalf("monitorListenAddr(\":2060\") = %q, want 0.0.0.0:2060", got)
	}

	withBind(t, "10.0.0.5")
	if got := monitorListenAddr(":2060"); got != "10.0.0.5:2060" {
		t.Fatalf("monitorListenAddr(\":2060\") = %q, want 10.0.0.5:2060", got)
	}
}

// An address that already names a host was decided somewhere else; and one
// that does not parse belongs to the listener's error message, not to a guess
// made here.
func TestMonitorListenAddrLeavesDecidedAddressesAlone(t *testing.T) {
	withBind(t, "127.0.0.1")
	for _, addr := range []string{"192.168.1.4:2060", "[::1]:2060", "not-an-address"} {
		if got := monitorListenAddr(addr); got != addr {
			t.Errorf("monitorListenAddr(%q) = %q, want it unchanged", addr, got)
		}
	}
}

func TestMonitorIsPublic(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1:2060": false,
		"[::1]:2060":     false,
		"0.0.0.0:2060":   true,
		"[::]:2060":      true,
		":2060":          true,
		"10.0.0.5:2060":  true,
		"not-an-address": false,
	}
	for addr, want := range tests {
		if got := monitorIsPublic(addr); got != want {
			t.Errorf("monitorIsPublic(%q) = %v, want %v", addr, got, want)
		}
	}
}

// A read-only page answers reads. Anything else must be refused before it
// reaches a handler that never considered it.
func TestMonitorHTTPAnswersReadsAndRefusesTheRest(t *testing.T) {
	reached := 0
	h := monitorHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusNoContent)
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://localhost/stats", nil))
	if w.Code != http.StatusNoContent || reached != 1 {
		t.Fatalf("GET: status=%d handler reached %d times", w.Code, reached)
	}
	for _, header := range []string{"Cache-Control", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if w.Header().Get(header) == "" {
			t.Errorf("missing %s on a served response", header)
		}
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "http://localhost/stats", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status=%d, want 405", method, w.Code)
		}
		if allow := w.Header().Get("Allow"); allow != "GET, HEAD" {
			t.Errorf("%s: Allow=%q, want \"GET, HEAD\"", method, allow)
		}
	}
	if reached != 1 {
		t.Fatalf("the handler ran %d times; only the GET should have reached it", reached)
	}
}

func TestMonitorServerHasLimits(t *testing.T) {
	srv := newMonitorServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout != 5*time.Second ||
		srv.ReadTimeout != 10*time.Second ||
		srv.WriteTimeout != 15*time.Second ||
		srv.IdleTimeout != 30*time.Second ||
		srv.MaxHeaderBytes != 16<<10 {
		t.Fatalf("the monitor server was built without its limits: %+v", srv)
	}
}

// withBind sets the process-wide bind for one test and puts it back after.
func withBind(t *testing.T, host string) {
	t.Helper()
	previous, _ := monitorBind.Load().(string)
	SetMonitorBind(host)
	t.Cleanup(func() { SetMonitorBind(previous) })
}

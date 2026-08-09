package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLocalMonitorAddr(t *testing.T) {
	tests := map[string]string{
		":8080":         "127.0.0.1:8080",
		"0.0.0.0:8080":  "127.0.0.1:8080",
		"[::]:8080":     "127.0.0.1:8080",
		"10.0.0.2:8080": "10.0.0.2:8080",
		"invalid":       "invalid",
	}
	for input, want := range tests {
		if got := localMonitorAddr(input); got != want {
			t.Errorf("localMonitorAddr(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMonitorHTTPBoundaries(t *testing.T) {
	called := 0
	h := monitorHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusNoContent)
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://localhost/stats", nil))
	if w.Code != http.StatusNoContent || called != 1 {
		t.Fatalf("GET status=%d called=%d", w.Code, called)
	}
	for _, header := range []string{"Cache-Control", "Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if w.Header().Get(header) == "" {
			t.Errorf("missing security header %s", header)
		}
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "http://localhost/stats", nil))
	if w.Code != http.StatusMethodNotAllowed || called != 1 || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST status=%d called=%d allow=%q", w.Code, called, w.Header().Get("Allow"))
	}
}

func TestMonitorHTTPServerLimits(t *testing.T) {
	srv := newMonitorHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout != 5*time.Second || srv.ReadTimeout != 10*time.Second ||
		srv.WriteTimeout != 15*time.Second || srv.IdleTimeout != 30*time.Second || srv.MaxHeaderBytes != 16<<10 {
		t.Fatalf("unexpected monitor HTTP limits: %+v", srv)
	}
}

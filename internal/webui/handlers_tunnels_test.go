package webui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/manage"
)

func TestRestartWaitsForServiceToBecomeActive(t *testing.T) {
	var restarted, waited string
	err := restartAndWait("backpack-client.service",
		func(service string) error { restarted = service; return nil },
		func(service string, timeout time.Duration) bool {
			waited = service
			if timeout != 10*time.Second {
				t.Errorf("timeout = %s, want 10s", timeout)
			}
			return true
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if restarted != "backpack-client.service" || waited != restarted {
		t.Fatalf("restarted %q and waited for %q", restarted, waited)
	}
}

func TestRestartFailsWhenServiceDoesNotBecomeActive(t *testing.T) {
	err := restartAndWait("backpack-client.service",
		func(string) error { return nil },
		func(string, time.Duration) bool { return false },
	)
	if err == nil {
		t.Fatal("an inactive service was reported as restarted")
	}
}

func TestTunnelRestartRouteRequiresPanelSession(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	registration := `mux.HandleFunc("/api/tunnels/restart", srv.requireAuth(srv.handleTunnelRestart))`
	if !strings.Contains(string(src), registration) {
		t.Fatal("restart endpoint is not registered behind full panel authentication")
	}
}

func TestTunnelRestartUsesConfiguredServiceName(t *testing.T) {
	tunnels := []manage.Tunnel{{Name: "iran-main", Service: "backpack-iran-main.service"}}
	var restarted string

	r := httptest.NewRequest(http.MethodPost, "/api/tunnels/restart", strings.NewReader("name=iran-main"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handleTunnelRestartWith(w, r, func() []manage.Tunnel { return tunnels }, func(service string) error {
		restarted = service
		return nil
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if restarted != "backpack-iran-main.service" {
		t.Fatalf("restarted %q, want the configured tunnel service", restarted)
	}
	if !strings.Contains(w.Body.String(), `"status":"restarted"`) {
		t.Fatalf("response does not report success: %s", w.Body.String())
	}
}

func TestTunnelRestartRejectsUnknownName(t *testing.T) {
	called := false
	r := httptest.NewRequest(http.MethodPost, "/api/tunnels/restart", strings.NewReader("name=../../ssh"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handleTunnelRestartWith(w, r,
		func() []manage.Tunnel { return []manage.Tunnel{{Name: "known", Service: "backpack-known.service"}} },
		func(string) error { called = true; return nil },
	)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
	if called {
		t.Fatal("restart was called for an unknown tunnel name")
	}
}

func TestTunnelRestartRequiresPostAndName(t *testing.T) {
	list := func() []manage.Tunnel { return nil }
	restart := func(string) error { t.Fatal("restart should not be called"); return nil }

	for _, tc := range []struct {
		method string
		body   string
		want   int
	}{
		{http.MethodGet, "", http.StatusMethodNotAllowed},
		{http.MethodPost, "", http.StatusBadRequest},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(tc.method, "/api/tunnels/restart", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		handleTunnelRestartWith(w, r, list, restart)
		if w.Code != tc.want {
			t.Errorf("%s status = %d, want %d", tc.method, w.Code, tc.want)
		}
	}
}

func TestTunnelRestartReportsSystemdFailure(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/tunnels/restart", strings.NewReader("name=client"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handleTunnelRestartWith(w, r,
		func() []manage.Tunnel { return []manage.Tunnel{{Name: "client", Service: "backpack-client.service"}} },
		func(string) error { return errors.New("systemd unavailable") },
	)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "systemd unavailable") {
		t.Fatalf("internal systemd detail leaked to the browser: %s", w.Body.String())
	}
}

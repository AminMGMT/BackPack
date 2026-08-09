package transport

import (
	"net/http"
	"testing"
	"time"
)

func TestWebSocketHTTPServerHasBoundaryTimeouts(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	srv := &http.Server{Addr: "127.0.0.1:0", Handler: handler}
	hardenWebSocketHTTPServer(srv)
	if srv.Handler == nil || srv.Addr != "127.0.0.1:0" {
		t.Fatal("server lost its handler or address")
	}
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout = %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.WriteTimeout != 15*time.Second {
		t.Fatalf("WriteTimeout = %v, want 15s", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 60*time.Second {
		t.Fatalf("IdleTimeout = %v, want 60s", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 16<<10 {
		t.Fatalf("MaxHeaderBytes = %d, want %d", srv.MaxHeaderBytes, 16<<10)
	}
}

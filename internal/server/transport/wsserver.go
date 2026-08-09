package transport

import (
	"net/http"
	"time"
)

func hardenWebSocketHTTPServer(srv *http.Server) {
	srv.ReadHeaderTimeout = 10 * time.Second
	srv.WriteTimeout = 15 * time.Second
	srv.IdleTimeout = 60 * time.Second
	srv.MaxHeaderBytes = 16 << 10
}

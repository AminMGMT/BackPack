package localproxy

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/socks"
)

// A minimal HTTP proxy: CONNECT for HTTPS (and any TCP the client tunnels
// through it) plus plain HTTP forwarding. It is the same idea as the SOCKS5
// proxy for clients that speak HTTP-proxy instead — browsers, and tools that
// only take an http_proxy setting.

// serveHTTP runs the HTTP proxy on addr until ctx is cancelled. auth is nil for
// no-auth (loopback behind an authenticated tunnel) or a checker for Basic
// Proxy-Authorization.
func serveHTTP(ctx context.Context, addr string, auth socks.AuthFunc) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: &httpProxy{auth: auth},
	}
	go func() { <-ctx.Done(); srv.Close() }()
	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

type httpProxy struct{ auth socks.AuthFunc }

func (p *httpProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.authorized(r) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="backpack"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

// authorized checks Basic Proxy-Authorization when a checker is configured.
func (p *httpProxy) authorized(r *http.Request) bool {
	if p.auth == nil {
		return true
	}
	parts := strings.SplitN(r.Header.Get("Proxy-Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Basic") {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	up := strings.SplitN(string(raw), ":", 2)
	if len(up) != 2 {
		return false
	}
	return p.auth(up[0], up[1])
}

// handleConnect tunnels raw bytes to the requested host (HTTPS and anything
// else over CONNECT): hijack the client socket, dial the target, pipe both.
func (p *httpProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	dst, err := net.DialTimeout("tcp", r.Host, 15*time.Second)
	if err != nil {
		http.Error(w, "cannot reach destination", http.StatusBadGateway)
		return
	}
	defer dst.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()

	client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	pipe(client, dst)
}

// handleHTTP forwards a plain (non-CONNECT) HTTP request and streams the reply.
func (p *httpProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	r.RequestURI = ""
	r.Header.Del("Proxy-Authorization")
	r.Header.Del("Proxy-Connection")

	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// pipe copies both directions and returns when both are done, half-closing so
// buffered data flushes cleanly.
func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}

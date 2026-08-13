package web

import (
	stdnet "net"
	"net/http"
	"sync/atomic"
	"time"
)

// Where the monitor page listens, and what it will answer.
//
// This page has no authentication and never has had any. What it serves is the
// host's CPU, memory, disk, swap and network counters, the tunnel's status, its
// total traffic and — with the sniffer on — the usage of every forwarded port.
// It was bound to every interface, so on a server with a public address that
// was a live readout of the machine handed to anything that connected, and the
// only thing standing between a port scan and it was the port number.
//
// So it binds to loopback unless the operator says otherwise, and the way to
// reach it from elsewhere is the way the pprof endpoint is already reached:
//
//	ssh -L 2060:127.0.0.1:2060 root@server
//
// One process runs one tunnel, so like the socket tuning this is process-wide
// and set once before anything listens.

// monitorBind is the host part the monitor listens on. Empty means loopback:
// the zero value has to be the safe one, because a caller that never sets it
// is a caller that never thought about it.
var monitorBind atomic.Value

// SetMonitorBind records the address the monitor page should listen on. The
// engine calls it once, before any listener exists. An empty host, or one that
// means "everything", is left as the caller wrote it — refusing to serve
// publicly is a decision for the operator, not for this function.
func SetMonitorBind(host string) { monitorBind.Store(host) }

// MonitorBind reports the configured host, defaulting to loopback.
func MonitorBind() string {
	host, _ := monitorBind.Load().(string)
	if host == "" {
		return "127.0.0.1"
	}
	return host
}

// monitorListenAddr rewrites the wildcard address the transports build
// (":2060") to sit on the configured host. An address that already names a
// host is left alone, and anything that does not parse is passed through so
// the listener reports the real error rather than this guessing at one.
func monitorListenAddr(addr string) string {
	host, port, err := stdnet.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host != "" && !isWildcardHost(host) {
		return addr
	}
	return stdnet.JoinHostPort(MonitorBind(), port)
}

// isWildcardHost reports whether a host means "every interface" — an empty
// string, or an unspecified address written either way round.
func isWildcardHost(host string) bool {
	if host == "" {
		return true
	}
	ip := stdnet.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

// monitorIsPublic reports whether the configured bind reaches beyond this
// host, so startup can say so out loud rather than leaving it to be noticed.
func monitorIsPublic(addr string) bool {
	host, _, err := stdnet.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if isWildcardHost(host) {
		return true
	}
	ip := stdnet.ParseIP(host)
	return ip != nil && !ip.IsLoopback()
}

// monitorHTTP is the boundary around the monitor's handlers: a read-only page
// answers reads, and says so for anything else rather than passing an
// unexpected method to a handler that never considered one.
func monitorHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// newMonitorServer builds the monitor's HTTP server with the limits the old
// one had none of. Without them a handful of connections that open and then go
// quiet hold their goroutines and buffers for as long as the process runs,
// which on the page that exists to report the host's health is a poor joke.
func newMonitorServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

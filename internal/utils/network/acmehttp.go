package network

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// The HTTP-01 responder is one listener for the life of the process.
//
// It used to be started with a bare `go serveACMEHTTP(m, …)` every time a TLS
// configuration was built, and nothing ever stopped it. One process builds one
// more of those on every reload, so the second reload found port 80 held by
// the responder the first run had left behind. It logged that it could not
// have the port and carried on — and the manager still answering on 80 was the
// old one, belonging to a generation that had been torn down, with the
// configuration and the cache directory it was built with rather than the ones
// now in force. Renewal then depended on TLS-ALPN, which only works when the
// tunnel is on 443; off 443 it simply stopped renewing, silently, from the
// second reload onwards.
//
// One listener, started once, answering from whichever manager is current, is
// the whole fix. The responder holds no state of its own: everything it needs
// for a challenge lives in the manager, so pointing it at the new one is all a
// reload has to do.

// acmeResponderAddr is where the HTTP-01 challenge is answered. Port 80 is not
// a choice — it is the port the ACME specification requires for this challenge
// type, so there is nothing to configure here.
const acmeResponderAddr = ":80"

var acmeResponder = &acmeHTTPResponder{addr: acmeResponderAddr}

type acmeHTTPResponder struct {
	// addr is a field only so a test can put the listener somewhere it is
	// allowed to bind; nothing outside a test ever sets it.
	addr string

	mu      sync.Mutex
	started bool
	current *autocert.Manager
	// bound is where the listener actually ended up, for tests that need to
	// reach it.
	bound net.Addr
}

// use points the responder at m, starting the listener the first time.
//
// Reporting that the port could not be taken stays best effort, as it was: a
// tunnel on 443 validates over TLS-ALPN and needs no responder at all, and
// refusing to start a working tunnel over a port that may not be needed would
// be the worse failure.
func (a *acmeHTTPResponder) use(m *autocert.Manager, logf func(string, ...any)) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.current = m
	if a.started {
		// A previous generation already has the listener; it now answers from
		// this manager. Rebinding would only fail against ourselves.
		return
	}

	listener, err := net.Listen("tcp", a.addr)
	if err != nil {
		logACMEPort(logf, err)
		return
	}
	a.started = true
	a.bound = listener.Addr()

	server := &http.Server{
		Handler:           http.HandlerFunc(a.serve),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			logACMEPort(logf, err)
			a.mu.Lock()
			a.started = false
			a.mu.Unlock()
		}
	}()
}

// serve answers a challenge from whichever manager is current at the time the
// request arrives, rather than from the one that happened to start the
// listener.
func (a *acmeHTTPResponder) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	m := a.current
	a.mu.Unlock()

	if m == nil {
		http.NotFound(w, r)
		return
	}
	m.HTTPHandler(nil).ServeHTTP(w, r)
}

func logACMEPort(logf func(string, ...any), err error) {
	if logf == nil {
		return
	}
	logf("ACME HTTP-01 responder could not use port 80 (%v); "+
		"validation will rely on TLS-ALPN, which needs the tunnel to be on port 443", err)
}

package network

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// TLS configuration for the wss and wssmux transports.
//
// Two ways to get a certificate:
//
//   - A file pair on disk, which is the self-signed certificate Backpack
//     generates. This works anywhere, including on a bare IP with no domain.
//   - Let's Encrypt, when the tunnel has a real domain name pointing at it.
//
// The second is worth having for a reason that is not really about encryption:
// the client is our own code and skips verification either way. It is about
// what the connection looks like from outside. Genuine HTTPS on port 443 never
// presents a self-signed certificate, so one is a distinguishing mark on a
// route where being distinguishable is the problem. A real certificate removes
// it, and is also what a CDN in front of the tunnel requires.

// TLSSettings describes how a listener should obtain its certificate.
type TLSSettings struct {
	// CertFile and KeyFile point at a PEM pair. Used when ACMEDomain is empty.
	CertFile string
	KeyFile  string

	// ACMEDomain, when set, switches to Let's Encrypt for that domain. It must
	// resolve to this server.
	ACMEDomain string
	// ACMEEmail is optional; Let's Encrypt uses it for expiry warnings.
	ACMEEmail string
	// ACMECacheDir is where issued certificates and the account key are kept.
	// Losing it only means re-issuing, but doing that repeatedly hits rate
	// limits, so it should be on persistent storage.
	ACMECacheDir string
}

// UsesACME reports whether these settings request a Let's Encrypt certificate.
func (s TLSSettings) UsesACME() bool { return s.ACMEDomain != "" }

// TLSLease couples a TLS configuration to the process-wide ACME responder
// registration which serves it. Close must be called when the listener stops;
// file-backed configurations use the same API with a no-op release.
type TLSLease struct {
	Config  *tls.Config
	onClose func()
	once    sync.Once
}

func (l *TLSLease) Close() error {
	if l != nil {
		l.once.Do(func() {
			if l.onClose != nil {
				l.onClose()
			}
		})
	}
	return nil
}

// ServerTLSConfig builds a leased TLS configuration for a listener.
//
// Both paths go through GetCertificate rather than a fixed certificate, so a
// renewed certificate is picked up without restarting the tunnel. That matters
// more than it sounds: Let's Encrypt certificates last 90 days, and a scheme
// that needed a restart would mean a scheduled interruption every couple of
// months on every tunnel using one.
func ServerTLSConfig(s TLSSettings, logf func(string, ...any)) (*TLSLease, error) {
	var lease *TLSLease
	var err error
	if s.UsesACME() {
		lease, err = acmeTLSConfig(s, logf)
	} else {
		var cfg *tls.Config
		cfg, err = fileTLSConfig(s.CertFile, s.KeyFile)
		lease = &TLSLease{Config: cfg}
	}
	if err != nil {
		return nil, err
	}
	pinHTTP11ALPN(lease.Config)
	return lease, nil
}

// HTTPSConfig builds a leased TLS configuration for an ordinary HTTPS server —
// the web panel. It offers the same two ways to get a certificate as the tunnel
// listeners, and deliberately skips the ALPN pinning they need: that exists so
// a WebSocket upgrade can never be negotiated away to HTTP/2, and a panel has
// no upgrade to protect.
//
// Renewal is not something the caller has to arrange. Both paths hand back a
// config whose certificate is resolved per handshake, so an ACME certificate
// reissued at the 60-day mark of its 90-day life is served from the next
// connection onward without restarting the panel.
func HTTPSConfig(s TLSSettings, logf func(string, ...any)) (*TLSLease, error) {
	if s.UsesACME() {
		return acmeTLSConfig(s, logf)
	}
	cfg, err := fileTLSConfig(s.CertFile, s.KeyFile)
	if err != nil {
		return nil, err
	}
	return &TLSLease{Config: cfg}, nil
}

// pinHTTP11ALPN forces ALPN negotiation to HTTP/1.1, dropping HTTP/2 while
// keeping acme-tls/1 for certificate issuance.
//
// The WSS client sends a browser ClientHello (to have no fingerprint of its
// own), and a browser offers both h2 and http/1.1. The websocket upgrade that
// has to follow is HTTP/1.1, so the server must never select h2 — an h2
// connection would leave the upgrade with nowhere to go. Because the resulting
// NextProtos does not list "h2", net/http will not auto-enable HTTP/2 either, so
// the listener offers exactly http/1.1 (and acme-tls/1 for a challenge).
func pinHTTP11ALPN(cfg *tls.Config) {
	protos := []string{"http/1.1"}
	if hasProto(cfg.NextProtos, acme.ALPNProto) {
		protos = append(protos, acme.ALPNProto)
	}
	cfg.NextProtos = protos
}

// --- file-backed certificates -----------------------------------------------

// certReloader holds a certificate and re-reads it when the file on disk
// changes, so an externally renewed certificate takes effect on the next
// handshake instead of at the next restart.
type certReloader struct {
	certFile string
	keyFile  string

	mu      sync.RWMutex
	cert    *tls.Certificate
	modTime time.Time
}

func fileTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	// Loaded once up front so a missing or malformed certificate is an error at
	// startup, where it is visible, rather than a handshake failure later.
	if _, err := r.load(); err != nil {
		return nil, err
	}
	return &tls.Config{
		GetCertificate: r.get,
		MinVersion:     tls.VersionTLS12,
	}, nil
}

func (r *certReloader) load() (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return nil, err
	}
	var mod time.Time
	if st, err := os.Stat(r.certFile); err == nil {
		mod = st.ModTime()
	}

	r.mu.Lock()
	r.cert, r.modTime = &cert, mod
	r.mu.Unlock()
	return &cert, nil
}

func (r *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.RLock()
	cert, known := r.cert, r.modTime
	r.mu.RUnlock()

	// A stat per handshake is cheap next to the handshake itself, and it is
	// what makes renewal seamless.
	if st, err := os.Stat(r.certFile); err == nil && st.ModTime().After(known) {
		if fresh, err := r.load(); err == nil {
			return fresh, nil
		}
		// A failed reload keeps serving the certificate already in memory: a
		// half-written file during renewal must not take the listener down.
	}
	if cert == nil {
		return nil, fmt.Errorf("no certificate loaded")
	}
	return cert, nil
}

// --- Let's Encrypt ----------------------------------------------------------

func acmeTLSConfig(s TLSSettings, logf func(string, ...any)) (*TLSLease, error) {
	if s.ACMECacheDir == "" {
		return nil, fmt.Errorf("acme cache directory is not set")
	}
	if err := os.MkdirAll(s.ACMECacheDir, 0700); err != nil {
		return nil, fmt.Errorf("acme cache directory: %w", err)
	}

	m, release, err := processACME.acquire(s, logf)
	if err != nil {
		return nil, err
	}

	cfg := m.TLSConfig()
	cfg.MinVersion = tls.VersionTLS12
	// TLSConfig() already advertises acme-tls/1, which is what lets Let's
	// Encrypt validate over the listener itself when it is on port 443 — no
	// second port, nothing else to open.
	if !hasProto(cfg.NextProtos, acme.ALPNProto) {
		cfg.NextProtos = append(cfg.NextProtos, acme.ALPNProto)
	}

	return &TLSLease{Config: cfg, onClose: release}, nil
}

func hasProto(list []string, want string) bool {
	for _, p := range list {
		if p == want {
			return true
		}
	}
	return false
}

type acmeEntry struct {
	manager  *autocert.Manager
	handler  http.Handler
	cacheDir string
	email    string
	refs     int
}

// acmeRegistry owns the single HTTP-01 listener a process is allowed to have.
// Its handler dispatches challenges to the manager registered for the request
// host, so independent tunnels and the web panel can safely share port 80.
type acmeRegistry struct {
	mu          sync.Mutex
	cond        *sync.Cond
	addr        string
	entries     map[string]*acmeEntry
	total       int
	server      *http.Server
	listener    net.Listener
	done        chan struct{}
	stopping    bool
	attempted   bool
	httpHandler http.Handler
	retryStop   chan struct{}
	retryDone   chan struct{}
	retryEvery  time.Duration
}

func newACMERegistry(addr string) *acmeRegistry {
	r := &acmeRegistry{addr: addr, entries: make(map[string]*acmeEntry), retryEvery: time.Second}
	r.cond = sync.NewCond(&r.mu)
	return r
}

var processACME = newACMERegistry(":80")

func (r *acmeRegistry) acquire(s TLSSettings, logf func(string, ...any)) (*autocert.Manager, func(), error) {
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s.ACMEDomain), "."))
	if domain == "" {
		return nil, nil, fmt.Errorf("acme domain is not set")
	}

	r.mu.Lock()
	for r.stopping {
		r.cond.Wait()
	}
	if existing := r.entries[domain]; existing != nil {
		if existing.cacheDir != s.ACMECacheDir || existing.email != s.ACMEEmail {
			r.mu.Unlock()
			return nil, nil, fmt.Errorf("acme domain %q is already registered with different account settings", domain)
		}
		existing.refs++
		r.total++
		manager := existing.manager
		r.mu.Unlock()
		return manager, r.releaseFunc(domain), nil
	}

	manager := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(s.ACMECacheDir),
		HostPolicy: autocert.HostWhitelist(domain),
		Email:      s.ACMEEmail,
	}
	// Calling HTTPHandler opts this manager into http-01 before certificate
	// issuance starts. Deferring this until the first challenge request would be
	// too late: autocert would already have selected tls-alpn-01 only.
	handler := manager.HTTPHandler(nil)
	r.entries[domain] = &acmeEntry{manager: manager, handler: handler, cacheDir: s.ACMECacheDir, email: s.ACMEEmail, refs: 1}
	r.total++
	if r.httpHandler == nil {
		shared := &autocert.Manager{
			Cache: autocert.DirCache(s.ACMECacheDir),
			HostPolicy: func(context.Context, string) error {
				// Challenge tokens are unguessable and public by design. Allowing
				// any Host here lets the process which owns port 80 serve a token
				// written to the shared cache by another Backpack process.
				return nil
			},
		}
		r.httpHandler = shared.HTTPHandler(nil)
	}
	if r.server == nil && !r.attempted {
		r.startLocked(logf)
	}
	r.mu.Unlock()
	return manager, r.releaseFunc(domain), nil
}

func (r *acmeRegistry) startLocked(logf func(string, ...any)) {
	r.attempted = true
	if err := r.listenLocked(logf); err == nil {
		return
	} else {
		logACME(logf, "ACME HTTP-01 responder could not use %s (%v); another Backpack process may own it, so this process will keep retrying", r.addr, err)
	}
	r.retryStop, r.retryDone = make(chan struct{}), make(chan struct{})
	stop, done := r.retryStop, r.retryDone
	go r.retryListen(stop, done, logf)
}

func (r *acmeRegistry) listenLocked(logf func(string, ...any)) error {
	listener, err := net.Listen("tcp", r.addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	r.server, r.listener, r.done = srv, listener, make(chan struct{})
	done := r.done
	go func() {
		defer close(done)
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			logACME(logf, "ACME HTTP-01 responder stopped: %v", err)
		}
	}()
	return nil
}

func (r *acmeRegistry) retryListen(stop <-chan struct{}, done chan<- struct{}, logf func(string, ...any)) {
	defer close(done)
	ticker := time.NewTicker(r.retryEvery)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			r.mu.Lock()
			if r.total == 0 || r.server != nil || r.retryStop != stop {
				r.mu.Unlock()
				return
			}
			if err := r.listenLocked(logf); err == nil {
				r.retryStop, r.retryDone = nil, nil
				r.mu.Unlock()
				logACME(logf, "ACME HTTP-01 responder acquired %s", r.addr)
				return
			}
			r.mu.Unlock()
		}
	}
}

func (r *acmeRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	host := req.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	r.mu.Lock()
	entry := r.entries[host]
	handler := r.httpHandler
	r.mu.Unlock()
	if entry != nil {
		entry.handler.ServeHTTP(w, req)
		return
	}
	if handler == nil || !strings.HasPrefix(req.URL.Path, "/.well-known/acme-challenge/") {
		http.NotFound(w, req)
		return
	}
	// DirCache stores http-01 tokens, so this fallback also serves challenges
	// created by another Backpack process using the same persistent cache.
	handler.ServeHTTP(w, req)
}

func (r *acmeRegistry) releaseFunc(domain string) func() {
	var once sync.Once
	return func() { once.Do(func() { r.release(domain) }) }
}

func (r *acmeRegistry) release(domain string) {
	r.mu.Lock()
	entry := r.entries[domain]
	if entry == nil {
		r.mu.Unlock()
		return
	}
	entry.refs--
	r.total--
	if entry.refs == 0 {
		delete(r.entries, domain)
	}
	if r.total > 0 {
		r.mu.Unlock()
		return
	}

	srv, done := r.server, r.done
	retryStop, retryDone := r.retryStop, r.retryDone
	r.server, r.listener, r.done = nil, nil, nil
	r.retryStop, r.retryDone = nil, nil
	r.attempted = false
	r.httpHandler = nil
	if srv != nil || retryStop != nil {
		r.stopping = true
	}
	r.mu.Unlock()

	if retryStop != nil {
		close(retryStop)
		<-retryDone
	}
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
		<-done
	}
	if srv != nil || retryStop != nil {
		r.mu.Lock()
		r.stopping = false
		r.cond.Broadcast()
		r.mu.Unlock()
	}
}

func logACME(logf func(string, ...any), format string, args ...any) {
	if logf != nil {
		logf(format, args...)
	}
}

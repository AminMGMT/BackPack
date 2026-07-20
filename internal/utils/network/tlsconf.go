package network

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
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

// ServerTLSConfig builds a *tls.Config for a listener.
//
// Both paths go through GetCertificate rather than a fixed certificate, so a
// renewed certificate is picked up without restarting the tunnel. That matters
// more than it sounds: Let's Encrypt certificates last 90 days, and a scheme
// that needed a restart would mean a scheduled interruption every couple of
// months on every tunnel using one.
func ServerTLSConfig(s TLSSettings, logf func(string, ...any)) (*tls.Config, error) {
	var cfg *tls.Config
	var err error
	if s.UsesACME() {
		cfg, err = acmeTLSConfig(s, logf)
	} else {
		cfg, err = fileTLSConfig(s.CertFile, s.KeyFile)
	}
	if err != nil {
		return nil, err
	}
	pinHTTP11ALPN(cfg)
	return cfg, nil
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

func acmeTLSConfig(s TLSSettings, logf func(string, ...any)) (*tls.Config, error) {
	if s.ACMECacheDir == "" {
		return nil, fmt.Errorf("acme cache directory is not set")
	}
	if err := os.MkdirAll(s.ACMECacheDir, 0700); err != nil {
		return nil, fmt.Errorf("acme cache directory: %w", err)
	}

	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(s.ACMECacheDir),
		HostPolicy: autocert.HostWhitelist(s.ACMEDomain),
		Email:      s.ACMEEmail,
	}

	cfg := m.TLSConfig()
	cfg.MinVersion = tls.VersionTLS12
	// TLSConfig() already advertises acme-tls/1, which is what lets Let's
	// Encrypt validate over the listener itself when it is on port 443 — no
	// second port, nothing else to open.
	if !hasProto(cfg.NextProtos, acme.ALPNProto) {
		cfg.NextProtos = append(cfg.NextProtos, acme.ALPNProto)
	}

	// A tunnel that is not on 443 cannot be validated that way, so an HTTP-01
	// responder is started on port 80 as well. It is best effort: if the port
	// is taken, TLS-ALPN may still work, and saying so is more useful than
	// refusing to start.
	go serveACMEHTTP(m, logf)

	return cfg, nil
}

func hasProto(list []string, want string) bool {
	for _, p := range list {
		if p == want {
			return true
		}
	}
	return false
}

// serveACMEHTTP runs the HTTP-01 responder on port 80.
func serveACMEHTTP(m *autocert.Manager, logf func(string, ...any)) {
	srv := &http.Server{
		Addr:         ":80",
		Handler:      m.HTTPHandler(nil),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		logf("ACME HTTP-01 responder could not use port 80 (%v); "+
			"validation will rely on TLS-ALPN, which needs the tunnel to be on port 443", err)
	}
}

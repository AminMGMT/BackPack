package network

import (
	"crypto/tls"
	"fmt"
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

// HTTPSConfig builds a *tls.Config for an ordinary HTTPS server — the web
// panel. It offers the same two ways to get a certificate as the tunnel
// listeners, and deliberately skips the ALPN pinning they need: that exists so
// a WebSocket upgrade can never be negotiated away to HTTP/2, and a panel has
// no upgrade to protect.
//
// Renewal is not something the caller has to arrange. Both paths hand back a
// config whose certificate is resolved per handshake, so an ACME certificate
// reissued at the 60-day mark of its 90-day life is served from the next
// connection onward without restarting the panel.
func HTTPSConfig(s TLSSettings, logf func(string, ...any)) (*tls.Config, error) {
	if s.UsesACME() {
		return acmeTLSConfig(s, logf)
	}
	return fileTLSConfig(s.CertFile, s.KeyFile)
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

	// A handshake that carries no SNI is answered with the one domain this
	// listener has.
	//
	// autocert identifies the certificate to serve by the name in the
	// ClientHello and refuses outright when there is none — "missing server
	// name". That is the right default for a host serving many domains and the
	// wrong one here, where exactly one was configured. It also bit in a way
	// nobody could see: a tunnel's remote address is usually the server's IP,
	// the client sends no SNI when it dials an address literal, and so every
	// connection was refused before autocert ever tried to obtain anything.
	issue := cfg.GetCertificate
	cfg.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if hello.ServerName == "" {
			clone := *hello
			clone.ServerName = s.ACMEDomain
			return issue(&clone)
		}
		return issue(hello)
	}
	// TLSConfig() already advertises acme-tls/1, which is what lets Let's
	// Encrypt validate over the listener itself when it is on port 443 — no
	// second port, nothing else to open.
	if !hasProto(cfg.NextProtos, acme.ALPNProto) {
		cfg.NextProtos = append(cfg.NextProtos, acme.ALPNProto)
	}

	// A tunnel that is not on 443 cannot be validated that way, so an HTTP-01
	// responder answers on port 80 as well. One responder serves the whole
	// process and is pointed at this manager; see acmehttp.go for why it is
	// not started afresh here.
	acmeResponder.use(m, logf)

	primeACMECert(m, s.ACMEDomain, logf)

	return cfg, nil
}

// acmePrimeTimeout bounds the startup attempt. autocert applies five minutes of
// its own to issuance; this is only the outer bound on the goroutine.
const acmePrimeTimeout = 6 * time.Minute

// primeACMECert obtains the certificate now instead of waiting for a client.
//
// autocert issues lazily: nothing is requested until a handshake asks for a
// name, and a failure then surfaces as a broken handshake on whoever happened
// to connect. From the server there was nothing at all — the operator set a
// domain, restarted, watched the tunnel not work, and had no way to learn why.
// Two reports of this arrived from two people who had each tried two servers,
// and both had concluded Let's Encrypt was down.
//
// Asking at startup changes when the work happens, not what it does: the same
// manager, the same cache, the same challenge. What it buys is that the
// answer — issued, or the exact reason not — lands in the tunnel's own log
// beside everything else about why it did or did not come up.
//
// The hello is built to look like the clients that will actually arrive.
// autocert picks between an RSA and an ECDSA certificate from the cipher
// suites and signature schemes offered (see supportsECDSA), and a bare
// ClientHelloInfo reads as an ancient client — so priming with one would fetch
// an RSA certificate that the first real handshake then misses in the cache,
// and issue everything a second time. Doing that on every start is how a host
// walks into Let's Encrypt's rate limits.
//
// It runs in the background because issuance talks to Let's Encrypt and waits
// on a challenge, which is far too long to hold up a listener, and it is best
// effort: a cached certificate is served whatever happens here, and a failure
// now is retried by the ordinary lazy path on the next handshake.
func primeACMECert(m *autocert.Manager, domain string, logf func(string, ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	go func() {
		done := make(chan error, 1)
		go func() {
			_, err := m.GetCertificate(&tls.ClientHelloInfo{
				ServerName: domain,
				CipherSuites: []uint16{
					tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
					tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				},
				SupportedCurves: []tls.CurveID{tls.CurveP256},
				SignatureSchemes: []tls.SignatureScheme{
					tls.ECDSAWithP256AndSHA256,
					tls.PSSWithSHA256,
				},
				SupportedVersions: []uint16{tls.VersionTLS13, tls.VersionTLS12},
			})
			done <- err
		}()

		select {
		case err := <-done:
			if err != nil {
				logf("certificate for %s could not be obtained: %v — TLS will not come up until this succeeds. "+
					"Check that %s resolves to this server, that port 80 is reachable from outside (or that this "+
					"tunnel listens on 443), and that this server can reach acme-v02.api.letsencrypt.org",
					domain, err, domain)
				return
			}
			logf("certificate for %s is ready", domain)
		case <-time.After(acmePrimeTimeout):
			logf("certificate for %s did not arrive within %s — still trying on the next connection",
				domain, acmePrimeTimeout)
		}
	}()
}

func hasProto(list []string, want string) bool {
	for _, p := range list {
		if p == want {
			return true
		}
	}
	return false
}

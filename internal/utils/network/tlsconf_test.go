package network

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestPair puts a throwaway self-signed certificate on disk.
func writeTestPair(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "backpack-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"nothing.invalid"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certFile = filepath.Join(dir, "t.crt")
	keyFile = filepath.Join(dir, "t.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// A panel behind Let's Encrypt still answers when Let's Encrypt cannot issue.
//
// autocert fails the handshake when it has no certificate for the name, so
// every reason issuance can fail — a domain that does not resolve yet, a
// blocked port 80, a contact address the CA rejects, no route to the CA at all
// — used to end with the panel refusing every browser, including the one the
// operator would have fixed it from.
func TestTheACMEPathFallsBackToTheSelfSignedCertificate(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeTestPair(t, dir)

	cfg, err := HTTPSConfig(TLSSettings{
		ACMEDomain:       "nothing.invalid",
		ACMECacheDir:     filepath.Join(dir, "acme"),
		FallbackCertFile: certFile,
		FallbackKeyFile:  keyFile,
	}, func(string, ...any) {})
	if err != nil {
		t.Fatalf("HTTPSConfig: %v", err)
	}

	// No CA is reachable from a test, so issuance fails; the handshake must
	// still be answerable.
	got, err := cfg.GetCertificate(&tls.ClientHelloInfo{
		ServerName:        "nothing.invalid",
		SupportedVersions: []uint16{tls.VersionTLS13},
		CipherSuites:      []uint16{tls.TLS_AES_128_GCM_SHA256},
	})
	if err != nil {
		t.Fatalf("with a fallback configured the handshake still failed: %v", err)
	}
	if got == nil || len(got.Certificate) == 0 {
		t.Fatal("no certificate was returned")
	}

	// And without one, the old behaviour stands: nothing to serve, so the
	// caller learns that rather than being handed something wrong.
	bare, err := HTTPSConfig(TLSSettings{
		ACMEDomain:   "nothing.invalid",
		ACMECacheDir: filepath.Join(dir, "acme2"),
	}, func(string, ...any) {})
	if err != nil {
		t.Fatalf("HTTPSConfig: %v", err)
	}
	if _, err := bare.GetCertificate(&tls.ClientHelloInfo{
		ServerName:        "nothing.invalid",
		SupportedVersions: []uint16{tls.VersionTLS13},
		CipherSuites:      []uint16{tls.TLS_AES_128_GCM_SHA256},
	}); err == nil {
		t.Error("with no fallback the handshake unexpectedly succeeded")
	}
}

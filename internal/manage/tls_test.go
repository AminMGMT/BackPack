package manage

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestCert generates a self-signed certificate with the given SANs and
// writes it to a temp file, returning the path — so certCoversSANs can be
// checked without touching the real cert directory or the network.
func writeTestCert(t *testing.T, ips []net.IP, dns []string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  ips,
		DNSNames:     dns,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "c.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The panel certificate is regenerated only when it does not already cover the
// address set — so this superset check is what stops a working certificate being
// reissued (and re-trusted by hand) on every restart, and what catches a stale
// one that no longer covers the address the panel is reached on.
func TestCertCoversSANs(t *testing.T) {
	path := writeTestCert(t,
		[]net.IP{net.IPv4(127, 0, 0, 1), net.ParseIP("203.0.113.5")},
		[]string{"localhost"})

	if !certCoversSANs(path, []net.IP{net.IPv4(127, 0, 0, 1)}, []string{"localhost"}) {
		t.Error("must report coverage of an IP and DNS name the certificate contains")
	}
	if !certCoversSANs(path, []net.IP{net.ParseIP("203.0.113.5")}, nil) {
		t.Error("must report coverage of the public IP it contains")
	}
	if certCoversSANs(path, []net.IP{net.ParseIP("10.0.0.9")}, nil) {
		t.Error("must not claim to cover an IP it lacks — the stale-cert case")
	}
	if certCoversSANs(path, nil, []string{"panel.example.com"}) {
		t.Error("must not claim to cover a DNS name it lacks")
	}
	if certCoversSANs(filepath.Join(t.TempDir(), "missing.crt"), nil, nil) {
		t.Error("a missing file covers nothing")
	}
}

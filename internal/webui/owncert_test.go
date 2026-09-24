package webui

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writePair writes a certificate for name, valid until notAfter, and its key.
func writePair(t *testing.T, dir, name string, notAfter time.Time) (certFile, keyFile string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    notAfter.Add(-48 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	certFile = filepath.Join(dir, name+"-fullchain.pem")
	keyFile = filepath.Join(dir, name+"-privkey.pem")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return certFile, keyFile
}

// #49: a certificate the operator obtained elsewhere — certbot's
// fullchain.pem and privkey.pem — is accepted and described.
func TestABroughtCertificateIsAccepted(t *testing.T) {
	dir := t.TempDir()
	c, k := writePair(t, dir, "panel.example.com", time.Now().Add(60*24*time.Hour))
	names, notAfter, err := CheckOwnCert(c, k)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "panel.example.com" || time.Until(notAfter) < 59*24*time.Hour {
		t.Fatalf("names %v, until %s", names, notAfter)
	}
}

// What would stop the panel coming back is refused at the form instead.
func TestABroughtCertificateThePanelCouldNotServeIsRefused(t *testing.T) {
	dir := t.TempDir()
	c, k := writePair(t, dir, "a.example.com", time.Now().Add(24*time.Hour))
	_, otherKey := writePair(t, dir, "b.example.com", time.Now().Add(24*time.Hour))
	oldC, oldK := writePair(t, dir, "old.example.com", time.Now().Add(-time.Hour))
	for _, tc := range []struct{ name, cert, key, want string }{
		{"a key that is not the certificate's", c, otherKey, "cannot be used together"},
		{"an expired certificate", oldC, oldK, "expired"},
		{"a relative path", "fullchain.pem", "privkey.pem", "full paths"},
		{"a missing file", filepath.Join(dir, "nope.pem"), k, "cannot be used together"},
		{"only one file", c, "", "both files"},
	} {
		if _, _, err := CheckOwnCert(tc.cert, tc.key); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

// Every other mode forgets a brought certificate, so switching back to
// self-signed really does switch back.
func TestOnlyOwnModeKeepsTheBroughtFiles(t *testing.T) {
	c := Config{HTTPS: true, TLSCertFile: "/a", TLSKeyFile: "/b"}
	if !c.OwnCert() {
		t.Fatal("OwnCert false with both files set")
	}
	c.TLSKeyFile = ""
	if c.OwnCert() {
		t.Fatal("OwnCert true with one file")
	}
}

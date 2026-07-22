package manage

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// certDir is where auto-generated self-signed certificates live.
const certDir = app.ConfigDir + "/certs"

// EnsureSelfSignedCert generates (or reuses) a self-signed certificate/key pair
// for a tunnel, used by the wss/wssmux transports. host may be a domain or IP
// to embed as a SAN; it is optional because tunnel clients skip verification
// (InsecureSkipVerify) — encryption works regardless of the name on the cert.
// It returns the on-disk cert and key paths.
func EnsureSelfSignedCert(name, host string) (certPath, keyPath string, err error) {
	certPath = certDir + "/" + name + ".crt"
	keyPath = certDir + "/" + name + ".key"
	if fileExists(certPath) && fileExists(keyPath) {
		return certPath, keyPath, nil
	}
	if err = os.MkdirAll(certDir, 0755); err != nil {
		return "", "", err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}

	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "backpack", Organization: []string{"backpack"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if host != "" {
		if ip := net.ParseIP(host); ip != nil {
			tmpl.IPAddresses = []net.IP{ip}
		} else {
			tmpl.DNSNames = []string{host}
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err = os.WriteFile(certPath, certPEM, 0644); err != nil {
		return "", "", err
	}
	if err = os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

// validCertPair checks that both TLS files exist and are readable.
func validCertPair(cert, key string) error {
	for _, f := range []string{cert, key} {
		if f == "" {
			return fmt.Errorf("both tls_cert and tls_key paths are required")
		}
		if !fileExists(f) {
			return fmt.Errorf("file not found: %s", f)
		}
	}
	return nil
}

// CertExpiry reads a PEM certificate and returns its NotAfter time. It is the
// one place certificate files are parsed, shared by the health screen and the
// web panel so the two can never disagree about the same file.
func CertExpiry(path string) (time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return time.Time{}, fmt.Errorf("%s: not a valid PEM file", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: unparsable certificate", path)
	}
	return cert.NotAfter, nil
}

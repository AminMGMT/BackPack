package e2e

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
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The TLS transports had no end-to-end coverage at all, which is how the two
// most security-relevant transports came to be the two least exercised. These
// tests run whole tunnels over wss and wssmux, so a change to certificate
// handling has something to fail against.

// testCert generates a self-signed certificate for 127.0.0.1 and returns the
// PEM pair, mirroring what manage.EnsureSelfSignedCert produces on a real
// server. Generated at run time rather than checked in, so nothing here is a
// private key sitting in the repository.
//
// It lives for the whole package run rather than one test. A tunnel is still
// winding down when its test returns — a Restart() already in flight will
// re-read the certificate — so a per-test directory can be deleted out from
// under a server that is still running, which fails the engine fatally and
// takes the test binary with it.
var testCertOnce struct {
	sync.Once
	cert, key string
	err       error
}

func testCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	testCertOnce.Do(func() {
		testCertOnce.cert, testCertOnce.key, testCertOnce.err = generateTestCert()
	})
	if testCertOnce.err != nil {
		t.Fatalf("generating the test certificate: %v", testCertOnce.err)
	}
	return testCertOnce.cert, testCertOnce.key
}

func generateTestCert() (certPath, keyPath string, err error) {
	dir, err := os.MkdirTemp("", "backpack-e2e-tls-")
	if err != nil {
		return "", "", err
	}
	certPath = filepath.Join(dir, "test.crt")
	keyPath = filepath.Join(dir, "test.key")

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
		Subject:               pkix.Name{CommonName: "backpack-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		DNSNames:              []string{"localhost"},
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	certOut, err := os.Create(certPath)
	if err != nil {
		return "", "", err
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		certOut.Close()
		return "", "", err
	}
	certOut.Close()

	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return "", "", err
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		keyOut.Close()
		return "", "", err
	}
	keyOut.Close()

	return certPath, keyPath, nil
}

// TestTLSTransports runs a full tunnel over each TLS transport: control
// channel, pool and forwarded traffic, all inside TLS.
func TestTLSTransports(t *testing.T) {
	certPath, keyPath := testCert(t)

	for _, transport := range []string{"wss", "wssmux"} {
		t.Run(transport, func(t *testing.T) {
			backend := startEchoBackend(t)

			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "tls-token-0123456789abcdefghijk"

			srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)
			srvCfg.TLSCertFile = certPath
			srvCfg.TLSKeyFile = keyPath

			cliCfg := baseClientConfig(transport,
				fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

			tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("%s tunnel never carried traffic: %v", transport, err)
			}
			if err := tun.roundTrip(randomPayload(t, 128*1024)); err != nil {
				t.Fatalf("%s failed to move data: %v", transport, err)
			}
		})
	}
}

// TestTLSTransportRejectsWrongToken checks that TLS does not accidentally
// bypass authentication — the token still has to be right.
func TestTLSTransportRejectsWrongToken(t *testing.T) {
	certPath, keyPath := testCert(t)
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)

	srvCfg := baseServerConfig("wss", tunnelPort, entryPort, backend.addr, "the-real-token-0123456789abcdef")
	srvCfg.TLSCertFile = certPath
	srvCfg.TLSKeyFile = keyPath

	cliCfg := baseClientConfig("wss",
		fmt.Sprintf("127.0.0.1:%d", tunnelPort), "a-completely-different-token-000", nil)

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	if err := tun.waitReady(12 * time.Second); err == nil {
		t.Fatal("a client with the wrong token was allowed to carry traffic")
	}
}

// A missing certificate is deliberately not covered here. The engine's
// behaviour is correct — it refuses to start with
//
//	FATAL failed to listen on 127.0.0.1:NNN: open <path>.crt: no such file
//
// which is what you want: a loud failure at startup rather than a listener that
// accepts connections and then breaks them. But that path calls logger.Fatalf,
// which exits the process, and these tests run the engine in-process. Covering
// it would mean re-execing the test binary as a subprocess, which is a lot of
// machinery for a case whose whole behaviour is "it exits".

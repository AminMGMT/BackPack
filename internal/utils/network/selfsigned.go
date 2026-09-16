package network

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"time"
)

// A certificate for a tunnel that was not given one.
//
// The direct engine has always generated one in this case: wss with no
// acme_domain and no tls_cert serves a throwaway certificate, says so in the
// log, and works. The reverse engine went through fileTLSConfig with two empty
// filenames and failed at startup with `open : no such file or directory` — a
// message with no subject, naming nothing, on a tunnel that had simply not been
// told where its certificate was. Two engines, the same configuration, one
// working and one refusing with a sentence that does not say what is wrong.
//
// It is generated rather than refused because nothing here authenticates a
// peer with it. The tunnel's credential is its token; TLS is on the wire to
// look like TLS to whatever is in between, and a certificate nobody validates
// serves that exactly as well whoever signed it. An operator who wants a
// trusted one sets acme_domain, and the two lines below say which they got.

// GenerateSelfSigned makes a throwaway certificate for one run, in memory.
//
// host is cosmetic — nothing verifies it by default — but a certificate that
// names the host it is served from is what an inspecting eye expects.
func GenerateSelfSigned(host string) (tls.Certificate, error) {
	if host == "" {
		host = "localhost"
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		// Backdated an hour so a peer whose clock runs slow does not reject a
		// certificate that was valid when it was made.
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        &template,
	}, nil
}

// selfSignedTLSConfig wraps a generated certificate for a listener.
func selfSignedTLSConfig(host string) (*tls.Config, error) {
	cert, err := GenerateSelfSigned(host)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

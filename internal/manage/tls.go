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
	"strings"
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
	var ips []net.IP
	var dns []string
	if host != "" {
		if ip := net.ParseIP(host); ip != nil {
			ips = []net.IP{ip}
		} else {
			dns = []string{host}
		}
	}
	return writeSelfSigned(name, ips, dns)
}

// writeSelfSigned generates a fresh ECDSA self-signed certificate carrying the
// given IP and DNS SANs and writes it to name.crt / name.key, overwriting any
// existing pair. It is the one place the certificate template lives, shared by
// the tunnel certificates and the web panel's.
func writeSelfSigned(name string, ips []net.IP, dns []string) (certPath, keyPath string, err error) {
	certPath = certDir + "/" + name + ".crt"
	keyPath = certDir + "/" + name + ".key"
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
		IPAddresses:           ips,
		DNSNames:              dns,
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

// localSANs is the SAN set the web panel's certificate should carry so the
// browser validates it whichever address the panel is reached on: loopback and
// "localhost" (an SSH-forwarded or on-box browse), every one of the machine's
// own interface addresses (the address a VPS is actually reached on, read from
// the kernel rather than an external lookup a filtered network blocks), the
// public IPv4 when a lookup does succeed, and an optional operator host. This is
// the fix for a panel that guessed a single address — often "-" on a filtered
// network — and served a certificate no browser would accept.
func localSANs(host string) (ips []net.IP, dns []string) {
	seenIP := map[string]bool{}
	addIP := func(ip net.IP) {
		if ip == nil {
			return
		}
		if s := ip.String(); !seenIP[s] {
			seenIP[s] = true
			ips = append(ips, ip)
		}
	}
	seenDNS := map[string]bool{"": true}
	addDNS := func(n string) {
		if n = strings.TrimSpace(n); !seenDNS[n] {
			seenDNS[n] = true
			dns = append(dns, n)
		}
	}

	addIP(net.IPv4(127, 0, 0, 1))
	addIP(net.IPv6loopback)
	addDNS("localhost")
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLinkLocalUnicast() {
				addIP(ipnet.IP)
			}
		}
	}
	if p := PublicIPv4(); p != "" && p != "-" {
		addIP(net.ParseIP(p))
	}
	if host = strings.TrimSpace(host); host != "" && host != "-" {
		if ip := net.ParseIP(host); ip != nil {
			addIP(ip)
		} else {
			addDNS(host)
		}
	}
	return ips, dns
}

// EnsurePanelCert makes sure the web panel's self-signed certificate exists and
// covers the current address set (plus an optional operator host). It reuses the
// existing pair when it already covers everything, and regenerates it when it
// does not — so an address that changed, or a first certificate that captured
// only a useless "-", is corrected rather than served to every browser forever.
func EnsurePanelCert(host string) (certPath, keyPath string, err error) {
	certPath = certDir + "/webui.crt"
	keyPath = certDir + "/webui.key"
	ips, dns := localSANs(host)
	if fileExists(certPath) && fileExists(keyPath) && certCoversSANs(certPath, ips, dns) {
		return certPath, keyPath, nil
	}
	return writeSelfSigned("webui", ips, dns)
}

// certCoversSANs reports whether the certificate at path already lists every one
// of the wanted IP and DNS SANs, so a certificate that is already reachable is
// not regenerated — and re-trusted by hand — on every restart for no reason.
func certCoversSANs(path string, ips []net.IP, dns []string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	has := map[string]bool{}
	for _, ip := range cert.IPAddresses {
		has[ip.String()] = true
	}
	for _, n := range cert.DNSNames {
		has[n] = true
	}
	for _, ip := range ips {
		if !has[ip.String()] {
			return false
		}
	}
	for _, n := range dns {
		if !has[n] {
			return false
		}
	}
	return true
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

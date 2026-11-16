package manage

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/backpack/backpack/config"
)

// fakeHTTPSHelpers stands in for the installed helpers and the certificate
// writer, so the panel's HTTPS path runs without an install or /etc/backpack.
// The REALITY probe passes every cover unless the test replaces it.
func fakeHTTPSHelpers(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"sing-box", "xray", "naive"} {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prevPath, prevReality, prevCert, prevProbe := panelHelperPath, panelRealityBinary, panelCert, panelRealityProbe
	t.Cleanup(func() {
		panelHelperPath, panelRealityBinary, panelCert, panelRealityProbe = prevPath, prevReality, prevCert, prevProbe
	})
	panelHelperPath = func(tool string) string { return filepath.Join(dir, tool) }
	panelRealityBinary = func() string { return filepath.Join(dir, "xray") }
	panelCert = func(name, host string) (string, string, error) { return testCert(dir, name, host) }
	panelRealityProbe = func(context.Context, string, string, string) error { return nil }
	return dir
}

func testCert(dir, name, host string) (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	tpl := x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true,
	}
	if ip := net.ParseIP(host); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else {
		tpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	cert, keyPath := filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return "", "", err
	}
	return cert, keyPath, nil
}

func freeHTTPSPort(t *testing.T) string {
	t.Helper()
	p := ctPickPort(map[int]bool{}, false)
	if p == 0 {
		t.Skip("no free port")
	}
	return strconv.Itoa(p)
}

// The panel builds the Iran end of each HTTPS transport from the form it
// already has, and the setup link it shows carries what the kharej end needs:
// the same credentials, read back through the link the kharej applies.
func TestThePanelBuildsTheIranEndOfEachHTTPSTransport(t *testing.T) {
	fakeHTTPSHelpers(t)
	const host = "203.0.113.7"
	for _, tr := range []string{"naive", "xhttp", "reality"} {
		t.Run(tr, func(t *testing.T) {
			public := freeHTTPSPort(t)
			n := NewTunnel{Role: "server", Transport: tr, Name: "https-" + tr, TunnelPort: public,
				Token: "a-token-long-enough-to-be-accepted-0123456789", Ports: freeHTTPSPort(t), Host: host}
			if tr == "xhttp" {
				n.TLSName = "cdn.example.com" // the host is an IP, which TLS cannot name
			}
			s, err := specFromNew(n)
			if err != nil {
				t.Fatalf("the panel refused a %s tunnel: %v", tr, err)
			}
			if s.Transport != "tcp" || !strings.HasPrefix(s.BindAddr, "127.0.0.1:") {
				t.Errorf("the engine half is %s on %s; want tcp on loopback behind the helper", s.Transport, s.BindAddr)
			}
			if got := selectedTransport(s); got != tr {
				t.Errorf("the tunnel reads back as %q", got)
			}
			if managedEndpoint(s) != "0.0.0.0:"+public {
				t.Errorf("the helper listens on %q, want the form's port", managedEndpoint(s))
			}

			var cfg config.Config
			if _, err := toml.Decode(s.Render(), &cfg); err != nil {
				t.Fatal(err)
			}
			raw, err := shareLinkOf(s.Name, host, cfg)
			if err != nil {
				t.Fatalf("no setup link for the kharej: %v", err)
			}
			link, err := DecodeShareLink(raw)
			if err != nil {
				t.Fatal(err)
			}
			if link.Tr != tr || link.Port != public {
				t.Errorf("the link names %s on %s", link.Tr, link.Port)
			}
			kharej, err := kharejFromLink(link, LinkApplyOptions{})
			if err != nil {
				t.Fatalf("the kharej cannot apply the link: %v", err)
			}
			switch tr {
			case "naive":
				if kharej.NaiveClient.Password != s.NaiveServer.Password || kharej.NaiveClient.Username != s.NaiveServer.Username {
					t.Error("the kharej got other Naive credentials than Iran's")
				}
				if link.HelperCA == "" {
					t.Error("the private certificate did not travel in the link")
				}
			case "xhttp":
				if kharej.XrayClient.UUID != s.XrayServer.UUID || kharej.XrayClient.Path != s.XrayServer.Path {
					t.Error("the kharej got another UUID or path than Iran's")
				}
			case "reality":
				if s.XrayServer.Target != realityCoverSuggestions[0] || kharej.XrayClient.PublicKey == "" ||
					kharej.XrayClient.ShortID != s.XrayServer.ShortID {
					t.Errorf("REALITY: cover %q, kharej %+v", s.XrayServer.Target, kharej.XrayClient)
				}
			}
		})
	}
}

// What the panel cannot build it refuses by name, before anything is written.
func TestThePanelRefusesWhatItCannotBuildForHTTPS(t *testing.T) {
	dir := fakeHTTPSHelpers(t)
	base := NewTunnel{Role: "server", Name: "https-x", TunnelPort: freeHTTPSPort(t),
		Token: "a-token-long-enough-to-be-accepted-0123456789", Ports: freeHTTPSPort(t), Host: "203.0.113.7"}

	kharej := base
	kharej.Role, kharej.Transport, kharej.Ports, kharej.ServerAddr = "client", "naive", "", "203.0.113.7"
	if _, err := specFromNew(kharej); err == nil || !strings.Contains(err.Error(), "setup link") {
		t.Errorf("a kharej HTTPS end was not sent to the setup link: %v", err)
	}

	if err := os.Remove(filepath.Join(dir, "sing-box")); err != nil {
		t.Fatal(err)
	}
	naive := base
	naive.Transport = "naive"
	if _, err := specFromNew(naive); err == nil || !strings.Contains(err.Error(), "BP_HELPERS") {
		t.Errorf("a missing helper was not named: %v", err)
	}

	xhttp := base
	xhttp.Transport = "xhttp"
	if _, err := specFromNew(xhttp); err == nil || !strings.Contains(err.Error(), "TLS name") {
		t.Errorf("XHTTP on an IP without a TLS name was accepted: %v", err)
	}

	panelRealityProbe = func(context.Context, string, string, string) error { return errors.New("no TLS 1.3") }
	reality := base
	reality.Transport = "reality"
	if _, err := specFromNew(reality); err == nil || !strings.Contains(err.Error(), "cover") {
		t.Errorf("no passing cover was not reported: %v", err)
	}
	reality.RealityCover = "203.0.113.9:443"
	if _, err := specFromNew(reality); err == nil || !strings.Contains(err.Error(), "hostname") {
		t.Errorf("an IP cover was accepted: %v", err)
	}
}

// The menus the panel draws: HTTPS as a family of its own, marked as built on
// the Iran side. The spoofing carriers stay CLI-only.
func TestThePanelOffersEveryTransport(t *testing.T) {
	var https *TransportFamily
	seen := map[string]bool{}
	for i, f := range TransportFamilies() {
		for _, e := range f.Entries {
			seen[e.Value] = true
		}
		if f.Label == "HTTPS" {
			https = &TransportFamilies()[i]
		}
	}
	if https == nil || !https.IranOnly || len(https.Entries) != 3 {
		t.Fatalf("the HTTPS family is missing or not marked Iran-only: %+v", https)
	}
	for _, tr := range []string{"tcp", "tcpmux", "stealth", "pck", "udp", "kcp", "quic", "ws", "wsmux", "wss", "wssmux", "naive", "xhttp", "reality"} {
		if !seen[tr] {
			t.Errorf("%s is not offered", tr)
		}
	}
	carriers := map[string]bool{}
	for _, c := range PanelDirectCarriers() {
		carriers[c["value"]] = true
	}
	for _, c := range []string{"xdi", "pck", "gre"} {
		if !carriers[c] {
			t.Errorf("the direct carrier %s is not offered", c)
		}
	}
	for _, c := range []string{"spoof", "sni"} {
		if carriers[c] {
			t.Errorf("the direct carrier %s is offered; it is CLI-only", c)
		}
	}
}

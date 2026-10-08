package tunnelspec

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"fmt"
	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"strings"
	"testing"
)

// The clamp only reaches the config file when it is set, so a tunnel that never
// had one does not grow an `mss = 0` line.
func TestClampIsWrittenOnlyWhenSet(t *testing.T) {
	render := func(s Spec) string {
		var b strings.Builder
		s.writeTuning(func(f string, a ...any) { b.WriteString(fmt.Sprintf(f, a...)) })
		return b.String()
	}
	if got := render(Spec{MSS: 1208}); !strings.Contains(got, "mss = 1208") {
		t.Fatalf("a set clamp was not written to the config: %q", got)
	}
	if got := render(Spec{}); strings.Contains(got, "mss") {
		t.Fatalf("an unset clamp was written anyway: %q", got)
	}
}

// Ordinary edits must keep the helper that carries the reverse connection,
// including credentials and certificate paths that generic JSON cannot expose.
func TestNaiveLoadEditRoundTrip(t *testing.T) {
	binary, certificate, key := naiveSpecFiles(t)
	for _, role := range []string{"server", "client"} {
		t.Run(role, func(t *testing.T) {
			s := Spec{Role: role, Transport: "tcp", BindAddr: "127.0.0.1:62001", RemoteAddr: "127.0.0.1:62001", Token: "reverse-token", Ports: []string{"62003=127.0.0.1:62004"}, KeepAlive: 15, Nodelay: true}
			if role == "server" {
				s.NaiveServer = config.NaiveServerConfig{Binary: binary, Listen: "0.0.0.0:62002", Username: "helper-user", Password: "secret-\"#-password", Certificate: certificate, Key: key}
			} else {
				s.NaiveClient = config.NaiveClientConfig{Binary: binary, Server: "example.org:62002", Username: "helper-user", Password: "secret-\"#-password", CAFile: certificate}
			}
			if err := s.validateNaive(); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
				t.Fatal(err)
			}
			file, err := os.CreateTemp(app.ConfigDir, "naive-edit-*.toml")
			if err != nil {
				t.Fatal(err)
			}
			path := file.Name()
			t.Cleanup(func() { os.Remove(path) })
			if _, err := file.WriteString(s.Render()); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			name := strings.TrimSuffix(filepath.Base(path), ".toml")
			loaded, err := Load(name)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Role != role || loaded.Transport != "tcp" || loaded.Token != s.Token || loaded.KeepAlive != 15 {
				t.Fatalf("loaded wrong reverse tunnel: %+v", loaded)
			}
			if !reflect.DeepEqual(loaded.NaiveServer, s.NaiveServer) || !reflect.DeepEqual(loaded.NaiveClient, s.NaiveClient) {
				t.Fatal("loading dropped the helper configuration")
			}
			loaded.KeepAlive = 45
			loaded.Nodelay = false
			if err := loaded.validateNaive(); err != nil {
				t.Fatalf("unrelated tuning edit invalidated helper: %v", err)
			}
			var cfg config.Config
			if _, err := toml.Decode(loaded.Render(), &cfg); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.Server.Naive, s.NaiveServer) || !reflect.DeepEqual(cfg.Client.Naive, s.NaiveClient) {
				t.Fatal("re-saving dropped or changed helper credentials or paths")
			}
			if role == "server" && (cfg.Server.Keepalive != 45 || cfg.Server.Nodelay || !reflect.DeepEqual(cfg.Server.Ports, s.Ports)) {
				t.Fatal("server edit changed forwarding or failed to save tuning")
			}
			if role == "client" && (cfg.Client.Keepalive != 45 || cfg.Client.Nodelay || cfg.Client.RemoteAddr != s.RemoteAddr) {
				t.Fatal("client edit changed reverse target or failed to save tuning")
			}
			encoded, err := json.Marshal(loaded)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "secret-") || strings.Contains(string(encoded), "helper-user") {
				t.Fatal("generic JSON exposed helper credentials")
			}
		})
	}
}

// Refuse edits that would bypass or misdirect the helper before Save writes a
// tunnel config or invokes systemd. Both role-specific validators are exercised.
func TestNaiveIncompatibleEditPreservesExistingConfig(t *testing.T) {
	binary, certificate, key := naiveSpecFiles(t)
	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(app.ConfigDir, "naive-invalid-edit-*.toml")
	if err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	t.Cleanup(func() { os.Remove(path) })
	original := []byte("# existing tunnel configuration\n")
	if _, err := file.Write(original); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	name := strings.TrimSuffix(filepath.Base(path), ".toml")
	base := Spec{Role: "client", Name: name, Transport: "tcp", RemoteAddr: "127.0.0.1:62001", NaiveClient: config.NaiveClientConfig{Binary: binary, Server: "example.org:62002", Username: "u", Password: "p"}}
	server := Spec{Role: "server", Name: base.Name, Transport: "tcp", BindAddr: "127.0.0.1:62001", NaiveServer: config.NaiveServerConfig{Binary: binary, Listen: "0.0.0.0:62002", Username: "u", Password: "p", Certificate: certificate, Key: key}}
	cases := []struct {
		name string
		spec Spec
		edit func(*Spec)
	}{
		{"client-carrier", base, func(s *Spec) { s.Transport = "udp" }},
		{"client-target", base, func(s *Spec) { s.RemoteAddr = "192.0.2.1:62001" }},
		{"client-proxy", base, func(s *Spec) { s.Proxy = "socks5://127.0.0.1:1080" }},
		{"client-fallback", base, func(s *Spec) { s.FallbackTransports = []string{"tcpmux"} }},
		{"client-credentials", base, func(s *Spec) { s.NaiveClient.Password = "" }},
		{"server-carrier", server, func(s *Spec) { s.Transport = "tcpmux" }},
		{"server-target", server, func(s *Spec) { s.BindAddr = "0.0.0.0:62001" }},
		{"server-public-port", server, func(s *Spec) { s.NaiveServer.Listen = "0.0.0.0:62001" }},
		{"server-udp", server, func(s *Spec) { s.AcceptUDP = true }},
		{"wrong-helper-role", base, func(s *Spec) { s.NaiveServer = server.NaiveServer }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.spec
			if err := s.validateNaive(); err != nil {
				t.Fatalf("valid base: %v", err)
			}
			tc.edit(&s)
			if _, err := s.Save(); err == nil || !strings.Contains(err.Error(), "Naive configuration") {
				t.Fatalf("incompatible edit reached config/service mutation: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(original) {
				t.Fatalf("invalid edit changed the existing configuration: %q, %v", got, err)
			}
		})
	}
	if err := (Spec{Role: "client", Transport: "tcp"}).validateNaive(); err != nil {
		t.Fatalf("ordinary reverse tunnel needs no helper: %v", err)
	}
}

func naiveSpecFiles(t *testing.T) (binary, certificate, key string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example.org"}, DNSNames: []string{"example.org"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certificate, key = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return binary, certificate, key
}

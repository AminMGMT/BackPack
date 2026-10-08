package tunnelspec

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"fmt"
	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/tunnel/naive"
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
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Save(); err == nil || !strings.Contains(err.Error(), "Naive configuration") {
				t.Fatalf("incompatible edit reached config/service mutation: %v", err)
			}
			if err := Apply(s); err == nil || !strings.Contains(err.Error(), "Naive configuration") {
				t.Fatalf("incompatible apply reached config/service mutation: %v", err)
			}
			after, err := os.Stat(path)
			if err != nil || !os.SameFile(before, after) || before.ModTime() != after.ModTime() {
				t.Fatal("invalid helper edit replaced the current file")
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

func TestXrayLoadEditPreservesSettingsAndRejectsBypass(t *testing.T) {
	binary, certificate, key := naiveSpecFiles(t)
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"xhttp", "reality"} {
		for _, role := range []string{"server", "client"} {
			t.Run(mode+"/"+role, func(t *testing.T) {
				s := Spec{Role: role, Transport: "tcp", BindAddr: "127.0.0.1:62001", RemoteAddr: "127.0.0.1:62001", Token: "reverse-token", Ports: []string{"62003=127.0.0.1:62004"}, KeepAlive: 15}
				if role == "server" {
					s.XrayServer = config.XrayServerConfig{Binary: binary, Listen: "0.0.0.0:62002", Mode: mode, UUID: "12345678-1234-4234-8234-123456789abc", ServerName: "example.org"}
					if mode == "xhttp" {
						s.XrayServer.Path, s.XrayServer.Certificate, s.XrayServer.Key = "/private-route", certificate, key
					} else {
						s.XrayServer.PrivateKey = base64.RawURLEncoding.EncodeToString(private.Bytes())
						s.XrayServer.Target, s.XrayServer.ShortID = "example.org:443", "abcdef0123456789"
					}
				} else {
					s.XrayClient = config.XrayClientConfig{Binary: binary, Server: "example.org:62002", Mode: mode, UUID: "12345678-1234-4234-8234-123456789abc", ServerName: "example.org"}
					if mode == "xhttp" {
						s.XrayClient.Path, s.XrayClient.CAFile = "/private-route", certificate
					} else {
						s.XrayClient.PublicKey = base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes())
						s.XrayClient.ShortID = "abcdef0123456789"
					}
				}
				if err := s.validateXray(); err != nil {
					t.Fatal(err)
				}
				file, err := os.CreateTemp(app.ConfigDir, "xray-edit-*.toml")
				if err != nil {
					t.Fatal(err)
				}
				path := file.Name()
				t.Cleanup(func() { os.Remove(path) })
				original := s.Render()
				if _, err := file.WriteString(original); err != nil {
					file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				s.Name = strings.TrimSuffix(filepath.Base(path), ".toml")
				loaded, err := Load(s.Name)
				if err != nil {
					t.Fatal(err)
				}
				if loaded.XrayServer != s.XrayServer || loaded.XrayClient != s.XrayClient {
					t.Fatal("load dropped Xray settings")
				}
				loaded.KeepAlive = 45
				if err := loaded.validateXray(); err != nil {
					t.Fatal(err)
				}
				var cfg config.Config
				if _, err := toml.Decode(loaded.Render(), &cfg); err != nil {
					t.Fatal(err)
				}
				if cfg.Server.Xray != s.XrayServer || cfg.Client.Xray != s.XrayClient {
					t.Fatal("ordinary edit changed Xray settings")
				}
				if role == "server" && mode == "xhttp" {
					body, err := naive.XrayServerJSON(&cfg.Server)
					if err != nil {
						t.Fatal(err)
					}
					var generated map[string]any
					if err := json.Unmarshal(body, &generated); err != nil {
						t.Fatal(err)
					}
					stream := generated["inbounds"].([]any)[0].(map[string]any)["streamSettings"].(map[string]any)
					cert := stream["tlsSettings"].(map[string]any)["certificates"].([]any)[0].(map[string]any)
					if cert["oneTimeLoading"] != true {
						t.Fatal("Xray must not reload a certificate rejected by BackPack")
					}
				}
				encoded, err := json.Marshal(loaded)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{s.XrayServer.UUID, s.XrayClient.UUID, s.XrayServer.PrivateKey} {
					if secret != "" && strings.Contains(string(encoded), secret) {
						t.Fatal("generic JSON exposed helper credentials")
					}
				}
				for _, edit := range []func(*Spec){
					func(s *Spec) { s.Transport = "udp" },
					func(s *Spec) { s.Role = "direct" },
					func(s *Spec) { s.FallbackTransports = []string{"tcpmux"} },
					func(s *Spec) { s.BindAddr, s.RemoteAddr = "192.0.2.1:62001", "192.0.2.1:62001" },
				} {
					invalid := s
					edit(&invalid)
					before, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := invalid.Save(); err == nil || !strings.Contains(err.Error(), "Xray configuration") {
						t.Fatalf("invalid edit reached mutation: %v", err)
					}
					if err := Apply(invalid); err == nil || !strings.Contains(err.Error(), "Xray configuration") {
						t.Fatalf("invalid apply reached mutation: %v", err)
					}
					after, err := os.Stat(path)
					if err != nil || !os.SameFile(before, after) || before.ModTime() != after.ModTime() {
						t.Fatal("invalid helper edit replaced the current file")
					}
					got, err := os.ReadFile(path)
					if err != nil || string(got) != original {
						t.Fatal("invalid edit changed existing config")
					}
				}
			})
		}
	}
}

func TestManagedCarrierPreservesReplyAfterDirectionalEOF(t *testing.T) {
	for _, size := range []int{0, 65536, 65537, 262144} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			left, right := net.Pipe()
			client, server := naive.WrapDuplex(left), naive.WrapDuplex(right)
			defer client.Close()
			defer server.Close()
			client.SetDeadline(time.Now().Add(3 * time.Second))
			server.SetDeadline(time.Now().Add(3 * time.Second))
			payload := bytes.Repeat([]byte{0xA7}, size)
			reply := []byte("complete backend reply after request EOF")
			done := make(chan error, 1)
			go func() {
				got, err := io.ReadAll(server)
				if err == nil && !bytes.Equal(got, payload) {
					err = fmt.Errorf("request data changed")
				}
				if err == nil {
					_, err = server.Write(reply)
				}
				if err == nil {
					err = server.CloseWrite()
				}
				done <- err
			}()
			if n, err := client.Write(payload); err != nil || n != len(payload) {
				t.Fatalf("write: %d %v", n, err)
			}
			if err := client.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			if err := client.CloseWrite(); err != nil {
				t.Fatalf("repeated EOF: %v", err)
			}
			if _, err := client.Write([]byte("late")); err == nil {
				t.Fatal("write after EOF succeeded")
			}
			got, err := io.ReadAll(client)
			if err != nil || !bytes.Equal(got, reply) {
				t.Fatalf("response after EOF: %q %v", got, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagedCarrierCloseInterruptsBothDirections(t *testing.T) {
	left, right := net.Pipe()
	c := naive.WrapDuplex(left)
	defer right.Close()
	defer c.Close()
	done := make(chan error, 2)
	go func() { _, err := c.Read(make([]byte, 1)); done <- err }()
	go func() { _, err := c.Write([]byte("blocked")); done <- err }()
	c.Close()
	for range 2 {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("closed carrier completed blocked IO successfully")
			}
		case <-time.After(time.Second):
			t.Fatal("close waited on an IO mutex")
		}
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

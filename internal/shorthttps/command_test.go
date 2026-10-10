package shorthttps

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInitializationPairsPrivateIPTrustWithoutOverwritingExistingState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "paired")
	if err := initialize("127.0.0.1", "127.0.0.1:9900", dir, 8443, io.Discard); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(dir, "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	var server, client FileConfig
	for name, cfg := range map[string]*FileConfig{"server.json": &server, "client.json": &client} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, cfg); err != nil {
			t.Fatal(err)
		}
	}
	if server.Token != client.Token || len(client.Token) < 32 || client.Key != "" || client.Certificate != "" || client.Target != "" {
		t.Fatal("pairing lost authentication or exposed server identity")
	}
	block, _ := pem.Decode([]byte(client.CA))
	if block == nil {
		t.Fatal("missing public trust")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "127.0.0.1", Roots: roots}); err != nil {
		t.Fatalf("generated IP trust is not verifiable: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(dir, "server.key"))
		if info.Mode().Perm() != 0600 {
			t.Fatal("private key is not owner-only")
		}
	}
	if err := initialize("127.0.0.1", "127.0.0.1:9900", dir, 8443, io.Discard); err == nil {
		t.Fatal("existing paired state was overwritten")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "server.key"))
	if string(after) != string(key) {
		t.Fatal("failed initialization replaced the private key")
	}
}

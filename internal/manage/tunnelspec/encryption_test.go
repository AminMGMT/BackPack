package tunnelspec

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
)

// The encryption is written only when set, so a tunnel without one keeps its
// file as it was; and it reads back.
func TestEncryptionIsRenderedOnlyWhenSet(t *testing.T) {
	for _, role := range []string{"server", "client"} {
		s := Spec{Role: role, Transport: "tcpmux", Ports: []string{"443"}, RemoteAddr: "192.0.2.1:3080", BindAddr: "0.0.0.0:3080"}
		if out := s.Render(); strings.Contains(out, "encryption") || strings.Contains(out, "splash") {
			t.Fatalf("%s: a tunnel without encryption wrote one:\n%s", role, out)
		}
		s.SetEncryption(config.EncStealth)
		out := s.Render()
		if !strings.Contains(out, `encryption = "stealth"`) {
			t.Fatalf("%s: stealth not written:\n%s", role, out)
		}
		var c config.Config
		if _, err := toml.Decode(out, &c); err != nil {
			t.Fatal(err)
		}
		got := c.Server.EncryptionConfig
		if role == "client" {
			got = c.Client.EncryptionConfig
		}
		if got.EncryptionMode() != config.EncStealth {
			t.Fatalf("%s: read back %q", role, got.EncryptionMode())
		}
	}
}

// A mode the transport does not carry falls back to none, so a transport
// change never leaves a tunnel the engine refuses to start.
func TestSetEncryptionFallsBackToNone(t *testing.T) {
	s := Spec{Role: "server", Transport: "kcp"}
	s.SetEncryption(config.EncStealth)
	if s.Encryption.Encrypted() {
		t.Fatal("kcp kept an encryption it does not carry")
	}
	s = Spec{Role: "server", Transport: "ws"}
	s.SetEncryption("shadow")
	if s.Encryption.Encrypted() {
		t.Fatal("ws kept shadow, which it does not carry")
	}
	if modes := (Spec{Transport: "tcp", NaiveServer: config.NaiveServerConfig{Listen: "0.0.0.0:443"}}).EncryptionModes(); modes != nil {
		t.Fatalf("a tunnel under naive offers %v", modes)
	}
}

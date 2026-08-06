package manage

import (
	"net"
	"testing"

	"github.com/backpack/backpack/config"
)

func TestRestoreSetRejectsCandidateSocketConflict(t *testing.T) {
	configs := map[string]*config.Config{
		"one": {Server: config.ServerConfig{BindAddr: "0.0.0.0:24443", Transport: config.TCP}},
		"two": {Server: config.ServerConfig{BindAddr: "127.0.0.1:24443", Transport: config.TCP}},
	}
	if err := validateRestoreClaims(configs); err == nil {
		t.Fatal("overlapping candidate listeners must be rejected before restore")
	}
}

func TestRestoreSetKeepsExplicitFamiliesIndependent(t *testing.T) {
	configs := map[string]*config.Config{
		"v4": {Server: config.ServerConfig{BindAddr: "0.0.0.0:24443", Transport: config.TCP}},
		"v6": {Server: config.ServerConfig{BindAddr: "[::]:24443", Transport: config.TCP}},
	}
	if err := validateRestoreClaims(configs); err != nil {
		t.Fatalf("explicit IPv4 and IPv6 candidates should be independent: %v", err)
	}
}

func TestRestoreProbeRejectsExternalListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	configs := map[string]*config.Config{
		"candidate": {Server: config.ServerConfig{BindAddr: listener.Addr().String(), Transport: config.TCP}},
	}
	if err := probeRestoreClaims(configs); err == nil {
		t.Fatal("an external listener must block restore activation")
	}
}

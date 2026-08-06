package manage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/backpack/backpack/config"
)

func loadRenderedForward(t *testing.T, s TunnelSpec) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forward.toml")
	if err := os.WriteFile(path, []byte(s.Render()), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("rendered forward config is invalid: %v\n%s", err, s.Render())
	}
	return cfg
}

func TestForwardIranRoleRendersDiallingClientWithIngress(t *testing.T) {
	s := TunnelSpec{
		Name: "iran", Role: "server", Engine: config.EngineForward,
		Transport: "tcp", RemoteAddr: "192.0.2.10:443", Token: "secret",
		Ports: []string{"8443=127.0.0.1:8443"}, KeepAlive: 75,
	}
	cfg := loadRenderedForward(t, s)
	if !cfg.HasClient() || cfg.HasServer() || len(cfg.Client.Ports) != 1 {
		t.Fatalf("Iran forward role rendered with wrong operational section: %#v", cfg)
	}
	if strings.Contains(s.Render(), "[forward]") || !strings.Contains(s.Render(), "engine = \"forward\"") {
		t.Fatalf("application forward was confused with iptables config:\n%s", s.Render())
	}
}

func TestForwardKharejRoleRendersListeningServerWithoutIngress(t *testing.T) {
	s := TunnelSpec{
		Name: "kharej", Role: "client", Engine: config.EngineForward,
		Transport: "tcp", BindAddr: "0.0.0.0:443", Token: "secret",
		ChannelSize: 2048, KeepAlive: 75, Heartbeat: 40,
	}
	cfg := loadRenderedForward(t, s)
	if !cfg.HasServer() || cfg.HasClient() || len(cfg.Server.Ports) != 0 {
		t.Fatalf("Kharej forward role rendered with wrong operational section: %#v", cfg)
	}
}

func TestForwardConfigReloadPreservesGeographicRolesAndIngress(t *testing.T) {
	iranRendered := TunnelSpec{
		Name: "iran", Role: "server", Engine: config.EngineForward,
		Transport: "tcpmux", RemoteAddr: "192.0.2.10:443", Token: "secret",
		Ports: []string{"8443=127.0.0.1:9443"}, MaxConnections: 50, BandwidthMbps: 25,
	}.Render()
	iranPath := filepath.Join(t.TempDir(), "iran.toml")
	if err := os.WriteFile(iranPath, []byte(iranRendered), 0o600); err != nil {
		t.Fatal(err)
	}
	iranCfg, err := config.LoadFile(iranPath)
	if err != nil {
		t.Fatal(err)
	}
	iran, err := clientSpecFromConfig("iran", iranCfg)
	if err != nil {
		t.Fatal(err)
	}
	if iran.Role != "server" || iran.Engine != config.EngineForward || len(iran.Ports) != 1 || iran.MaxConnections != 50 || iran.BandwidthMbps != 25 {
		t.Fatalf("Iran Direct spec lost settings on reload: %#v", iran)
	}
	if !strings.Contains(iran.Render(), "[client]") || strings.Contains(iran.Render(), "[server]") {
		t.Fatalf("editing Iran Direct would flip its operational role:\n%s", iran.Render())
	}

	kharejRendered := TunnelSpec{
		Name: "kharej", Role: "client", Engine: config.EngineForward,
		Transport: "wss", BindAddr: "0.0.0.0:443", Token: "secret",
		TLSCert: "/tmp/cert", TLSKey: "/tmp/key",
	}.Render()
	kharejPath := filepath.Join(t.TempDir(), "kharej.toml")
	if err := os.WriteFile(kharejPath, []byte(kharejRendered), 0o600); err != nil {
		t.Fatal(err)
	}
	kharejCfg, err := config.LoadFile(kharejPath)
	if err != nil {
		t.Fatal(err)
	}
	kharej, err := serverSpecFromConfig("kharej", kharejCfg)
	if err != nil {
		t.Fatal(err)
	}
	if kharej.Role != "client" || !kharej.operationalServer() || kharej.TLSCert != "/tmp/cert" {
		t.Fatalf("Kharej Direct spec lost its operational server identity: %#v", kharej)
	}
}

func TestForwardTunnelDisplayRoleStaysGeographic(t *testing.T) {
	iran := Tunnel{Engine: string(config.EngineForward), Role: "client"}
	kharej := Tunnel{Engine: string(config.EngineForward), Role: "server"}
	if iran.DisplayRole() != "server" || kharej.DisplayRole() != "client" {
		t.Fatalf("display roles changed sides: Iran=%q Kharej=%q", iran.DisplayRole(), kharej.DisplayRole())
	}
}

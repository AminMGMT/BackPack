package cmd

import (
	"strings"
	"testing"

	"github.com/backpack/backpack/config"
)

// UDP alone on a transport that forwards TCP only would open ports that
// listen for nothing, so it is refused with what to do instead. "both" there
// still starts — the ports carry TCP and the engine says UDP is not forwarded
// — so a tunnel an older file describes keeps working.
func TestPortProtocolIsCheckedAgainstTheTransport(t *testing.T) {
	cfg := func(tr config.TransportType, proto string) *config.Config {
		return &config.Config{Server: config.ServerConfig{Transport: tr, PortProtocol: proto}}
	}
	if err := checkPortProtocol(cfg(config.TCPMUX, "udp")); err == nil || !strings.Contains(err.Error(), "TCP only") {
		t.Fatalf("udp on tcpmux: %v, want a refusal that says TCP only", err)
	}
	for _, c := range []struct {
		tr    config.TransportType
		proto string
	}{{config.TCPMUX, "both"}, {config.TCP, "udp"}, {config.KCP, "udp"}, {config.WS, "tcp"}, {config.WS, ""}} {
		if err := checkPortProtocol(cfg(c.tr, c.proto)); err != nil {
			t.Errorf("%s with %q refused: %v", c.tr, c.proto, err)
		}
	}
	if err := checkPortProtocol(cfg(config.TCP, "tcp+udp")); err == nil {
		t.Error("an unknown port_protocol was accepted")
	}
}

// An encryption the transport does not carry is refused at load, rather than
// a tunnel that runs without it; so is one under a managed HTTPS helper,
// which is encrypted by the helper.
func TestEncryptionIsCheckedAgainstTheTransport(t *testing.T) {
	server := func(tr config.TransportType, mode string) *config.Config {
		c := &config.Config{Server: config.ServerConfig{Transport: tr}}
		c.Server.Encryption = mode
		return c
	}
	client := func(tr config.TransportType, mode string) *config.Config {
		c := &config.Config{Client: config.ClientConfig{Transport: tr}}
		c.Client.Encryption = mode
		return c
	}
	for _, tr := range []config.TransportType{config.TCP, config.TCPMUX, config.WS, config.WSS, config.WSMUX, config.WSSMUX} {
		if err := checkEncryption(server(tr, "stealth")); err != nil {
			t.Errorf("stealth on %s refused on the server: %v", tr, err)
		}
		if err := checkEncryption(client(tr, "stealth")); err != nil {
			t.Errorf("stealth on %s refused on the client: %v", tr, err)
		}
	}
	if err := checkEncryption(server(config.KCP, "stealth")); err == nil || !strings.Contains(err.Error(), "only tcp, tcpmux") {
		t.Errorf("stealth on kcp: %v, want a refusal naming the stream transports", err)
	}
	if err := checkEncryption(client(config.TCP, "aes")); err == nil {
		t.Error("an unknown encryption was accepted")
	}
	c := server(config.TCP, "stealth")
	c.Server.Naive.Listen = "0.0.0.0:443"
	if err := checkEncryption(c); err == nil || !strings.Contains(err.Error(), "managed HTTPS helper") {
		t.Errorf("stealth under naive: %v, want a refusal", err)
	}
	// Splash rides TLS on wss and wssmux; elsewhere it needs an encryption.
	s := server(config.WSS, "")
	s.Server.Splash = true
	if err := checkEncryption(s); err != nil {
		t.Errorf("splash on wss refused: %v", err)
	}
	s = server(config.TCP, "")
	s.Server.Splash = true
	if err := checkEncryption(s); err == nil {
		t.Error("splash on plain tcp without an encryption was accepted")
	}
	s = server(config.TCP, "stealth")
	s.Server.Splash = true
	if err := checkEncryption(s); err != nil {
		t.Errorf("splash with stealth on tcp refused: %v", err)
	}
}

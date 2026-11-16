package manage

import (
	"net"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/backpack/backpack/config"
)

// What the Add form says about the kharej reaches the kharej through the
// setup link, so `link apply` there builds it with no question to ask: the
// address to dial, the CDN edge of a websocket transport, and the backups.
func TestTheKharejSettingsTravelInTheSetupLink(t *testing.T) {
	n := NewTunnel{Role: "server", Transport: "wss", Name: "cdn-wss", TunnelPort: "8443",
		Token: "a-token-long-enough-to-be-accepted-0123456789", Ports: "443",
		KharejDial: "tunnel.example.com", KharejEdge: "104.16.0.1", KharejBackups: "5.6.7.8, backup.example.com"}
	k, err := kharejLinkSettings(n, "wss")
	if err != nil {
		t.Fatal(err)
	}
	s := TunnelSpec{Role: "server", Transport: "wss", Name: n.Name, Token: n.Token,
		BindAddr: "0.0.0.0:8443", Ports: []string{"443"}}
	ApplyPreset(&s, PresetTurbo)
	var cfg config.Config
	if _, err := toml.Decode(s.Render(), &cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := shareLinkWith(n.Name, k.dial, cfg, linkExtras{hosts: k.backups, edge: k.edge})
	if err != nil {
		t.Fatal(err)
	}
	link, err := DecodeShareLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	kharej, err := kharejFromLink(link, LinkApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if kharej.RemoteAddr != net.JoinHostPort("tunnel.example.com", "8443") {
		t.Errorf("the kharej dials %q, not the address the form named", kharej.RemoteAddr)
	}
	if kharej.EdgeIP != "104.16.0.1" {
		t.Errorf("the kharej's edge IP is %q", kharej.EdgeIP)
	}
	if strings.Join(kharej.FallbackAddrs, ",") != "5.6.7.8:8443,backup.example.com:8443" {
		t.Errorf("the kharej's backups are %v", kharej.FallbackAddrs)
	}

	// An edge on a transport that is not a websocket one is not carried.
	plain := s
	plain.Transport = "tcp"
	cfg = config.Config{}
	if _, err := toml.Decode(plain.Render(), &cfg); err != nil {
		t.Fatal(err)
	}
	raw, err = shareLinkWith(n.Name, "1.2.3.4", cfg, linkExtras{edge: "104.16.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if link, _ = DecodeShareLink(raw); link.Edge != "" {
		t.Errorf("a tcp link carries the edge %q", link.Edge)
	}
}

// The form's kharej settings are checked before anything is written.
func TestTheKharejSettingsAreChecked(t *testing.T) {
	base := NewTunnel{Role: "server", Transport: "tcp"}
	for _, c := range []struct {
		edit func(*NewTunnel)
		want string
	}{
		{func(n *NewTunnel) { n.KharejEdge = "104.16.0.1" }, "websocket"},
		{func(n *NewTunnel) { n.Transport, n.KharejEdge = "ws", "cdn.example.com" }, "IP address"},
		{func(n *NewTunnel) { n.KharejDial = "tunnel.example.com:443" }, "without a port"},
	} {
		n := base
		c.edit(&n)
		if _, err := kharejLinkSettings(n, n.Transport); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: got %v, want an error about %q", n, err, c.want)
		}
	}
	ok := base
	ok.KharejDial = "2001:db8::1"
	if _, err := kharejLinkSettings(ok, "tcp"); err != nil {
		t.Errorf("an IPv6 address to dial was refused: %v", err)
	}
}

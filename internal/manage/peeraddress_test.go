package manage

import (
	"os"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
)

// Reported on v1.8.4: when the kharej's IP changed there was nowhere in Edit to
// say so, and the direct tunnel had to be deleted and set up again.

func TestANewKharejAddressKeepsThePortUnlessOneIsGiven(t *testing.T) {
	for _, tc := range []struct{ current, in, want string }{
		{"198.51.100.4:9000", "203.0.113.7", "203.0.113.7:9000"},
		{"198.51.100.4:9000", " 203.0.113.7:9443 ", "203.0.113.7:9443"},
		{"198.51.100.4:9000", "kharej.example.com", "kharej.example.com:9000"},
		{"[2001:db8::1]:9000", "2001:db8::2", "[2001:db8::2]:9000"},
		{"[2001:db8::1]:9000", "[2001:db8::2]:443", "[2001:db8::2]:443"},
	} {
		got, err := withPeerHost(tc.current, tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%q + %q = %q, %v; want %q", tc.current, tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "   ", "203.0.113.7:http", "a b"} {
		if got, err := withPeerHost("198.51.100.4:9000", bad); err == nil {
			t.Errorf("%q was accepted as %q", bad, got)
		}
	}
}

// The change reaches the file the engine reads: the tunnel is re-rendered
// through the wizard's renderer with the new address and nothing else moved.
func TestMovingTheKharejRewritesTheL3ConfigAndTheSpoofPeer(t *testing.T) {
	var cfg config.Config
	src := `[l3]
mode = "dial"
addr = "198.51.100.4:9000"
token = "tok-0123456789abcdef0123456789abcdef"
carrier = "spoof"
iface = "bp0"
local_ip = "10.200.0.1/30"
peer_ip = "10.200.0.2"
mtu = 1300
spoof_src_ip = "192.0.2.10"
spoof_peer_ip = "198.51.100.4"
`
	if _, err := toml.Decode(src, &cfg); err != nil {
		t.Fatal(err)
	}
	l := cfg.L3
	movePeer(&l, "203.0.113.7:9000")

	var got config.Config
	if _, err := toml.Decode(l3SpecOf(Tunnel{Name: "t1"}, l).Render(), &got); err != nil {
		t.Fatalf("the edited config does not parse: %v", err)
	}
	if got.L3.Addr != "203.0.113.7:9000" {
		t.Errorf("addr = %q", got.L3.Addr)
	}
	if got.L3.SpoofPeerIP != "203.0.113.7" {
		t.Errorf("spoof_peer_ip = %q; it still points at the address the kharej left", got.L3.SpoofPeerIP)
	}
	if got.L3.Token != l.Token || got.L3.MTU != 1300 || got.L3.LocalIP != "10.200.0.1/30" {
		t.Errorf("moving the peer changed something else: %+v", got.L3)
	}

	// A spoof peer set to something else on purpose is left alone.
	l = cfg.L3
	l.SpoofPeerIP = "192.0.2.99"
	movePeer(&l, "203.0.113.7:9000")
	if l.SpoofPeerIP != "192.0.2.99" {
		t.Errorf("a deliberately different spoof peer was overwritten: %q", l.SpoofPeerIP)
	}
}

// Both editors offer it, on the side that dials — a guard on the menus, since
// the bug was that there was no entry at all.
func TestTheDirectEditorsOfferTheKharejAddress(t *testing.T) {
	b, err := os.ReadFile("directedit.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`{Title: "Kharej Address", Desc: "now " + d.Addr}`,
		`options = append(options, tui.Option{Title: "Kharej Address"`,
		`Title: "Iran Real IP"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("directedit.go no longer offers %s", want)
		}
	}
}

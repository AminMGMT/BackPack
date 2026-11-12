package config

import (
	"testing"

	"github.com/BurntSushi/toml"
)

// The gate between the two engines. Everything else about the layer-3 tunnel
// is tested in internal/tunnel/l3; what matters here is only that an existing
// configuration cannot reach it.

func TestExistingConfigsDoNotEnableL3(t *testing.T) {
	// A representative reverse server and client, in the shape the tool has
	// always written them.
	files := map[string]string{
		"reverse server": `
[server]
bind_addr = "0.0.0.0:8443"
transport = "tcpmux"
token = "a-long-token"
ports = ["443=127.0.0.1:443", "2053-2060"]
accept_udp = true
`,
		"reverse client": `
[client]
remote_addr = "1.2.3.4:8443"
transport = "tcpmux"
token = "a-long-token"
connection_pool = 8
`,
		"spoof relay client": `
[client]
remote_addr = "1.2.3.4:8443"
transport = "spoof"
token = "a-long-token"
spoof_mode = "relay"
spoof_forward = "127.0.0.1:51820"
`,
		"empty": ``,
	}

	for name, text := range files {
		var cfg Config
		if _, err := toml.Decode(text, &cfg); err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		if cfg.L3.Enabled() {
			t.Fatalf("%s: a configuration with no [l3] table reports a layer-3 tunnel", name)
		}
		if cfg.Direct.Present() {
			t.Fatalf("%s: a configuration with no [direct] table reports a direct tunnel", name)
		}
	}
}

// The [direct] stream tunnel was removed in v2.0.0, and its table is still
// read — just enough for a file that has one to be recognised and refused by
// name, rather than decoding as a file with no tunnel in it.
func TestARemovedDirectTableIsStillRecognised(t *testing.T) {
	const text = `
[direct]
role      = "kharej"
addr      = "0.0.0.0:8443"
token     = "a-long-token"
transport = "stealth"
ports     = ["443", "2053-2060"]
sessions  = 4
mux_recievebuffer = 4194304
`
	var cfg Config
	if _, err := toml.Decode(text, &cfg); err != nil {
		t.Fatalf("a [direct] file with keys that are no longer read does not decode: %v", err)
	}
	if !cfg.Direct.Present() {
		t.Fatal("a configuration with a [direct] table does not report one")
	}
	if cfg.L3.Enabled() || cfg.Server.BindAddr != "" || cfg.Client.RemoteAddr != "" {
		t.Fatal("a [direct]-only file reported another kind of tunnel")
	}
	if cfg.Direct.OnIran() || cfg.Direct.Transport != "stealth" || len(cfg.Direct.Ports) != 2 {
		t.Fatalf("decoded [direct] = %+v", cfg.Direct)
	}
	for _, role := range []string{"", " ", "\t"} {
		if (LegacyDirectConfig{Role: role}).Present() {
			t.Fatalf("role %q reports a [direct] table", role)
		}
	}
	for _, role := range []string{"iran", "IRAN", " edge "} {
		if !(LegacyDirectConfig{Role: role}).OnIran() {
			t.Errorf("role %q is the Iran side", role)
		}
	}
}

func TestL3TableEnablesTheTunnel(t *testing.T) {
	const text = `
[l3]
mode     = "dial"
addr     = "1.2.3.4:9000"
token    = "a-long-token"
encap    = "gre"
gre_key  = 42
iface    = "bp1"
local_ip = "10.10.0.1/30"
peer_ip  = "10.10.0.2"
mtu      = 1380
sockbuf  = 8388608
`
	var cfg Config
	if _, err := toml.Decode(text, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !cfg.L3.Enabled() {
		t.Fatal("a configuration with an [l3] table does not report a layer-3 tunnel")
	}

	// Compared field by field rather than whole: the embedded SpoofConfig
	// carries a slice, so L3Config is not a comparable type.
	for _, check := range []struct {
		field     string
		got, want any
	}{
		{"mode", cfg.L3.Mode, "dial"},
		{"addr", cfg.L3.Addr, "1.2.3.4:9000"},
		{"token", cfg.L3.Token, "a-long-token"},
		{"encap", cfg.L3.Encap, "gre"},
		{"gre_key", cfg.L3.GREKey, uint32(42)},
		{"iface", cfg.L3.Iface, "bp1"},
		{"local_ip", cfg.L3.LocalIP, "10.10.0.1/30"},
		{"peer_ip", cfg.L3.PeerIP, "10.10.0.2"},
		{"mtu", cfg.L3.MTU, 1380},
		{"sockbuf", cfg.L3.SockBuf, 8388608},
	} {
		if check.got != check.want {
			t.Fatalf("%s = %v, want %v", check.field, check.got, check.want)
		}
	}

	// The reverse halves stay empty, which is what keeps the two engines from
	// both claiming the same file.
	if cfg.Server.BindAddr != "" || cfg.Client.RemoteAddr != "" {
		t.Fatal("an [l3]-only file populated [server] or [client]")
	}
}

// The spoof_* and pck_* keys sit at the top level of [l3], exactly as they do
// in [server] and [client], so an operator who knows them already knows these.
func TestL3CarrierKeysDecodeAtTheTopLevel(t *testing.T) {
	const text = `
[l3]
mode           = "listen"
addr           = "0.0.0.0:9000"
token          = "a-long-token"
carrier        = "spoof"
local_ip       = "10.10.0.2/30"
peer_ip        = "10.10.0.1"
spoof_profile  = "icmp"
spoof_src_ip   = "198.51.100.7"
spoof_peer_ip  = "203.0.113.9"
spoof_src_pool = ["198.51.100.7", "198.51.100.8"]
spoof_padding  = true
pck_interface  = "eth0"
pck_flags      = ["PA", "A"]
`
	var cfg Config
	if _, err := toml.Decode(text, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.L3.Carrier != "spoof" {
		t.Fatalf("carrier = %q, want spoof", cfg.L3.Carrier)
	}
	if cfg.L3.SpoofProfile != "icmp" {
		t.Fatalf("spoof_profile = %q, want icmp", cfg.L3.SpoofProfile)
	}
	if cfg.L3.SpoofPeerIP != "203.0.113.9" {
		t.Fatalf("spoof_peer_ip = %q, want 203.0.113.9", cfg.L3.SpoofPeerIP)
	}
	if len(cfg.L3.SpoofSrcPool) != 2 {
		t.Fatalf("spoof_src_pool has %d entries, want 2", len(cfg.L3.SpoofSrcPool))
	}
	if !cfg.L3.SpoofPadding {
		t.Fatal("spoof_padding did not decode")
	}
	if cfg.L3.PckInterface != "eth0" {
		t.Fatalf("pck_interface = %q, want eth0", cfg.L3.PckInterface)
	}
	if len(cfg.L3.PckFlags) != 2 {
		t.Fatalf("pck_flags has %d entries, want 2", len(cfg.L3.PckFlags))
	}

	// The reverse halves have no carrier of their own to read these with —
	// spoof is a direct-tunnel carrier and nothing else — so what proves the
	// separation now is that [server] and [client] were not filled at all.
	if cfg.Server.BindAddr != "" || cfg.Client.RemoteAddr != "" {
		t.Fatal("an [l3] table filled in the reverse tunnel's tables")
	}
}

// Whitespace is not a mode. A table left half-written must not start a tunnel
// that then fails validation at a less helpful moment.
func TestL3BlankModeIsDisabled(t *testing.T) {
	for _, mode := range []string{"", " ", "\t"} {
		if (L3Config{Mode: mode}).Enabled() {
			t.Fatalf("mode %q reports a layer-3 tunnel", mode)
		}
	}
}

package manage

import (
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
)

// An Edit of a direct tunnel keeps what a person wrote in the file: its header,
// the notes above a key, the comment at the end of a line. The correctness bar
// is that the file still means exactly what a full re-render would have said.

// reRendered is the file an edit wrote before comments were kept: the render
// of the edited spec, with the foreign keys put back.
func reRendered(t *testing.T, raw string, e DirectEdit) string {
	t.Helper()
	var cfg config.Config
	md, err := toml.Decode(raw, &cfg)
	if err != nil {
		t.Fatalf("the test file does not parse: %v", err)
	}
	l, err := editedL3("t", cfg, e)
	if err != nil {
		t.Fatalf("the edit was refused: %v", err)
	}
	body, err := withForeignKeys(directSpecFrom("t", l).Render(), raw, md)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func decodeMap(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if _, err := toml.Decode(body, &m); err != nil {
		t.Fatalf("does not parse: %v\n%s", err, body)
	}
	return m
}

func TestEditKeepsTheComments(t *testing.T) {
	str := func(s string) *string { return &s }
	num := func(n int) *int { return &n }
	yes := true

	cases := []struct {
		name string
		raw  string
		edit DirectEdit
		// keep are lines (comments, with the keys they sit on) that must come out as written.
		keep []string
		// has are the lines the edit changed or added.
		has []string
		// gone are text that must not be in the result.
		gone []string
		// inOrder are lines that must appear in this order.
		inOrder []string
	}{
		{
			name: "xdi: change, add, remove, foreign key, hash in a string",
			raw: `# My Iran side to the Frankfurt box.
# Do not touch the token — it is shared with the other server.

[l3]
mode = "dial"
addr = "203.0.113.9:9000"   # Frankfurt, port 9000
token = "ab#cd-long-token"  # has a # inside, and = too
carrier = "xdi"
encap = "gre"

# tunnel addresses
iface = "bp1"
local_ip = "10.10.1.1/30"
peer_ip = "10.10.1.2"
mtu = 1400   # was 1500, lowered after the ISP change
auto_mtu = false # measured it by hand
future_knob = "kept"   # from a newer version

# what the panel forwards
ports = ["443", "8443"] # web and alt
accept_udp = true
`,
			edit: DirectEdit{Ports: str("443, 2053"), MTU: num(1300), AutoMTU: &yes, FEC: &yes},
			keep: []string{
				"# My Iran side to the Frankfurt box.",
				"# Do not touch the token — it is shared with the other server.",
				`addr = "203.0.113.9:9000"   # Frankfurt, port 9000`,
				`token = "ab#cd-long-token"  # has a # inside, and = too`,
				"# tunnel addresses",
				`future_knob = "kept"   # from a newer version`,
				"# what the panel forwards",
			},
			has:  []string{"mtu = 1300   # was 1500, lowered after the ISP change", `ports = ["443", "2053"] # web and alt`, "fec_data", "fec_parity"},
			gone: []string{"auto_mtu", "measured it by hand"},
		},
		{
			name: "pck: ports removed, a tuning key added",
			raw: `# Iran -> pck link
[l3]
mode = "dial"
addr = "203.0.113.9:9000"
token = "a-long-token-for-pck"
carrier = "pck"
preset = "turbo" # keep turbo

# the capture interface
pck_interface = "eth0"  # not ens3
sockbuf = 8388608

ports = ["443"] # one port
accept_udp = true
`,
			edit:    DirectEdit{Ports: str(""), PckConns: num(3)},
			keep:    []string{"# Iran -> pck link", `preset = "turbo" # keep turbo`, "# the capture interface", `pck_interface = "eth0"  # not ens3`},
			has:     []string{"pck_conns"},
			gone:    []string{"# one port", "ports", "accept_udp"},
			inOrder: []string{`carrier = "pck"`, `preset = "turbo"`, "pck_conns", "# the capture interface"},
		},
		{
			name: "sni: limits added after the ports",
			raw: `# sni link, set up by hand
[l3]
mode = "dial"
addr = "203.0.113.9:443"
token = "tok#en"
carrier = "sni"
sni_domain = "cdn.example.com" # looks like a real site
encap = "gre"
iface = "bp1"
local_ip = "10.10.1.1/30"
peer_ip = "10.10.1.2"
mtu = 1400
ports = ["443"]   # web
accept_udp = false
`,
			edit:    DirectEdit{MaxConnections: num(500), BandwidthMbps: num(100)},
			keep:    []string{"# sni link, set up by hand", `sni_domain = "cdn.example.com" # looks like a real site`, `ports = ["443"]   # web`},
			has:     []string{"max_connections", "bandwidth_mbps"},
			inOrder: []string{"accept_udp", "max_connections", "bandwidth_mbps"},
		},
		{
			name: "spoof: ports changed, forged-source keys and their notes stay",
			raw: `# Forged-source link
[l3]
mode = "dial"
addr = "203.0.113.9:9000"
token = "spoof-token-1234"
carrier = "spoof"
encap = "gre"
iface = "bp1"
local_ip = "10.10.1.1/30"
peer_ip = "10.10.1.2"
mtu = 1400

# the forged side
spoof_profile = "tcp"   # looks like TCP
spoof_peer_ip = "198.51.100.7"
spoof_src_ip = "192.0.2.50" # must be routable here

ports = ["443"]
accept_udp = true
`,
			edit: DirectEdit{Ports: str("443, 8443")},
			keep: []string{"# Forged-source link", "# the forged side", `spoof_profile = "tcp"   # looks like TCP`,
				`spoof_src_ip = "192.0.2.50" # must be routable here`},
			has: []string{`ports = ["443", "8443"]`},
		},
		{
			name: "gre: the 'no mtu' note goes when mtu is set",
			raw: `# GRE to Frankfurt (hand edited)
[l3]
mode = "dial"
carrier = "gre"
addr = "203.0.113.9"  # the real address
gre_key = 41007

iface = "bp2"
local_ip = "10.10.2.1/30"
peer_ip = "10.10.2.2"
# No mtu: it is worked out at start from the interface the route to the
# other server leaves by.

# my ports
ports = ["443"]
accept_udp = true
`,
			edit:    DirectEdit{MTU: num(1380)},
			keep:    []string{"# GRE to Frankfurt (hand edited)", `addr = "203.0.113.9"  # the real address`, "# my ports"},
			has:     []string{"mtu"},
			gone:    []string{"# No mtu"},
			inOrder: []string{`peer_ip = "10.10.2.2"`, "mtu = 1380", "# my ports"},
		},
		{
			name: "ip6gre: ports off, header and key notes stay",
			raw: `# ip6gre
[l3]
mode = "dial"
carrier = "ip6gre"
addr = "2001:db8::9"  # v6 of the box
gre_key = 41007
iface = "bp2"
local_ip = "10.10.2.1/30"
peer_ip = "10.10.2.2"
mtu = 1380 # v6 overhead
ports = ["443"] # to be removed
accept_udp = true
`,
			edit: DirectEdit{Ports: str("")},
			keep: []string{"# ip6gre", `addr = "2001:db8::9"  # v6 of the box`, "mtu = 1380 # v6 overhead"},
			gone: []string{"ports", "accept_udp", "to be removed"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := editedDirectBody("t", tc.raw, tc.edit)
			if err != nil {
				t.Fatal(err)
			}
			// The bar: the same config a full re-render writes.
			want := reRendered(t, tc.raw, tc.edit)
			if !reflect.DeepEqual(decodeMap(t, got), decodeMap(t, want)) {
				t.Errorf("not the same config as a re-render:\n--- got\n%s\n--- re-rendered\n%s", got, want)
			}
			for _, k := range tc.keep {
				if !strings.Contains(got, k+"\n") {
					t.Errorf("lost %q:\n%s", k, got)
				}
			}
			for _, h := range tc.has {
				if !strings.Contains(got, h) {
					t.Errorf("missing %q:\n%s", h, got)
				}
			}
			for _, g := range tc.gone {
				if strings.Contains(got, g) {
					t.Errorf("still has %q:\n%s", g, got)
				}
			}
			at := -1
			for _, o := range tc.inOrder {
				i := strings.Index(got[max(at, 0):], o)
				if i < 0 {
					t.Errorf("%q is not after the previous line:\n%s", o, got)
					break
				}
				at = max(at, 0) + i
			}
		})
	}
}

// A file the line editor will not touch is written as a full re-render, as
// before, and still parses to the same config.
func TestEditFallsBackToARenderForFilesItCannotPatch(t *testing.T) {
	ports := "443, 2053"
	base := `# odd
[l3]
mode = "dial"
addr = "203.0.113.9:9000"
token = "a-long-token"
carrier = "xdi"
encap = "gre"
iface = "bp1"
local_ip = "10.10.1.1/30"
peer_ip = "10.10.1.2"
mtu = 1400
`
	for name, extra := range map[string]string{
		"multi-line array":  "ports = [\n  \"443\",\n]\naccept_udp = true\n",
		"multi-line string": "note = \"\"\"\nline one\n\"\"\"\nports = [\"443\"]\naccept_udp = true\n",
		"inline table":      "ports = [\"443\"]\naccept_udp = true\nmeta = { a = 1 }\n",
		"quoted key":        "ports = [\"443\"]\naccept_udp = true\n\"odd key\" = 1\n",
		"array table":       "ports = [\"443\"]\naccept_udp = true\n\n[[other]]\nx = 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			raw := base + extra
			e := DirectEdit{Ports: &ports}
			got, err := editedDirectBody("t", raw, e)
			if err != nil {
				t.Fatal(err)
			}
			if want := reRendered(t, raw, e); !reflect.DeepEqual(decodeMap(t, got), decodeMap(t, want)) {
				t.Errorf("not the same config as a re-render:\n--- got\n%s\n--- re-rendered\n%s", got, want)
			}
			if strings.Contains(got, "# odd") {
				t.Errorf("expected the plain re-render for this file, got a patch:\n%s", got)
			}
		})
	}
}

func TestPreserveCommentsScansOnlyWhatItUnderstands(t *testing.T) {
	for _, tc := range []struct {
		line string
		ok   bool
	}{
		{`a = "x # not a comment" # a comment`, true},
		{`a = 'it''s'`, false},
		{`a = "esc \" quote # still string" # c`, true},
		{`a = [ "x", "y#z" ] # c`, true},
		{`a = [ "x",`, false},
		{`a = {b = 1}`, false},
		{`a = """x"""`, false},
		{`a.b = 1`, false},
		{`"a" = 1`, false},
		{`[l3.sub] # c`, true},
		{`[[x]]`, false},
	} {
		_, ok := scanTOML(tc.line + "\n")
		if ok != tc.ok {
			t.Errorf("%q: ok = %v, want %v", tc.line, ok, tc.ok)
		}
	}
}

package manage

import (
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tunnel/l3"
)

// What a panel edit of a direct tunnel writes.
//
// The panel used to decode the file into a generic document, change the [l3]
// table and encode it again. That kept nothing: the header and every comment
// went, the keys came out in the encoder's order, and the struct's zero values
// were written as keys the file never had — a token on a GRE link, a qdisc and
// a txqueuelen on a carrier that never reads them. The edit now goes through
// the same renderer the CLI's Edit uses, so these tests hold the two together.

// panelEditCase is one tunnel as a setup writes it, and the edit to put it
// through.
type panelEditCase struct {
	name string
	spec l3Spec
	edit func() DirectEdit
	// took reports whether the edit's change is in the new file.
	took func(l config.L3Config) string
	// comments are lines a setup writes for this carrier, which an edit keeps.
	comments []string
	// added are the keys the edit itself turns on, which the file may gain.
	added []string
	// absent are keys this carrier's file must never grow.
	absent []string
}

// tomlKeys is the key set of the [l3] table.
func l3KeySet(t *testing.T, body string) map[string]bool {
	t.Helper()
	var doc struct {
		L3 map[string]any `toml:"l3"`
	}
	if _, err := toml.Decode(body, &doc); err != nil {
		t.Fatalf("does not parse: %v\n%s", err, body)
	}
	keys := map[string]bool{}
	for k := range doc.L3 {
		keys[k] = true
	}
	return keys
}

func commentLines(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

func panelEditCases() []panelEditCase {
	ports := func(v string) func() DirectEdit {
		return func() DirectEdit {
			mtu := 1380
			return DirectEdit{Ports: &v, MTU: &mtu}
		}
	}

	pck := l3Spec{
		Name: "xdi-iran", Side: sideIran, Carrier: "xdi", Encap: "gre",
		Addr: "203.0.113.9:9000", Token: "a-long-token", Iface: "bp1",
		LocalIP: "10.10.1.1/30", PeerIP: "10.10.1.2", MTU: 1400,
		Ports: []string{"443", "8443"}, AcceptUDP: true,
		SockBuf: 8 << 20, MSSClamp: 1200,
	}
	findL3Preset(PresetTurbo).apply(&pck)
	pck.Pck.PckInterface = "eth0"
	pck.Pck.PckFlags = []string{"PA", "A"}

	xdi := l3Spec{
		Name: "xdi-kharej", Side: sideKharej, Carrier: "xdi", Encap: "gre",
		Addr: "0.0.0.0:9000", Token: "another-long-token", Iface: "bp2",
		LocalIP: "10.10.2.2/30", PeerIP: "10.10.2.1", MTU: 1400,
	}
	findL3Preset(PresetTurbo).apply(&xdi)

	gre := greIranSpec()
	gre.Name = "gre-iran"

	gre6 := greIranSpec()
	gre6.Name = "gre6-iran"
	gre6.Carrier = l3.CarrierIP6GRE
	gre6.Addr = "2001:db8::9"

	cases := []panelEditCase{
		{
			name: "xdi-iran", spec: pck, edit: ports("443, 2053"),
			took: func(l config.L3Config) string {
				if strings.Join(l.Ports, ",") != "443,2053" || l.MTU != 1380 {
					return "ports/mtu did not change"
				}
				return ""
			},
			comments: []string{"# Full IP tunnel (layer 3) — a private network between the two servers."},
		},
		{
			name: "xdi", spec: xdi,
			edit: func() DirectEdit { fec := true; mtu := 1300; return DirectEdit{FEC: &fec, MTU: &mtu} },
			took: func(l config.L3Config) string {
				if l.FECData == 0 || l.FECParity == 0 || l.MTU != 1300 {
					return "fec/mtu did not change"
				}
				return ""
			},
			comments: []string{"# Full IP tunnel (layer 3) — a private network between the two servers."},
			added:    []string{"fec_data", "fec_parity"},
		},
		{
			name: "gre", spec: gre, edit: ports("443, 2053"),
			took: func(l config.L3Config) string {
				if strings.Join(l.Ports, ",") != "443,2053" || l.MTU != 1380 || l.GREKey != 41007 {
					return "ports/mtu did not change, or the key moved"
				}
				return ""
			},
			comments: []string{
				"# Kernel GRE (layer 3) — a private network between the two servers.",
				"# NOT ENCRYPTED. The kernel sends this tunnel as bare IP protocol 47, and",
				"# The other server's real address. A GRE link has no port.",
			},
			added:  []string{"mtu"},
			absent: []string{"token", "qdisc", "txqueuelen", "encap", "sockbuf", "fec_data"},
		},
		{
			name: "ip6gre", spec: gre6, edit: ports("8443"),
			took: func(l config.L3Config) string {
				if strings.Join(l.Ports, ",") != "8443" || l.Carrier != l3.CarrierIP6GRE {
					return "ports did not change, or the carrier moved"
				}
				return ""
			},
			comments: []string{"# NOT ENCRYPTED. The kernel sends this tunnel as bare IP protocol 47, and"},
			added:    []string{"mtu"},
			absent:   []string{"token", "qdisc", "txqueuelen", "encap", "sockbuf", "fec_data"},
		},
	}
	return cases
}

func TestPanelEditOfADirectTunnelKeepsTheRenderedFile(t *testing.T) {
	for _, tc := range panelEditCases() {
		t.Run(tc.name, func(t *testing.T) {
			original := tc.spec.Render()
			before := l3KeySet(t, original)

			// An edit that changes nothing is the same file, byte for byte:
			// the renderer is the one author of both.
			same, err := editedDirectBody(tc.spec.Name, original, DirectEdit{})
			if err != nil {
				t.Fatalf("a no-op edit failed: %v", err)
			}
			if same != original {
				t.Errorf("a no-op edit rewrote the file:\n--- before\n%s\n--- after\n%s", original, same)
			}

			body, err := editedDirectBody(tc.spec.Name, original, tc.edit())
			if err != nil {
				t.Fatalf("the edit failed: %v\n%s", err, original)
			}
			cfg := decode(t, body)
			if why := tc.took(cfg.L3); why != "" {
				t.Errorf("%s\n%s", why, body)
			}

			// The header and the carrier's comments survive.
			if !strings.HasPrefix(body, "# ") {
				t.Errorf("the header comment is gone:\n%s", body)
			}
			if first, now := commentLines(original)[0], commentLines(body); len(now) == 0 || now[0] != first {
				t.Errorf("the first comment line changed: %q", now)
			}
			for _, c := range tc.comments {
				if !strings.Contains(body, c+"\n") {
					t.Errorf("the comment %q is gone:\n%s", c, body)
				}
			}

			// Nothing appears that the file did not have, nor that this carrier
			// has no use for, beyond what the edit itself turned on.
			after := l3KeySet(t, body)
			for k := range after {
				if before[k] {
					continue
				}
				if slices.Contains(tc.added, k) {
					continue
				}
				t.Errorf("the edit added a key the file did not have: %s\n%s", k, body)
			}
			for _, k := range tc.absent {
				if after[k] {
					t.Errorf("a %s file gained %q:\n%s", tc.name, k, body)
				}
			}
			// Nothing was lost either: keys the edit did not ask about stay.
			for k := range before {
				if !after[k] && k != "ports" && k != "accept_udp" {
					t.Errorf("the edit dropped %q:\n%s", k, body)
				}
			}

			engine, err := l3.ConfigFromTable(cfg.L3)
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Validate(); err != nil {
				t.Errorf("the engine refuses the edited file: %v\n%s", err, body)
			}

			// And it is the file the CLI would write for the same settings.
			if cli := directSpecFrom(tc.spec.Name, cfg.L3).Render(); cli != body {
				t.Errorf("the panel and the CLI write different files:\n--- panel\n%s\n--- cli\n%s", body, cli)
			}
		})
	}
}

// Keys the file has and the panel does not offer survive: the Fine-Tune ones the
// renderer holds, and keys this engine has no field for.
func TestPanelEditKeepsWhatItDoesNotExpose(t *testing.T) {
	original := `# Hand written.

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
qdisc = "cake"
txqueuelen = 2000
sockbuf = 4194304
mss_clamp = -1
auto_mtu = false
fec_data = 10
fec_parity = 3
pck_interface = "eth0"
future_knob = "kept"
future_list = [1, 2]
ports = ["443"]
accept_udp = true

[l3.future_table]
depth = 3

[other]
note = "kept"
`
	mtu := 1300
	body, err := editedDirectBody("hand", original, DirectEdit{MTU: &mtu})
	if err != nil {
		t.Fatalf("the edit failed: %v", err)
	}
	cfg := decode(t, body)
	l := cfg.L3
	if l.MTU != 1300 {
		t.Errorf("the edit did not take: mtu %d", l.MTU)
	}
	if l.Qdisc != "cake" || l.TxQueueLen != 2000 || l.SockBuf != 4194304 || l.MSSClamp != -1 ||
		l.AutoMTU == nil || *l.AutoMTU || l.FECData != 10 || l.FECParity != 3 || l.PckInterface != "eth0" {
		t.Errorf("a key the panel does not expose was lost: %+v", l)
	}
	var doc map[string]any
	if _, err := toml.Decode(body, &doc); err != nil {
		t.Fatal(err)
	}
	l3t, _ := doc["l3"].(map[string]any)
	if l3t["future_knob"] != "kept" || l3t["future_list"] == nil || l3t["future_table"] == nil {
		t.Errorf("a key this engine has no field for was lost: %v\n%s", l3t, body)
	}
	if other, _ := doc["other"].(map[string]any); other["note"] != "kept" {
		t.Errorf("another table was lost:\n%s", body)
	}
	if !strings.Contains(body, "# Full IP tunnel (layer 3)") {
		t.Errorf("the setup header is gone:\n%s", body)
	}
}

// A preset change goes through the same render, and a GRE file still gets no
// queue keys from it.
func TestPanelEditPresetOnGREAddsNoQueueKeys(t *testing.T) {
	original := greIranSpec().Render()
	p := PresetBalance
	body, err := editedDirectBody("gre-iran", original, DirectEdit{Preset: &p})
	if err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, body)
	if cfg.L3.Preset != PresetBalance {
		t.Errorf("preset = %q", cfg.L3.Preset)
	}
	keys := l3KeySet(t, body)
	for _, k := range []string{"token", "qdisc", "txqueuelen"} {
		if keys[k] {
			t.Errorf("a GRE file gained %q:\n%s", k, body)
		}
	}
}

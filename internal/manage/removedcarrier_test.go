package manage

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/tunnel/l3"
)

// v2.0.0 removed the udp and quic carriers with no migration: a tunnel on one
// is refused at start, with the way out named. That is only fair if the way
// out works — so both editors have to read such a tunnel without the load
// itself failing on the carrier, and save it once it is moved onto one that
// remains. These hold both screens to that, through the same functions the
// screens call; only the file I/O is left out, because ConfigDir is a
// constant a test cannot point somewhere safe.

// removedCarrierFile is a tunnel built before v2.0.0 on a removed carrier,
// with the keys an operator would not want an edit to lose.
func removedCarrierFile(t *testing.T, carrierLine, mode string) config.Config {
	t.Helper()
	body := fmt.Sprintf(`[l3]
mode       = %q
addr       = "203.0.113.9:9000"
token      = "a-long-enough-token-for-the-test"
%s
encap      = "gre"
gre_key    = 77
iface      = "bp3"
local_ip   = "10.10.7.1/30"
peer_ip    = "10.10.7.2"
mtu        = 1380
fec_data   = 10
fec_parity = 3
ports      = ["443", "8080=80"]
accept_udp = true
paths      = 4
`, mode, carrierLine)
	var cfg config.Config
	if _, err := toml.Decode(body, &cfg); err != nil {
		t.Fatalf("the fixture does not parse: %v", err)
	}
	return cfg
}

var removedCarrierCases = []struct{ name, line, shown string }{
	{"udp", `carrier = "udp"`, "udp"},
	{"quic", `carrier = "quic"`, "quic"},
	// No carrier at all meant udp, and is fixed the same way.
	{"empty", ``, "udp"},
}

// The panel's Edit: it opens on the tunnel, says why it cannot start, offers
// the carriers that remain, refuses an edit that would leave it unable to
// start — and saves one that moves it onto pck, keeping everything else.
func TestThePanelEditMovesARemovedCarrierOntoPck(t *testing.T) {
	for _, tc := range removedCarrierCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := removedCarrierFile(t, tc.line, "dial")

			set := directSettingsFrom("old-udp", cfg.L3)
			if set.Carrier != tc.shown {
				t.Errorf("the form shows carrier %q, want %q", set.Carrier, tc.shown)
			}
			if !strings.Contains(set.Unrunnable, "removed in Backpack v2.0.0") {
				t.Errorf("the form does not say why the tunnel cannot start: %q", set.Unrunnable)
			}
			var offered []string
			for _, c := range set.Carriers {
				offered = append(offered, c["value"])
			}
			if got := strings.Join(offered, ","); got != "xdi,pck,"+tc.shown {
				t.Errorf("the carrier menu offers %s, want the panel's two and the tunnel's own", got)
			}

			// Any other change still cannot start, and is refused for the
			// carrier rather than written and then refused at the restart.
			mtu := 1300
			if _, err := editedL3("old-udp", cfg, DirectEdit{MTU: &mtu}); err == nil ||
				!strings.Contains(err.Error(), "removed in Backpack v2.0.0") {
				t.Errorf("an edit that leaves the carrier removed = %v, want the removal named", err)
			}

			// The fix.
			pck := "pck"
			// PCK has no TUN device, so an MTU asked for in the same edit is
			// refused with the reason, not written and ignored.
			if _, err := editedL3("old-udp", cfg, DirectEdit{Carrier: &pck, MTU: &mtu}); err == nil ||
				!strings.Contains(err.Error(), "no MTU to set") {
				t.Errorf("an MTU on the move onto pck = %v, want it refused", err)
			}
			l, err := editedL3("old-udp", cfg, DirectEdit{Carrier: &pck})
			if err != nil {
				t.Fatalf("moving the tunnel onto pck was refused: %v", err)
			}
			if l.Carrier != "pck" {
				t.Errorf("carrier %q, want pck", l.Carrier)
			}
			// What PCK reads is kept; the TUN's keys stay in the struct and are
			// dropped when the file is rendered (see the rendering test).
			if l.Token != cfg.L3.Token || l.Addr != cfg.L3.Addr || len(l.Ports) != 2 || !l.AcceptUDP {
				t.Errorf("the move lost a setting it was not asked about: %+v", l)
			}

			// And the result is a tunnel the engine would start.
			engine, err := l3.ConfigFromTable(l)
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Validate(); err != nil {
				t.Errorf("the engine refuses the edited tunnel: %v", err)
			}

			// Moving it onto another removed carrier is not a fix.
			for _, bad := range []string{"udp", "quic"} {
				if _, err := editedL3("old-udp", cfg, DirectEdit{Carrier: &bad}); err == nil {
					t.Errorf("moving the tunnel onto %s was accepted", bad)
				}
			}
		})
	}
}

// A [direct] stream tunnel has nothing to edit into, and says so.
func TestThePanelEditRefusesARemovedDirectTunnelByName(t *testing.T) {
	var cfg config.Config
	if _, err := toml.Decode(`[direct]
role = "iran"
addr = "203.0.113.9:8443"
transport = "wss"
`, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := editedL3("old-stream", cfg, DirectEdit{}); !errors.Is(err, config.ErrDirectRemoved) {
		t.Fatalf("editing a [direct] tunnel = %v, want ErrDirectRemoved", err)
	}
}

// The CLI's Edit: the Carrier entry asks the wizard's own carrier question and
// re-renders the whole tunnel onto the answer. Driven through the terminal
// with the answer "PCK" (the menu's second entry).
func TestTheCLIEditMovesARemovedCarrierOntoPck(t *testing.T) {
	pckAt := -1
	for i, c := range DirectCarriers() {
		if c["value"] == "pck" {
			pckAt = i + 1
		}
	}
	if pckAt < 0 {
		t.Fatal("the carrier menu has no PCK")
	}
	for _, tc := range removedCarrierCases {
		for _, mode := range []string{"dial", "listen"} {
			t.Run(tc.name+"-"+mode, func(t *testing.T) {
				cfg := removedCarrierFile(t, tc.line, mode)

				restore := tui.SetInput(strings.NewReader(fmt.Sprintf("%d\n", pckAt)))
				var spec l3Spec
				var ok bool
				out := capture(t, func() { spec, ok = askNewL3Carrier(Tunnel{Name: "old-udp"}, cfg.L3) })
				restore()
				if !ok {
					t.Fatalf("the carrier change was refused:\n%s", out)
				}
				if !strings.Contains(out, "Set The Other Server To PCK As Well") {
					t.Errorf("the operator is not told to change the other end too:\n%s", out)
				}

				var back config.Config
				if _, err := toml.Decode(spec.Render(), &back); err != nil {
					t.Fatalf("the edit renders a config that does not parse: %v", err)
				}
				if back.L3.Carrier != "pck" {
					t.Errorf("carrier = %q, want pck", back.L3.Carrier)
				}
				// What PCK reads survives the move; what belonged to the TUN does
				// not, because the PCK file has no such keys.
				wantPorts := len(cfg.L3.Ports)
				if mode == "listen" {
					wantPorts = 0 // the kharej side keeps no port list
				}
				if back.L3.Token != cfg.L3.Token || back.L3.Addr != cfg.L3.Addr || len(back.L3.Ports) != wantPorts {
					t.Errorf("the carrier change lost a setting: %+v", back.L3)
				}
				if back.L3.GREKey != 0 || back.L3.FECData != 0 || back.L3.MTU != 0 || back.L3.Iface != "" || back.L3.LocalIP != "" {
					t.Errorf("the PCK file carries TUN settings it does not read: %+v", back.L3)
				}
				engine, err := l3.ConfigFromTable(back.L3)
				if err != nil {
					t.Fatal(err)
				}
				if err := engine.Validate(); err != nil {
					t.Errorf("the engine refuses the edited tunnel: %v", err)
				}
			})
		}
	}
}

// The editor shows the entry, and says first why the tunnel cannot start —
// read off the file as it is, with nothing on the way refusing the carrier.
func TestTheCLIEditOffersCarrierOnARemovedCarrier(t *testing.T) {
	cfg := removedCarrierFile(t, `carrier = "quic"`, "dial")
	restore := tui.SetInput(strings.NewReader("0\n"))
	out := capture(t, func() { editL3Ports(Tunnel{Name: "old-quic"}, cfg) })
	restore()
	for _, want := range []string{"Cannot Start: The QUIC Carrier Was Removed In v2.0.0", "Carrier"} {
		if !strings.Contains(out, want) {
			t.Errorf("the edit screen does not show %q:\n%s", want, out)
		}
	}
}

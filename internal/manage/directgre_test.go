package manage

import (
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/tunnel/l3"
)

// greIranSpec is an Iran GRE tunnel as the wizard writes it.
func greIranSpec() l3Spec {
	s := l3Spec{
		Name: "gre-iran", Side: sideIran, Carrier: l3.CarrierGRE, Encap: "gre", GREKey: 41007,
		Addr: "203.0.113.9", Iface: "bp2", LocalIP: "10.10.2.1/30", PeerIP: "10.10.2.2",
		Ports: []string{"443", "8443"}, AcceptUDP: true,
	}
	findL3Preset(PresetTurbo).apply(&s)
	return s
}

// What the wizard writes is a file the engine runs, holds every paired value,
// and holds nothing a GRE link has no use for.
func TestGRERenderIsAFileTheEngineRuns(t *testing.T) {
	spec := greIranSpec()
	body := spec.Render()
	cfg := decode(t, body)

	engine, err := l3.ConfigFromTable(cfg.L3)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Validate(); err != nil {
		t.Fatalf("the engine refuses what the wizard wrote: %v\n%s", err, body)
	}
	l := cfg.L3
	if l.Carrier != "gre" || l.Addr != "203.0.113.9" || l.GREKey != 41007 || l.Mode != "dial" {
		t.Errorf("paired values did not round trip: %+v", l)
	}
	if l.MTU != 0 || l.Token != "" || l.FECData != 0 || l.SockBuf != 0 {
		t.Errorf("a GRE file carries keys a GRE link does not read: mtu %d token %q fec %d sockbuf %d",
			l.MTU, l.Token, l.FECData, l.SockBuf)
	}
	for _, line := range []string{"token", "encap", "fec_data", "sockbuf", "txqueuelen", "qdisc"} {
		if strings.Contains(body, "\n"+line+" ") {
			t.Errorf("the file has a %s line:\n%s", line, body)
		}
	}
	if !strings.Contains(body, "NOT ENCRYPTED") {
		t.Errorf("the file does not say it is unencrypted:\n%s", body)
	}
	// The preset is kept; its queue settings are not written, because the
	// engine leaves a GRE link the kernel's noqueue and would never read them.
	if l.Preset != PresetTurbo || l.TxQueueLen != 0 || l.Qdisc != "" {
		t.Errorf("preset %q txqueuelen %d qdisc %q: want the preset alone", l.Preset, l.TxQueueLen, l.Qdisc)
	}

	// Rendered again from what was read back, as every edit does, it is the
	// same file.
	if again := directSpecFrom(spec.Name, l).Render(); again != body {
		t.Errorf("an edit re-renders a different file:\n--- first\n%s\n--- again\n%s", body, again)
	}
}

func TestGREEditsKeepTheLinkAndRefuseWhatItCannotHave(t *testing.T) {
	l := decode(t, greIranSpec().Render()).L3

	ports, mtu := "443, 2053", 1400
	edited, err := applyDirectEdit(l, DirectEdit{Ports: &ports, MTU: &mtu})
	if err != nil {
		t.Fatal(err)
	}
	if edited.GREKey != l.GREKey || edited.Addr != l.Addr || edited.Carrier != "gre" || edited.MTU != 1400 {
		t.Errorf("an edit of the ports lost the link: %+v", edited)
	}

	pck := "pck"
	if _, err := applyDirectEdit(l, DirectEdit{Carrier: &pck}); err == nil || !strings.Contains(err.Error(), "set up anew") {
		t.Errorf("moving GRE onto pck = %v, want a refusal that says to set it up anew", err)
	}
	on := true
	if _, err := applyDirectEdit(l, DirectEdit{FEC: &on}); err == nil || !strings.Contains(err.Error(), "no error correction") {
		t.Errorf("FEC on a GRE tunnel = %v", err)
	}

	// The screens' one entry, "gre", keeps an ip6gre tunnel on IPv6.
	six := l
	six.Carrier, six.Addr = l3.CarrierIP6GRE, "2001:db8::9"
	gre := "gre"
	kept, err := applyDirectEdit(six, DirectEdit{Carrier: &gre})
	if err != nil || kept.Carrier != l3.CarrierIP6GRE {
		t.Errorf("an ip6gre tunnel sent back \"gre\" became %q (%v)", kept.Carrier, err)
	}

	// And a datagram tunnel is not edited into GRE.
	datagram := config.L3Config{Mode: "dial", Carrier: "pck", Addr: "203.0.113.9:9000", Token: "t"}
	if _, err := applyDirectEdit(datagram, DirectEdit{Carrier: &gre}); err == nil {
		t.Error("a pck tunnel was edited into GRE")
	}
}

func TestGRESettingsForThePanel(t *testing.T) {
	l := decode(t, greIranSpec().Render()).L3
	set := directSettingsFrom("gre-iran", l)
	if !set.Kernel || set.GREKey != 41007 || set.Token != "" {
		t.Errorf("settings: %+v", set)
	}
	if len(set.Carriers) != 1 || set.Carriers[0]["value"] != "gre" {
		t.Errorf("a GRE tunnel's carrier menu offers %v, want GRE alone", set.Carriers)
	}
	if !strings.Contains(set.Encap, "unencrypted") {
		t.Errorf("wrapping label %q does not say it is unencrypted", set.Encap)
	}

	pck := directSettingsFrom("p", config.L3Config{Mode: "dial", Carrier: "pck", Addr: "1.2.3.4:9000", Token: "t"})
	for _, c := range pck.Carriers {
		if l3.KernelCarrier(c["value"]) {
			t.Errorf("a pck tunnel's carrier menu offers %s", c["value"])
		}
	}

	six := l
	six.Carrier, six.Addr = l3.CarrierIP6GRE, "2001:db8::9"
	got := directSettingsFrom("gre-iran", six).Carriers
	if len(got) != 2 || got[1]["value"] != "ip6gre" {
		t.Errorf("an ip6gre tunnel's menu %v does not show what it runs", got)
	}
}

// The Iran side's setup link builds a kharej that names Iran, holds the same
// key and the swapped addresses, and needs no token — over IPv4 and over IPv6.
func TestGRESetupLinkRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, carrier, kharej, iran string
	}{
		{"ipv4", l3.CarrierGRE, "203.0.113.9", "198.51.100.1"},
		{"ipv6", l3.CarrierIP6GRE, "2001:db8::9", "2001:db8::1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := greIranSpec()
			spec.Carrier, spec.Addr = tc.carrier, tc.kharej
			raw := pendingShareLinkFrom(spec, tc.iran, linkExtras{})
			if raw == "" {
				t.Fatal("no link was built for a GRE tunnel")
			}
			link, err := DecodeShareLink(raw)
			if err != nil {
				t.Fatalf("the link does not decode: %v", err)
			}
			if link.Tok != "" || link.Tr != tc.carrier || link.Host != tc.iran || link.GREKey != spec.GREKey {
				t.Fatalf("link: %+v", link)
			}

			form := MirrorForPeer(link)
			if form.Side != "kharej" || form.ServerAddr != tc.iran || form.GREKey != spec.GREKey {
				t.Fatalf("kharej form: %+v", form)
			}
			if form.LocalIP != "10.10.2.2" || form.PeerIP != "10.10.2.1" {
				t.Fatalf("the tunnel addresses did not swap: %s ↔ %s", form.LocalIP, form.PeerIP)
			}
			for _, p := range form.Paired {
				if p == "token" || p == "tunnelPort" {
					t.Errorf("a GRE link marks %q as paired, and has none", p)
				}
			}

			kharej, err := form.ToNewDirectTunnel().spec()
			if err != nil {
				t.Fatalf("the kharej side cannot be built from the link: %v", err)
			}
			if kharej.Side != sideKharej || kharej.Carrier != tc.carrier || kharej.Addr != tc.iran ||
				kharej.GREKey != spec.GREKey || len(kharej.Ports) != 0 {
				t.Fatalf("kharej spec: %+v", kharej)
			}
			back := decode(t, kharej.Render())
			engine, err := l3.ConfigFromTable(back.L3)
			if err == nil {
				err = engine.Validate()
			}
			if err != nil || back.L3.Mode != "listen" {
				t.Fatalf("the kharej file does not run (%v):\n%s", err, kharej.Render())
			}
		})
	}
}

// A setup link made where the address could not be found still builds a
// kharej, once it is given; the form says it is needed rather than guessing.
func TestGREKharejWithoutAnAddressIsRefusedByName(t *testing.T) {
	form := NewDirectTunnel{Side: "kharej", Carrier: "gre", GREKey: 5,
		LocalIP: "10.10.7.2/30", PeerIP: "10.10.7.1"}
	if _, err := form.spec(); err == nil || !strings.Contains(err.Error(), "Iran server's real address") {
		t.Errorf("a kharej with no Iran address = %v", err)
	}
	form.PeerAddr = "198.51.100.1"
	form.GREKey = 0
	if _, err := form.spec(); err == nil || !strings.Contains(err.Error(), "GRE key is required") {
		t.Errorf("a kharej with no key = %v", err)
	}
}

// The panel's Iran form: the address picks the family, the key is picked when
// none is given, and nothing asks for a port or a token.
func TestGREIranFormFromThePanel(t *testing.T) {
	n := NewDirectTunnel{Side: "iran", Carrier: "gre", PeerAddr: "2001:db8::9", Ports: "443"}
	spec, err := n.spec()
	if err != nil {
		t.Fatal(err)
	}
	if spec.Carrier != l3.CarrierIP6GRE || spec.GREKey == 0 || spec.Token != "" || spec.MTU != 0 {
		t.Errorf("spec: %+v", spec)
	}
	n.FEC = true
	if _, err := n.spec(); err == nil {
		t.Error("FEC was accepted on GRE")
	}
	n.FEC, n.GRELocal = false, "198.51.100.1"
	if _, err := n.spec(); err == nil {
		t.Error("an IPv4 gre_local was accepted on an IPv6 tunnel")
	}
}

// Several GRE tunnels on one server, two of them to the same kharej: each gets
// its own interface, its own /30 and its own key, and the second to the same
// kharej with the first one's key is refused before the kernel refuses it.
func TestGREAllocationAcrossSeveralTunnels(t *testing.T) {
	var existing []config.L3Config
	byName := map[string]config.L3Config{}
	// A source of "randomness" that keeps offering the keys already taken
	// first, so the test proves they are passed over.
	offers := []int{0, 0, 1, 1, 2}
	intn := func(int) int { v := offers[0]; offers = offers[1:]; return v }

	kharejs := []string{"203.0.113.9", "203.0.113.9", "198.51.100.7"}
	seen := map[string]bool{}
	for i, kharej := range kharejs {
		iface := freeL3IfaceIn(existing)
		local, peer := freeL3SubnetIn(existing, sideIran)
		key := freeGREKeyIn(existing, intn)
		for what, v := range map[string]string{"iface": iface, "subnet": l3Block(local), "key": fmt.Sprint(key)} {
			if seen[what+v] {
				t.Fatalf("tunnel %d was given a %s already in use: %q", i, what, v)
			}
			seen[what+v] = true
		}
		if other := greJoiningIn(byName, kharej, key); other != "" {
			t.Fatalf("tunnel %d's fresh key %d already joins %s (%s)", i, key, kharej, other)
		}
		l := config.L3Config{Mode: "dial", Carrier: "gre", Addr: kharej, GREKey: key,
			Iface: iface, LocalIP: local, PeerIP: peer}
		existing = append(existing, l)
		byName["gre-iran-"+string(rune('a'+i))] = l
	}
	if existing[0].GREKey == existing[1].GREKey {
		t.Fatal("two tunnels to the same kharej share a key")
	}
	if got := greJoiningIn(byName, "203.0.113.9", existing[1].GREKey); got == "" {
		t.Error("a second link to the same kharej with a key in use was not caught")
	}
	if got := greJoiningIn(byName, "203.0.113.9", 999); got != "" {
		t.Errorf("an unused key read as taken by %s", got)
	}
	// Datagram tunnels share the interface and subnet pools, not the keys.
	existing = append(existing, config.L3Config{Carrier: "xdi", Iface: "bp3", LocalIP: "10.10.3.1/30", GREKey: 1003})
	if iface := freeL3IfaceIn(existing); iface != "bp4" {
		t.Errorf("next interface %s, want bp4", iface)
	}
	if local, _ := freeL3SubnetIn(existing, sideIran); local != "10.10.4.1/30" {
		t.Errorf("next subnet %s, want 10.10.4.1/30", local)
	}
	if key := freeGREKeyIn(existing, func(int) int { return 3 }); key != 1003 {
		t.Errorf("a pck tunnel's encap key blocked GRE key %d", key)
	}
}

func TestGREFamilyFollowsTheAddress(t *testing.T) {
	saved := lookupHost
	defer func() { lookupHost = saved }()
	lookupHost = func(host string) ([]net.IP, error) {
		switch host {
		case "both.example":
			return []net.IP{net.ParseIP("2001:db8::9"), net.ParseIP("203.0.113.9")}, nil
		case "six.example":
			return []net.IP{net.ParseIP("2001:db8::9")}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host}
	}
	for _, tc := range []struct{ host, want, got string }{
		{"203.0.113.9", "", "gre"},
		{"2001:db8::9", "", "ip6gre"},
		{"[2001:db8::9]", "", "ip6gre"},
		{"both.example", "", "gre"},
		{"both.example", "ip6gre", "ip6gre"},
		{"six.example", "", "ip6gre"},
	} {
		got, err := greCarrierForHost(tc.host, tc.want)
		if err != nil || got != tc.got {
			t.Errorf("%s (want %q): %q, %v — expected %q", tc.host, tc.want, got, err, tc.got)
		}
	}
	for _, tc := range []struct{ host, want string }{
		{"203.0.113.9", "ip6gre"}, {"six.example", "gre"}, {"nowhere.example", ""}, {"0.0.0.0", ""},
	} {
		if _, err := greCarrierForHost(tc.host, tc.want); err == nil {
			t.Errorf("%s as %q was accepted", tc.host, tc.want)
		}
	}
}

// The setup link's host: what was asked for when it can be this server's GRE
// end; otherwise what the route gives, never a private address.
func TestGREShareHost(t *testing.T) {
	saved := greLinkHostFor
	defer func() { greLinkHostFor = saved }()
	greLinkHostFor = func(l3Spec) string { return "198.51.100.200" }
	spec := greIranSpec()
	for given, want := range map[string]string{
		"198.51.100.1":   "198.51.100.1",
		"iran.example":   "iran.example",
		"10.0.0.4":       "198.51.100.200",
		"127.0.0.1":      "198.51.100.200",
		"100.64.1.1":     "198.51.100.200",
		"2001:db8::1":    "198.51.100.200",
		"":               "198.51.100.200",
		"[198.51.100.1]": "198.51.100.1",
	} {
		if got := greShareHost(given, spec); got != want {
			t.Errorf("greShareHost(%q) = %q, want %q", given, got, want)
		}
	}
}

// Two servers on one private network see each other's private addresses, so
// the link may name one; a private source towards a public server is NAT, and
// the other end sees some other address. Found running the panel between two
// namespaces on 10.99.0.0/24: the setup link carried no Iran address at all,
// and `link apply` on the kharej refused it.
func TestGRESourceSeenAs(t *testing.T) {
	for _, tc := range []struct {
		local, remote string
		want          bool
	}{
		{"198.51.100.1", "203.0.113.9", true},
		{"10.99.0.1", "10.99.0.2", true},
		{"fd99::1", "fd99::2", true},
		{"10.0.0.4", "203.0.113.9", false},
		{"100.64.1.1", "203.0.113.9", false},
		{"fd99::1", "2001:db8::9", false},
	} {
		if got := greSourceSeenAs(net.ParseIP(tc.local), net.ParseIP(tc.remote)); got != tc.want {
			t.Errorf("greSourceSeenAs(%s, %s) = %v, want %v", tc.local, tc.remote, got, tc.want)
		}
	}
	// A private address given for a private kharej is kept.
	saved := greLinkHostFor
	defer func() { greLinkHostFor = saved }()
	greLinkHostFor = func(l3Spec) string { return "198.51.100.200" }
	spec := greIranSpec()
	spec.Addr = "10.99.0.2"
	if got := greShareHost("10.99.0.1", spec); got != "10.99.0.1" {
		t.Errorf("greShareHost kept %q, want 10.99.0.1", got)
	}
}

// The wizard's Iran side, start to the summary: it says GRE is unencrypted
// first, asks no port and no token, and the summary carries the setup link.
func TestTheGREWizardAsksOnlyWhatGREHas(t *testing.T) {
	saved := greLinkHostFor
	defer func() { greLinkHostFor = saved }()
	greLinkHostFor = func(l3Spec) string { return "198.51.100.1" }

	answers := strings.Join([]string{
		"203.0.113.9", // Kharej IP Or Domain
		"443",         // Forwarded Ports
		"",            // Tunnel Name: the default
		"",            // Carry UDP: no
		"1",           // Tuning: Turbo
		"",            // Fine-Tune: no
		"",            // Scheduled restart: off
		"n",           // Create This Tunnel: no
	}, "\n") + "\n"
	restore := tui.SetInput(strings.NewReader(answers))
	out := capture(t, func() { setupGRE(sideIran) })
	restore()

	for _, want := range []string{"Not Encrypted", "backpack://", "GRE Key", "none — kernel GRE"} {
		if !strings.Contains(out, want) {
			t.Errorf("the wizard did not show %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"Tunnel Port", "Security Token", "Error Correction"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the wizard asked %q, which GRE does not have:\n%s", unwanted, out)
		}
	}
}

// The panel's setup link for a GRE tunnel whose own address could not be
// found: the link has no Iran address, `link apply` on the kharej refuses one
// without --host, so the screen's command carries --host and says so. Before,
// it showed a plain `link apply` that could only fail, with no warning.
func TestAGRESetupLinkWithNoAddressSaysWhatToAdd(t *testing.T) {
	spec := greIranSpec()
	var cfg config.Config
	if _, err := toml.Decode(spec.Render(), &cfg); err != nil {
		t.Fatal(err)
	}
	prevFor, prevHost, prevGRE := shareLinkFor, linkHost, greLinkHostFor
	t.Cleanup(func() { shareLinkFor, linkHost, greLinkHostFor = prevFor, prevHost, prevGRE })
	shareLinkFor = func(name, host string) (string, error) { return shareLinkOf(name, host, cfg) }
	linkHost = func() string { return "" }
	greLinkHostFor = func(l3Spec) string { return "" }

	info, err := SetupLinkFor(spec.Name, "")
	if err != nil {
		t.Fatal(err)
	}
	if !info.GRE {
		t.Error("the link is not marked as GRE, so the screen would talk of a token")
	}
	if !info.NeedsAddress || info.Host != "" {
		t.Errorf("needsAddress %v host %q: the panel would not warn", info.NeedsAddress, info.Host)
	}
	if !strings.Contains(info.Apply, "--host "+greHostPlaceholder+" ") {
		t.Errorf("the kharej's command cannot work without --host: %s", info.Apply)
	}

	// With an address found, the link carries it and the command is plain.
	greLinkHostFor = func(l3Spec) string { return "198.51.100.1" }
	info, err = SetupLinkFor(spec.Name, "")
	if err != nil {
		t.Fatal(err)
	}
	if info.NeedsAddress || info.Host != "198.51.100.1" || strings.Contains(info.Apply, "--host") {
		t.Errorf("with an address: %+v", info)
	}
}

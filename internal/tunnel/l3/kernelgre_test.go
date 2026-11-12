package l3

import (
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

// greConfig is a valid Iran-side kernel GRE config to vary.
func greConfig() Config {
	return Config{
		Mode: ModeDial, Addr: "203.0.113.9", Carrier: CarrierGRE, GREKey: 7,
		LocalIP: "10.10.3.1/30", PeerIP: "10.10.3.2",
	}
}

// A GRE link has no port, no secret and no session: the file names the other
// server alone, needs no token, and keeps mtu 0 for the engine to work out.
func TestKernelGREValidates(t *testing.T) {
	cfg := greConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a plain GRE config was refused: %v", err)
	}
	if cfg.MTU != 0 {
		t.Errorf("mtu = %d, want 0 left for the engine to derive", cfg.MTU)
	}
	if cfg.Iface != defaultIfaceName {
		t.Errorf("iface = %q", cfg.Iface)
	}

	for _, mode := range []string{ModeDial, ModeListen} {
		six := Config{Mode: mode, Addr: "[2001:db8::9]", Carrier: " IP6GRE ", LocalIP: "10.10.4.2/30", PeerIP: "10.10.4.1"}
		if err := six.Validate(); err != nil {
			t.Fatalf("ip6gre (%s) was refused: %v", mode, err)
		}
		if six.Addr != "2001:db8::9" || six.Carrier != CarrierIP6GRE {
			t.Errorf("not normalised: addr %q carrier %q", six.Addr, six.Carrier)
		}
	}
}

func TestKernelGRERefusesWhatCannotWork(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"a port", func(c *Config) { c.Addr = "203.0.113.9:9000" }, "has no port"},
		{"no address", func(c *Config) { c.Addr = "" }, "other server's address"},
		{"the wildcard", func(c *Config) { c.Addr = "0.0.0.0" }, "cannot be a GRE endpoint"},
		{"IPv6 on gre", func(c *Config) { c.Addr = "2001:db8::9" }, `use carrier = "ip6gre"`},
		{"IPv4 on ip6gre", func(c *Config) { c.Carrier = CarrierIP6GRE }, `use carrier = "gre"`},
		{"a local of the wrong family", func(c *Config) { c.GRELocal = "2001:db8::1" }, "gre_local"},
		{"a local that is not an address", func(c *Config) { c.GRELocal = "eth0" }, "gre_local"},
		{"error correction", func(c *Config) { c.FEC = FECConfig{Data: 10, Parity: 3} }, "error correction"},
		{"an mtu out of range", func(c *Config) { c.MTU = 100 }, "workable range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := greConfig()
			tc.edit(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A domain is accepted as written and resolved at start, of the carrier's own
// family; a token, if a file has one, is simply not needed.
func TestKernelGREAcceptsADomainAndIgnoresAToken(t *testing.T) {
	cfg := greConfig()
	cfg.Addr, cfg.Token = "kharej.example.com", "left-over"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg = greConfig()
	cfg.Token = ""
	cfg.Mode = ModeListen
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a listening GRE end without a token: %v", err)
	}
}

func TestGRELinkArguments(t *testing.T) {
	v4 := greLinkSpec{Name: "bp2", Carrier: CarrierGRE,
		Local: net.ParseIP("198.51.100.1"), Remote: net.ParseIP("203.0.113.9"), Key: 41}
	want := "link add bp2 type gre local 198.51.100.1 remote 203.0.113.9 ttl 255 key 41"
	if got := strings.Join(greLinkAddArgs(v4), " "); got != want {
		t.Errorf("gre:\n got %s\nwant %s", got, want)
	}
	v4.Key = 0
	if got := strings.Join(greLinkAddArgs(v4), " "); strings.Contains(got, "key") {
		t.Errorf("a keyless link names a key: %s", got)
	}

	v6 := greLinkSpec{Name: "bp3", Carrier: CarrierIP6GRE,
		Local: net.ParseIP("2001:db8::1"), Remote: net.ParseIP("2001:db8::2"), Key: 4294967295}
	want = "link add bp3 type ip6gre local 2001:db8::1 remote 2001:db8::2 hoplimit 255 encaplimit none key 4294967295"
	if got := strings.Join(greLinkAddArgs(v6), " "); got != want {
		t.Errorf("ip6gre:\n got %s\nwant %s", got, want)
	}
	if got := strings.Join(greLinkDelArgs("bp3"), " "); got != "link del dev bp3" {
		t.Errorf("delete = %s", got)
	}
	rule := strings.Join(greAcceptRule("bp3", net.ParseIP("203.0.113.9")), " ")
	if rule != "INPUT -p 47 -s 203.0.113.9 -m comment --comment backpack-gre bp3 -j ACCEPT" {
		t.Errorf("accept rule = %s", rule)
	}
}

// What the kernel adds, and the MTU that leaves on a 1500-byte interface —
// the same figures `ip link add` arrives at for itself.
func TestGREOverheadAndMTU(t *testing.T) {
	for _, tc := range []struct {
		carrier string
		key     uint32
		over    int
	}{
		{CarrierGRE, 0, 24}, {CarrierGRE, 9, 28}, {CarrierIP6GRE, 0, 44}, {CarrierIP6GRE, 9, 48},
	} {
		if got := GREOverhead(tc.carrier, tc.key); got != tc.over {
			t.Errorf("%s key %d: overhead %d, want %d", tc.carrier, tc.key, got, tc.over)
		}
		if got := greMTUFor(1500, tc.carrier, tc.key); got != 1500-tc.over {
			t.Errorf("%s key %d: mtu %d, want %d", tc.carrier, tc.key, got, 1500-tc.over)
		}
	}
	if got := greMTUFor(0, CarrierGRE, 1); got != 1472 {
		t.Errorf("an unknown egress is read as 1500: got %d", got)
	}
	if got := greMTUFor(9000, CarrierGRE, 1); got != 8972 {
		t.Errorf("jumbo egress: %d", got)
	}
	if GRECarrierFor(net.ParseIP("2001:db8::1")) != CarrierIP6GRE || GRECarrierFor(net.ParseIP("1.2.3.4")) != CarrierGRE {
		t.Error("GRECarrierFor picks the wrong family")
	}
}

func TestGREAddFailuresSayWhatToDo(t *testing.T) {
	s := greLinkSpec{Name: "bp1", Carrier: CarrierIP6GRE,
		Local: net.ParseIP("2001:db8::1"), Remote: net.ParseIP("2001:db8::2"), Key: 5}
	failed := errors.New("exit status 2")
	if err := explainGREAddFailure(s, "Error: Unknown device type.\n", failed); !strings.Contains(err.Error(), "modprobe ip6_gre") {
		t.Errorf("missing module: %v", err)
	}
	s.Carrier = CarrierGRE
	if err := explainGREAddFailure(s, "RTNETLINK answers: Operation not supported", failed); !strings.Contains(err.Error(), "modprobe ip_gre") {
		t.Errorf("old iproute2 wording: %v", err)
	}
	if err := explainGREAddFailure(s, "RTNETLINK answers: File exists", failed); !strings.Contains(err.Error(), "key 5") ||
		!strings.Contains(err.Error(), "key of its own") {
		t.Errorf("a clashing pair: %v", err)
	}
	if err := explainGREAddFailure(s, "RTNETLINK answers: Operation not permitted", failed); !strings.Contains(err.Error(), "CAP_NET_ADMIN") {
		t.Errorf("no privilege: %v", err)
	}
}

// Both kinds of link are configured by the same commands.
func TestInterfaceSteps(t *testing.T) {
	steps, err := interfaceSteps("bp1", "10.10.1.1/30", "10.10.1.2", 1472, 4096)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range steps {
		got = append(got, strings.Join(s, " "))
	}
	want := []string{
		"addr add 10.10.1.1/30 dev bp1",
		"link set dev bp1 mtu 1472",
		"link set dev bp1 txqueuelen 4096",
		"link set dev bp1 up",
	}
	if !slices.Equal(got, want) {
		t.Errorf("steps:\n got %q\nwant %q", got, want)
	}
	steps, _ = interfaceSteps("bp1", "10.10.1.1/32", "10.10.1.2", 1400, 1)
	if last := strings.Join(steps[len(steps)-1], " "); last != "route replace 10.10.1.2 dev bp1" {
		t.Errorf("a /32 needs its peer route, last step %q", last)
	}
	steps, _ = interfaceSteps("bp1", "10.10.1.1", "10.10.1.2", 1400, 1)
	if first := strings.Join(steps[0], " "); first != "addr add 10.10.1.1 peer 10.10.1.2 dev bp1" {
		t.Errorf("point-to-point form %q", first)
	}
	if _, err := interfaceSteps("bp1", "10.10.1.1", "", 1400, 1); err == nil {
		t.Error("a bare local address with no peer was accepted")
	}
}

func TestGRESearchMTU(t *testing.T) {
	carries := func(limit int) func(int) bool { return func(n int) bool { return n <= limit } }
	if got, ok := greSearchMTU(576, 1472, carries(1500)); !ok || got != 1472 {
		t.Errorf("clean path: %d %v", got, ok)
	}
	if got, ok := greSearchMTU(576, 1472, carries(1371)); !ok || got != 1371 {
		t.Errorf("narrow path: %d %v", got, ok)
	}
	if _, ok := greSearchMTU(576, 1472, carries(0)); ok {
		t.Error("nothing crossing was reported as a measurement")
	}
}

// A path that goes dark in the middle of a search fails every probe from then
// on. Read as sizes that do not fit, that would walk the search down to the
// floor and lower the link for good; it has to come back as no measurement.
func TestGRESearchMTUIgnoresAnOutageMidSearch(t *testing.T) {
	// The ceiling is lost as the outage begins, the floor just gets through,
	// and nothing crosses after that.
	probes := 0
	dark := func(int) bool {
		probes++
		return probes == 2
	}
	if got, ok := greSearchMTU(576, 1472, dark); ok {
		t.Errorf("an outage mid-search was measured as %d", got)
	}

	// The same outage, over by the time the search ends: the interface's size
	// crosses again, so the path never shrank.
	probes = 0
	back := false
	recovers := func(n int) bool {
		probes++
		switch {
		case probes == 1:
			return false
		case probes == 2:
			return true
		case n == 576: // the search itself never sends the floor again
			back = true
		}
		return back
	}
	if got, ok := greSearchMTU(576, 1472, recovers); !ok || got != 1472 {
		t.Errorf("a path back to full size by the end: %d %v, want 1472", got, ok)
	}
}

// Up is the other end having answered recently, and the counters of a link
// that went away are kept in the totals of the next.
func TestGREStateLivenessAndCounters(t *testing.T) {
	var g greState
	now := time.Now()
	if g.up(now) {
		t.Fatal("up before anything answered")
	}
	g.lastAnswer.Store(now.Add(-time.Second).UnixNano())
	if !g.up(now) {
		t.Fatal("not up a second after an answer")
	}
	if g.up(now.Add(greSilentAfter)) {
		t.Fatal("still up after the silence limit")
	}

	name := "bp9"
	g.iface.Store(&name)
	read := func(string) (greCounters, bool) {
		return greCounters{bytesIn: 100, bytesOut: 50, packetsIn: 2, packetsOut: 1, dropped: 1}, true
	}
	if s := g.stats(read); s.BytesIn != 100 || s.BytesOut != 50 {
		t.Fatalf("first link: %+v", s)
	}
	g.retire(greCounters{bytesIn: 100, bytesOut: 50, packetsIn: 2, packetsOut: 1, dropped: 1})
	if s := g.stats(read); s.BytesIn != 100 {
		t.Fatalf("with no link open the totals are what closed: %+v", s)
	}
	g.iface.Store(&name)
	if s := g.stats(read); s.BytesIn != 200 || s.PacketsOut != 2 || s.Dropped != 2 {
		t.Fatalf("second link adds to the first: %+v", s)
	}
}

// A kernel GRE tunnel answers Up from its liveness, with no session to have.
func TestKernelGRETunnelUpFollowsTheOtherEnd(t *testing.T) {
	tun, err := New(greConfig(), quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if tun.Up() {
		t.Fatal("up before the other end answered")
	}
	tun.gre.lastAnswer.Store(time.Now().UnixNano())
	if !tun.Up() {
		t.Fatal("not up after the other end answered")
	}
}

// The way out of a removed carrier is an edit of the carrier alone, so the
// refusal lists only what an edit can move a tunnel onto. GRE is a carrier, and
// is listed where every carrier is, but not there.
func TestRemovedCarrierErrorListsOnlyWhatAnEditCanReach(t *testing.T) {
	msg := RemovedCarrierError("udp").Error()
	for _, c := range DatagramCarriers() {
		if !strings.Contains(msg, c) {
			t.Errorf("the refusal does not offer %q: %s", c, msg)
		}
	}
	if strings.Contains(msg, "gre,") || strings.Contains(msg, "ip6gre") {
		t.Errorf("the refusal offers a GRE link an edit cannot make: %s", msg)
	}
	for _, c := range []string{CarrierGRE, CarrierIP6GRE} {
		if !slices.Contains(Carriers(), c) || !KnownCarrier(c) || !KernelCarrier(c) {
			t.Errorf("%s is not a known kernel carrier", c)
		}
	}
	if KernelCarrier(CarrierPck) {
		t.Error("pck read as a kernel carrier")
	}
}

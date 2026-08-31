package manage

import (
	"strings"
	"testing"

	"github.com/backpack/backpack/config"
)

// The panel's spoof drawer and the CLI's askSpoofCarrier have to be able to
// describe the same direct tunnel. These tests pin the parts of that where being merely close
// would produce a tunnel that comes up and carries nothing.

// One forged address is a single source; several are a pool the carrier rotates
// through. Getting this wrong either way is silent: a one-entry pool looks like
// rotation that never rotates, and a pool collapsed to one address looks like a
// block being evaded when it is not.
func TestForgedSourcesBecomeAnAddressOrAPool(t *testing.T) {
	single := config.SpoofConfig{}
	if err := (SpoofTune{Profile: "udp", SrcIPs: " 203.0.113.10 "}).apply(&single); err != nil {
		t.Fatalf("one address was refused: %v", err)
	}
	if single.SpoofSrcIP != "203.0.113.10" || single.SpoofSrcPool != nil {
		t.Errorf("one address should not make a pool: %q %v", single.SpoofSrcIP, single.SpoofSrcPool)
	}

	pool := config.SpoofConfig{}
	if err := (SpoofTune{Profile: "udp", SrcIPs: "203.0.113.10, 198.51.100.7"}).apply(&pool); err != nil {
		t.Fatalf("a pool was refused: %v", err)
	}
	if len(pool.SpoofSrcPool) != 2 || pool.SpoofSrcIP != "203.0.113.10" {
		t.Errorf("pool not kept: %q %v", pool.SpoofSrcIP, pool.SpoofSrcPool)
	}
}

// The carrier writes raw IPv4 headers, so anything that is not an IPv4 address
// is refused rather than dropped with a warning: a form that silently discarded
// the field would leave the operator looking at a tunnel configured differently
// from the page in front of them.
func TestBadForgedSourceIsRefused(t *testing.T) {
	s := config.SpoofConfig{}
	err := (SpoofTune{Profile: "udp", SrcIPs: "203.0.113.10, not-an-ip"}).apply(&s)
	if err == nil {
		t.Fatal("a junk address was accepted")
	}
	if !strings.Contains(err.Error(), "not-an-ip") {
		t.Errorf("the error does not say which address was wrong: %v", err)
	}
}

// Where the peer's real address is required is the direct tunnel's own
// validation now (l3.Config.validate refuses a listening side without it) and
// not this drawer's, because the drawer no longer knows which side it is on.
// What it still owes is to accept a valid one and refuse a junk one.
func TestThePeersRealAddressIsCheckedButNotRequiredHere(t *testing.T) {
	var sc config.SpoofConfig
	if err := (SpoofTune{Profile: "udp"}).apply(&sc); err != nil {
		t.Fatalf("an unset peer address was refused: %v", err)
	}
	if err := (SpoofTune{Profile: "udp", PeerIP: "203.0.113.10"}).apply(&sc); err != nil {
		t.Fatalf("a valid peer address was refused: %v", err)
	}
	if sc.SpoofPeerIP != "203.0.113.10" {
		t.Errorf("peer address = %q, want 203.0.113.10", sc.SpoofPeerIP)
	}
	if err := (SpoofTune{Profile: "udp", PeerIP: "nonsense"}).apply(&sc); err == nil {
		t.Error("a junk peer address was accepted")
	}
}

// A profile the engine does not know would be written into the config and read
// back as nothing at all, so it is caught here.
func TestSpoofProfileIsOneOfTheThree(t *testing.T) {
	s := config.SpoofConfig{}
	if err := (SpoofTune{Profile: "quic"}).apply(&s); err == nil {
		t.Error("an unknown packet profile was accepted")
	}
	// Empty means the wizard's recommendation, not "no profile".
	s = config.SpoofConfig{}
	if err := (SpoofTune{}).apply(&s); err != nil || s.SpoofProfile != "udp" {
		t.Errorf("an unanswered profile should default to udp, got %q (%v)", s.SpoofProfile, err)
	}
}

// The fake TLS header is prepended to a TCP-profile packet. On the other two
// there is no record to fake, and writing the setting out anyway would leave a
// knob in the config that nothing reads.
func TestFakeTLSOnlySurvivesOnTheTCPProfile(t *testing.T) {
	udp := config.SpoofConfig{}
	if err := (SpoofTune{Profile: "udp", FakeTLS: true}).apply(&udp); err != nil {
		t.Fatal(err)
	}
	if udp.SpoofFakeTLS {
		t.Error("fake TLS was kept on a UDP-profile tunnel")
	}
	tcp := config.SpoofConfig{}
	if err := (SpoofTune{Profile: "tcp", FakeTLS: true}).apply(&tcp); err != nil {
		t.Fatal(err)
	}
	if !tcp.SpoofFakeTLS {
		t.Error("fake TLS was dropped on a TCP-profile tunnel")
	}
}

// Everything the drawer shows has to come back out of the tunnel it wrote, or
// the Edit form opens on a tunnel other than the one that is running.
func TestSpoofDrawerRoundTripsThroughTheSpec(t *testing.T) {
	in := SpoofTune{
		Profile: "icmp", Uplink: "udp", Downlink: "tcp",
		SrcIPs: "203.0.113.10, 198.51.100.7", PeerIP: "192.0.2.5",
		PeerSrcIP: "198.51.100.9", DstIP: "192.0.2.9",
		SockBuf: 4194304, MTU: 1200,
		ICMPReply: true, TTLJitter: true, RandomDSCP: true,
		ShufflePort: true, PortMin: 20000, PortMax: 40000,
		Padding: true, PaddingMax: 128,
	}
	s := config.SpoofConfig{}
	if err := in.apply(&s); err != nil {
		t.Fatalf("the drawer was refused: %v", err)
	}
	out := spoofOf(s)
	if out.Profile != in.Profile || out.Uplink != in.Uplink || out.Downlink != in.Downlink {
		t.Errorf("profiles did not survive: %+v", out)
	}
	if out.SrcIPs != "203.0.113.10, 198.51.100.7" || out.PeerIP != in.PeerIP || out.PeerSrcIP != in.PeerSrcIP {
		t.Errorf("addresses did not survive: %+v", out)
	}
	if out.MTU != in.MTU || out.SockBuf != in.SockBuf {
		t.Errorf("sizing did not survive: %+v", out)
	}
	if !out.TTLJitter || !out.RandomDSCP || !out.ShufflePort || out.PortMin != 20000 ||
		out.PortMax != 40000 || !out.Padding || out.PaddingMax != 128 || !out.ICMPReply {
		t.Errorf("the evasion knobs did not survive: %+v", out)
	}
}

// A source-port range that starts above where it ends would be written out and
// then produce ports from nowhere, so it is caught at the form.
func TestBackwardsPortRangeIsRefused(t *testing.T) {
	s := config.SpoofConfig{}
	err := (SpoofTune{Profile: "udp", ShufflePort: true, PortMin: 40000, PortMax: 20000}).apply(&s)
	if err == nil {
		t.Fatal("a backwards port range was accepted")
	}
}

// A backup address without a port reuses the tunnel port, which is what makes
// "a second IP of the same server" a one-word answer.
func TestBackupAddressInheritsTheTunnelPort(t *testing.T) {
	s := TunnelSpec{Role: "client", Transport: "tcp"}
	c := ConnTune{FallbackAddrs: "203.0.113.9, 198.51.100.4:5555", LoadBalance: true}
	if err := c.apply(&s, "4443"); err != nil {
		t.Fatalf("backup addresses were refused: %v", err)
	}
	if len(s.FallbackAddrs) != 2 || s.FallbackAddrs[0] != "203.0.113.9:4443" {
		t.Errorf("the bare address did not take the tunnel port: %v", s.FallbackAddrs)
	}
	if s.FallbackAddrs[1] != "198.51.100.4:5555" {
		t.Errorf("an address with its own port was rewritten: %v", s.FallbackAddrs)
	}
	if !s.LoadBalance {
		t.Error("load balancing was dropped even though there are addresses to spread over")
	}
}

// Load balancing spreads connections over every address at once. With no backup
// addresses there is nothing to spread them over, and leaving the flag on would
// describe a tunnel doing something it is not.
func TestLoadBalancingNeedsSomewhereToBalance(t *testing.T) {
	s := TunnelSpec{Role: "client", Transport: "tcp"}
	if err := (ConnTune{LoadBalance: true}).apply(&s, "4443"); err != nil {
		t.Fatal(err)
	}
	if s.LoadBalance {
		t.Error("load balancing stayed on with no addresses to balance over")
	}
}

// A settings block that only applies to some transports is refused on the
// others rather than written out and ignored — a setting that looks live and is
// not is the hardest kind to debug.
func TestConnectionOptionsAreRefusedWhereTheyCannotWork(t *testing.T) {
	kcp := TunnelSpec{Role: "client", Transport: "kcp"}
	if err := (ConnTune{Proxy: "socks5://127.0.0.1:1080"}).apply(&kcp, "4443"); err == nil {
		t.Error("a TCP proxy was accepted for a datagram transport")
	}
	tcp := TunnelSpec{Role: "client", Transport: "tcp"}
	if err := (ConnTune{EdgeIP: "104.16.0.1"}).apply(&tcp, "4443"); err == nil {
		t.Error("a CDN edge IP was accepted for a non-websocket transport")
	}
	// Simple auth only means anything where there is TLS to terminate.
	if err := (ConnTune{SimpleAuth: true}).apply(&tcp, "4443"); err != nil {
		t.Fatal(err)
	}
	if tcp.SimpleAuth {
		t.Error("simple auth was kept on a transport with no TLS binding")
	}
}

// Switching a tunnel off pck has to take that carrier's settings with it, or
// the Edit form keeps showing values nothing reads.
func TestSwitchingCarrierDropsTheOldOnesSettings(t *testing.T) {
	s := TunnelSpec{
		Role: "client", Transport: "tcp",
		PckInterface: "eth0", PckFlags: []string{"PA"},
		EdgeIP: "104.16.0.1", SimpleAuth: true,
	}
	clearForTransport(&s)
	if s.PckInterface != "" || s.PckFlags != nil {
		t.Errorf("packet-carrier settings survived a move to tcp: %+v", s)
	}
	if s.EdgeIP != "" || s.SimpleAuth {
		t.Errorf("websocket-only settings survived a move to tcp: %+v", s)
	}
}

// The limits are the CLI's Limits screen: 0 is no limit, and a negative number
// is not an answer.
func TestLimitsRoundTripAndRefuseNegatives(t *testing.T) {
	s := TunnelSpec{Role: "server", Transport: "tcp"}
	if err := (TunnelLimits{MaxConnections: 500, BandwidthMbps: 100}).apply(&s); err != nil {
		t.Fatal(err)
	}
	if got := limitsOf(s); got.MaxConnections != 500 || got.BandwidthMbps != 100 {
		t.Errorf("limits did not survive: %+v", got)
	}
	if err := (TunnelLimits{MaxConnections: -1}).apply(&s); err == nil {
		t.Error("a negative limit was accepted")
	}
}

// The packet carrier's flag cycle is normalised the way the CLI's own screen
// normalises it, and a MAC that is not one is refused.
func TestPacketCarrierSettings(t *testing.T) {
	s := TunnelSpec{Role: "client", Transport: "pck"}
	if err := (PckTune{Flags: " pa , a "}).apply(&s); err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.PckFlags, ",") != "PA,A" {
		t.Errorf("flag cycle not normalised: %v", s.PckFlags)
	}
	if err := (PckTune{GatewayMAC: "not a mac"}).apply(&s); err == nil {
		t.Error("a junk gateway MAC was accepted")
	}
}

// The panel's spoof profile menu and the CLI's are one list, for the same
// reason the transport menu is.
func TestSpoofProfileMenuMatchesTheEngine(t *testing.T) {
	got := SpoofProfiles()
	if len(got) != len(spoofProfiles) {
		t.Fatalf("the panel offers %d profiles, the carrier has %d", len(got), len(spoofProfiles))
	}
	for i, p := range got {
		if p.Value != spoofProfiles[i] {
			t.Errorf("profile %d: panel offers %q, the carrier knows %q", i, p.Value, spoofProfiles[i])
		}
	}
}

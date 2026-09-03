package manage

import (
	"strings"
	"testing"
)

// The link exists to remove the one failure this system is worst at: two ends
// that disagree about a paired setting, which produces a tunnel that reports
// itself connected and carries nothing. So what these hold down is that the
// settings survive the trip intact, and that a link which did NOT survive says
// so instead of half-filling a form.

func sampleLink() ShareLink {
	return ShareLink{
		Kind: "direct", From: "iran", Name: "iran-spoof",
		Tok: "a-real-looking-token-0123456789abcdef", Tr: "spoof", Encap: "gre",
		Port: "9000", Host: "203.0.113.9",
		Preset: "turbo", Ports: "443, 8080=80", AcceptUDP: true, MSS: 1208, MTU: 1400,
		LocalIP: "10.20.0.1/30", PeerIP: "10.20.0.2",
		FECData: 10, FECParity: 3, Paths: 4,
		Profile: "icmp", Uplink: "icmp", Downlink: "udp",
		SrcIPs: "8.8.4.4, 1.1.1.1", Stealth: true, ICMPReply: true,
	}
}

// Everything paired must come back exactly. A field that silently does not
// survive is a setting the other end quietly keeps its own value for, which is
// the failure this whole thing exists to prevent.
func TestShareLinkRoundTripsEverySetting(t *testing.T) {
	want := sampleLink()
	s, err := want.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeShareLink(s)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want.V = 1 // set by Encode
	if got != want {
		t.Errorf("the link did not survive the trip:\n got %+v\nwant %+v", got, want)
	}
}

// It has to look like what it is, and be short enough that somebody actually
// pastes it rather than truncating it.
func TestShareLinkIsRecognisableAndCompact(t *testing.T) {
	s, err := sampleLink().Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, "backpack://1.") {
		t.Errorf("the link does not announce its scheme and version: %q", s[:min(20, len(s))])
	}
	// A fully loaded spoof tunnel is the biggest this gets. If it grows past a
	// few hundred characters it stops being something people paste whole.
	if len(s) > 600 {
		t.Errorf("the link is %d characters — too long to paste reliably", len(s))
	}
	t.Logf("a fully loaded link is %d characters", len(s))
}

// A copy that stopped short is the common accident — chat clients wrap, people
// select by eye. It must fail, and say that is what happened, rather than
// decoding into a partial tunnel.
func TestATruncatedLinkIsRefused(t *testing.T) {
	s, _ := sampleLink().Encode()
	for _, cut := range []int{len(s) - 1, len(s) - 5, len(s) / 2, len("backpack://1.") + 4} {
		if _, err := DecodeShareLink(s[:cut]); err == nil {
			t.Errorf("a link cut to %d characters was accepted", cut)
		}
	}
}

// And the other ways a paste goes wrong, each with its own answer.
func TestBadLinksSayWhatIsWrong(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"empty", "", "paste the setup link"},
		{"not a link", "hello there", "does not look like"},
		{"no version separator", "backpack://abcdef", "incomplete"},
		{"a version this build does not know", "backpack://9.abcdef", "version 9"},
		{"damaged payload", "backpack://1.!!!!not-base64!!!!", "damaged"},
	} {
		_, err := DecodeShareLink(tc.in)
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
}

// A link is pasted into the OTHER side's form. Getting that backwards would
// build two tunnels of the same side, which cannot connect to each other.
func TestALinkNamesTheSideItIsMeantFor(t *testing.T) {
	if got := (ShareLink{From: "iran"}).PeerSide(); got != "kharej" {
		t.Errorf("a link from Iran is for %q, want kharej", got)
	}
	if got := (ShareLink{From: "kharej"}).PeerSide(); got != "iran" {
		t.Errorf("a link from kharej is for %q, want iran", got)
	}
}

// A link that is well-formed but says nothing useful is refused rather than
// half-applied: without a token or a transport there is no tunnel to describe.
func TestALinkWithoutTheEssentialsIsRefused(t *testing.T) {
	for _, l := range []ShareLink{
		{Kind: "direct", From: "iran", Tr: "udp"},             // no token
		{Kind: "direct", From: "iran", Tok: "x"},              // no transport
		{From: "iran", Tok: "x", Tr: "udp"},                   // no kind
		{Kind: "sideways", From: "iran", Tok: "x", Tr: "udp"}, // a kind that does not exist
	} {
		s, err := l.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeShareLink(s); err == nil {
			t.Errorf("a link missing its essentials was accepted: %+v", l)
		}
	}
}

// The inversion is the part that is easy to get subtly wrong, and every one of
// these is a mistake that would produce a tunnel which connects and carries
// nothing rather than one that fails loudly.

// A direct tunnel set up on Iran hands kharej a form for the listening side,
// with the two tunnel addresses the other way round.
func TestMirroringADirectLinkSwapsTheSidesAndTheAddresses(t *testing.T) {
	f := MirrorForPeer(sampleLink())

	if f.Side != "kharej" || f.Kind != "direct" {
		t.Fatalf("side=%q kind=%q, want kharej/direct", f.Side, f.Kind)
	}
	// The producer's local address is the receiver's peer, and the prefix is
	// dropped because it describes the producer's end of the /30.
	if f.LocalIP != "10.20.0.2" || f.PeerIP != "10.20.0.1" {
		t.Errorf("tunnel addresses did not swap: local=%q peer=%q", f.LocalIP, f.PeerIP)
	}
	// Iran dials on a direct tunnel, so the kharej side is not given an address
	// to reach — it listens.
	if f.ServerAddr != "" {
		t.Errorf("the listening side was given an address to dial: %q", f.ServerAddr)
	}
	if f.Token != "a-real-looking-token-0123456789abcdef" || f.TunnelPort != "9000" {
		t.Errorf("the essentials did not carry: token=%q port=%q", f.Token, f.TunnelPort)
	}
	if f.Carrier != "spoof" || f.Encap != "gre" {
		t.Errorf("the carrier did not carry: %q/%q", f.Carrier, f.Encap)
	}
}

// A reverse tunnel set up on Iran hands kharej the address to dial and does not
// hand it the forwarded ports, which are Iran's alone.
func TestMirroringAReverseLinkGivesKharejTheAddressAndNoPorts(t *testing.T) {
	l := sampleLink()
	l.Kind, l.Tr, l.Encap = "reverse", "tcp", ""
	l.Profile, l.Uplink, l.Downlink, l.SrcIPs = "", "", "", ""

	f := MirrorForPeer(l)
	if f.Transport != "tcp" {
		t.Errorf("transport = %q, want tcp", f.Transport)
	}
	if f.ServerAddr != "203.0.113.9" {
		t.Errorf("the kharej side was not told where to dial: %q", f.ServerAddr)
	}
	if f.Ports != "" {
		t.Errorf("the dialling side was given forwarded ports: %q", f.Ports)
	}
}

// The subtle one: the producer's forged source is not copied across — it
// becomes what this side EXPECTS from it. Copying it would have both ends
// forging the same address, which is not what the pinning means.
func TestTheProducersForgedSourceBecomesTheExpectedOne(t *testing.T) {
	l := sampleLink()
	l.SrcIPs = "8.8.4.4"

	f := MirrorForPeer(l)
	if f.Spoof == nil {
		t.Fatal("the carrier settings did not carry at all")
	}
	if f.Spoof.PeerSrcIP != "8.8.4.4" {
		t.Errorf("expected source = %q, want the producer's 8.8.4.4", f.Spoof.PeerSrcIP)
	}
	if f.Spoof.SrcIPs != "" {
		t.Errorf("the producer's forged source was copied as this side's own: %q", f.Spoof.SrcIPs)
	}
	// And the listening side is told where its peer really is, which it cannot
	// learn from packets whose source is forged.
	if f.SpoofPeerIP != "203.0.113.9" {
		t.Errorf("the peer's real address was not carried: %q", f.SpoofPeerIP)
	}
}

// A producer rotating a pool has no single source to pin. Pinning one of
// several would drop every packet sent from the others, so it pins none and
// says why.
func TestAPoolOfForgedSourcesPinsNothingAndSaysSo(t *testing.T) {
	f := MirrorForPeer(sampleLink()) // the sample rotates two
	if f.Spoof.PeerSrcIP != "" {
		t.Errorf("a pool was pinned to %q, which would drop the rest", f.Spoof.PeerSrcIP)
	}
	if f.Note == "" {
		t.Error("nothing was said about why the expected source was left empty")
	}
}

// The list of paired fields is what the panel warns on. It must name the
// settings that actually break a tunnel when they differ — and must not name
// the ones that are local, or the panel warns about things that do not matter.
func TestThePairedListNamesWhatMustMatch(t *testing.T) {
	f := MirrorForPeer(sampleLink())
	has := func(s string) bool {
		for _, p := range f.Paired {
			if p == s {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"token", "tunnelPort", "carrier", "localIp", "peerIp",
		"paths", "fec", "stealth", "spoof.profile", "spoofPeerIp"} {
		if !has(want) {
			t.Errorf("%q must match the other end and is not marked as paired", want)
		}
	}
	// The name is a suggestion and the forged source is this side's own choice:
	// warning about either would be warning about nothing.
	for _, notWanted := range []string{"name", "spoof.srcIPs"} {
		if has(notWanted) {
			t.Errorf("%q is local to this side and must not be marked paired", notWanted)
		}
	}
}

// The suggested name makes a pair recognisable in a list without pretending the
// name matters to the tunnel.
func TestTheSuggestedNameNamesTheOtherSide(t *testing.T) {
	l := sampleLink()
	l.Name = "hetzner-iran"
	if got := MirrorForPeer(l).Name; got != "hetzner-kharej" {
		t.Errorf("suggested name = %q, want hetzner-kharej", got)
	}
	l.Name = ""
	if got := MirrorForPeer(l).Name; got != "" {
		t.Errorf("a link with no name suggested %q", got)
	}
}

// A spoof tunnel on its defaults still has to tell the other side where the
// packets really come from.
//
// The far end cannot learn it: every packet it receives carries a forged
// source, and the engine refuses to start without the address. It used to be
// carried only when some spoof tuning had also been set, so the ordinary case —
// pick the carrier, accept the defaults — produced a link that built one end
// and could never build the other.
func TestASpoofLinkCarriesTheRealAddressEvenWithNoTuning(t *testing.T) {
	l := ShareLink{
		Name: "sp", Kind: "direct", From: "iran", Tr: "spoof",
		Host: "198.51.100.5", Port: "9000", Tok: "tok",
		LocalIP: "10.0.0.1", PeerIP: "10.0.0.2",
	}
	f := MirrorForPeer(l)
	if f.SpoofPeerIP != "198.51.100.5" {
		t.Errorf("SpoofPeerIP = %q, want the producer's real address", f.SpoofPeerIP)
	}
	if f.ToNewDirectTunnel().SpoofPeerIP != "198.51.100.5" {
		t.Error("the setup form built from the mirror drops the address again")
	}
}

package manage

import (
	"strings"
	"testing"
)

// The setup link, end to end through the two functions the menu calls.
//
// Everything below this existed and was reachable from nothing: the panel
// encoded a link and decoded it again in the same process to derive a peer's
// config, and that was the only caller. The codec had error messages written
// for somebody holding a pasted string and no way for anybody to hold one.
//
// What is tested here is the claim the feature makes: a link made on one side
// produces the other side, with every paired setting carried across. That is
// the whole value — a tunnel has around thirty paired settings and the failure
// a mismatch produces is the one that reads as "connected, carrying nothing".

func TestALinkProducesTheOppositeSide(t *testing.T) {
	iran := ShareLink{
		Kind: "reverse", From: "iran", Name: "edge", Tok: "a-shared-secret",
		Tr: "wss", Port: "8443", Host: "203.0.113.9",
		Ports: "443, 8080=127.0.0.1:80", Preset: "turbo", MSS: 1360,
	}
	encoded, err := iran.Encode()
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	back, err := DecodeShareLink(encoded)
	if err != nil {
		t.Fatalf("decoding a link this build made: %v", err)
	}
	form := MirrorForPeer(back)

	// The side flips. That is the point: you paste it into the other machine.
	if form.Side != "kharej" {
		t.Fatalf("a link from iran produced the %s side", form.Side)
	}
	// Everything the two ends must agree on comes across untouched.
	if form.Token != iran.Tok {
		t.Errorf("token = %q, want %q — a token that does not survive this is a "+
			"tunnel that never authenticates", form.Token, iran.Tok)
	}
	if form.TunnelPort != iran.Port {
		t.Errorf("tunnel port = %q, want %q", form.TunnelPort, iran.Port)
	}
	if form.Transport != iran.Tr {
		t.Errorf("transport = %q, want %q", form.Transport, iran.Tr)
	}
	if form.Preset != iran.Preset || form.MSS != iran.MSS {
		t.Errorf("tuning did not survive: preset %q, mss %d", form.Preset, form.MSS)
	}
	// The kharej side dials in and publishes nothing, so it is not handed the
	// Iran side's forwarded ports.
	if form.ServerAddr != iran.Host {
		t.Errorf("the kharej end was not told where to dial: %q", form.ServerAddr)
	}
	if form.Ports != "" {
		t.Errorf("the kharej end was given ports to expose (%q); which services a "+
			"machine publishes is its own decision", form.Ports)
	}
}

// The other direction: a link made on the kharej side builds the Iran end, and
// that one *does* get the ports, because it is the end that exposes them.
func TestALinkFromKharejBuildsTheEndThatPublishes(t *testing.T) {
	kharej := ShareLink{
		Kind: "reverse", From: "kharej", Tok: "secret", Tr: "tcp",
		Port: "443", Host: "198.51.100.4", Ports: "80=127.0.0.1:8080",
	}
	encoded, err := kharej.Encode()
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	back, err := DecodeShareLink(encoded)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	form := MirrorForPeer(back)

	if form.Side != "iran" {
		t.Fatalf("side = %q", form.Side)
	}
	if form.Ports != "80=127.0.0.1:8080" {
		t.Errorf("the publishing end was not given the ports: %q", form.Ports)
	}
}

// A link is a secret. The screen that shows one has to say so, because it looks
// like a URL and people paste URLs into places they should not.
func TestTheLinkIsRecognisableAsASecret(t *testing.T) {
	l := ShareLink{Kind: "reverse", From: "iran", Tok: "the-token", Tr: "tcp", Port: "443"}
	encoded, err := l.Encode()
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	// The token must not be readable in it at a glance — it is compressed and
	// base64'd, which is not encryption and is not claimed to be, but a token
	// sitting in plain sight in a string people paste around is worse.
	if strings.Contains(encoded, l.Tok) {
		t.Error("the token appears verbatim in the link")
	}
	// And it must still round-trip, or the above is just corruption.
	back, err := DecodeShareLink(encoded)
	if err != nil || back.Tok != l.Tok {
		t.Fatalf("round trip lost the token: %v, %q", err, back.Tok)
	}
}

// A link for a direct tunnel swaps the private addresses, because the
// producer's local address is the receiver's peer.
func TestADirectLinkSwapsThePrivateAddresses(t *testing.T) {
	d := ShareLink{
		Kind: "direct", From: "kharej", Tok: "secret", Tr: "udp",
		Port: "9000", Host: "198.51.100.4",
		LocalIP: "10.9.0.1/24", PeerIP: "10.9.0.2",
	}
	encoded, err := d.Encode()
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	back, err := DecodeShareLink(encoded)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	form := MirrorForPeer(back)

	if form.LocalIP != "10.9.0.2" {
		t.Errorf("local = %q, want the producer's peer address", form.LocalIP)
	}
	if form.PeerIP != "10.9.0.1" {
		t.Errorf("peer = %q, want the producer's local address", form.PeerIP)
	}
}

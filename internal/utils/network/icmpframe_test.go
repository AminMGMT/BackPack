package network

import (
	"bytes"
	"testing"
)

// The identity is derived, not exchanged, so the two ends of a tunnel must
// arrive at the same tag and identifier from the same token alone.
func TestXdiIdentityIsDeterministic(t *testing.T) {
	id1, tag1 := xdiIdentity("a-shared-tunnel-token")
	id2, tag2 := xdiIdentity("a-shared-tunnel-token")
	if id1 != id2 || tag1 != tag2 {
		t.Fatalf("the same token produced two identities: (%d,%x) vs (%d,%x)", id1, tag1, id2, tag2)
	}
	if id1 < 0 || id1 > 0xffff {
		t.Fatalf("identifier %d does not fit the 16-bit ICMP field", id1)
	}
}

// Two tunnels with different tokens must not share a tag — the tag is the whole
// isolation guarantee. (A 16-bit identifier collision is tolerable because the
// tag still separates them; a tag collision is not.)
func TestDifferentTokensGetDifferentTags(t *testing.T) {
	seen := map[[xdiTagLen]byte]string{}
	for _, tok := range []string{
		"token-one", "token-two", "token-three",
		"aaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaab", "",
		"a-real-looking-tunnel-token-0123456789",
	} {
		_, tag := xdiIdentity(tok)
		if other, ok := seen[tag]; ok {
			t.Fatalf("tokens %q and %q collided on tag %x", tok, other, tag)
		}
		seen[tag] = tok
	}
}

// A packet encoded by one side must decode on the other, and the payload must
// come back byte for byte.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	_, tag := xdiIdentity("token")
	payload := []byte("a KCP packet's worth of bytes \x00\x01\xff")

	// server -> client
	wire := encodeXdiPayload(tag, xdiDirServer, payload)
	got, ok := decodeXdiPayload(tag, inboundDir(false), wire) // client accepts server dir
	if !ok {
		t.Fatal("the client rejected a packet the server sent it")
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload changed in transit: %q vs %q", got, payload)
	}

	// client -> server
	wire = encodeXdiPayload(tag, xdiDirClient, payload)
	if got, ok := decodeXdiPayload(tag, inboundDir(true), wire); !ok || !bytes.Equal(got, payload) {
		t.Fatal("the server did not accept a packet the client sent it")
	}
}

// The kernel's automatic echo reply is the case the direction byte exists for.
// It carries the client's outbound payload — tag and all — bounced straight
// back to the client. The client must reject it, or KCP would see its own
// packets returned as if from the server.
func TestKernelEchoReplyIsRejected(t *testing.T) {
	_, tag := xdiIdentity("token")

	// What the client sent: its outbound direction.
	clientOutbound := encodeXdiPayload(tag, xdiDirClient, []byte("client data"))

	// The kernel bounces that exact payload back to the client. The client,
	// which accepts only the server's direction, must drop it.
	if _, ok := decodeXdiPayload(tag, inboundDir(false), clientOutbound); ok {
		t.Fatal("the client accepted the kernel's echo of its own packet")
	}
}

// Another tunnel's traffic, arriving on the same raw socket, must be dropped on
// the tag alone.
func TestForeignTagIsRejected(t *testing.T) {
	_, mine := xdiIdentity("my-token")
	_, theirs := xdiIdentity("their-token")

	theirPacket := encodeXdiPayload(theirs, xdiDirClient, []byte("not for me"))
	if _, ok := decodeXdiPayload(mine, inboundDir(true), theirPacket); ok {
		t.Fatal("one tunnel accepted another tunnel's packet")
	}
}

// A bare ping — no framing at all, or too short to hold a header — must be
// dropped rather than read past its end.
func TestShortOrEmptyPayloadIsRejected(t *testing.T) {
	_, tag := xdiIdentity("token")
	for _, data := range [][]byte{
		nil,
		{},
		{0x01},
		tag[:], // the tag but no direction byte
	} {
		if _, ok := decodeXdiPayload(tag, xdiDirClient, data); ok {
			t.Fatalf("a %d-byte payload was accepted", len(data))
		}
	}
}

// Each side sends its own direction and accepts the other's — never its own.
func TestDirectionsAreOpposite(t *testing.T) {
	if outboundDir(true) == outboundDir(false) {
		t.Fatal("server and client send the same direction marker")
	}
	if outboundDir(true) != inboundDir(false) || outboundDir(false) != inboundDir(true) {
		t.Fatal("one side's outbound is not the other's inbound")
	}
	if inboundDir(true) == outboundDir(true) {
		t.Fatal("a side accepts its own direction — it would read its own kernel echoes")
	}
}

// KCP is told a smaller MTU over ICMP than over UDP, because the echo header
// and the tunnel's own framing eat into the packet. Getting this wrong would
// let a full-window KCP packet fragment — the one thing the MTU setting exists
// to prevent.
func TestEffectiveMTUShrinksOverICMP(t *testing.T) {
	udp := KCPSettings{MTU: 1350, UseICMP: false}
	if udp.effectiveMTU() != 1350 {
		t.Errorf("UDP MTU changed: got %d, want 1350", udp.effectiveMTU())
	}

	icmp := KCPSettings{MTU: 1350, UseICMP: true}
	got := icmp.effectiveMTU()
	if got != 1350-icmpMTUOverhead {
		t.Errorf("ICMP effective MTU = %d, want %d", got, 1350-icmpMTUOverhead)
	}
	if got >= udp.effectiveMTU() {
		t.Error("the ICMP MTU is not smaller than the UDP one")
	}
	// The overhead must cover the real headers: 8-byte ICMP echo header plus
	// the tag-and-direction prefix. Anything less would fragment.
	if icmpMTUOverhead < 8+xdiHeaderLen {
		t.Errorf("overhead %d is too small for the ICMP and framing headers", icmpMTUOverhead)
	}
}

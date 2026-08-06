package utils

import "testing"

func TestForwardUDPRoundTripAndMalformedPackets(t *testing.T) {
	p, err := EncodeForwardUDP("tok:en\x00", "[2001:db8::1]:443")
	if err != nil {
		t.Fatal(err)
	}
	token, target, err := DecodeForwardUDP(p)
	if err != nil || token != "tok:en\x00" || target != "[2001:db8::1]:443" {
		t.Fatalf("decoded %q %q err=%v", token, target, err)
	}
	for _, bad := range [][]byte{nil, {SG_ForwardUDP}, {SG_ForwardUDP, 0, 1, 0, 1, 'x'}} {
		if _, _, err := DecodeForwardUDP(bad); err == nil {
			t.Fatalf("accepted malformed packet %v", bad)
		}
	}
}

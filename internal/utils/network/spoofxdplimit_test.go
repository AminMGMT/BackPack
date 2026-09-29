package network

import (
	"testing"

	"golang.org/x/net/ipv4"
)

// The XDP path captures at most maxXDPPayload bytes of a segment; a spoof_mtu
// that allows larger ones must not use it, or those packets vanish.
func TestXDPIsUsedOnlyWhereEveryPacketFits(t *testing.T) {
	for mtu, want := range map[int]bool{
		1500:                               true,
		maxXDPPayload + ipv4.HeaderLen:     true,
		maxXDPPayload + ipv4.HeaderLen + 1: false,
		9000:                               false,
	} {
		if got := xdpCanCarry(mtu); got != want {
			t.Errorf("xdpCanCarry(%d) = %v, want %v", mtu, got, want)
		}
	}
}

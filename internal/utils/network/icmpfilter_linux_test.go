package network

import (
	"testing"

	"golang.org/x/net/bpf"
)

// pkt is an IPv4 packet (with ihl words of header) carrying an ICMP echo of
// the given type and identifier.
func pkt(ihl int, typ uint8, id uint16) []byte {
	b := make([]byte, ihl*4+16)
	b[0] = 0x40 | byte(ihl)
	b[ihl*4] = typ
	b[ihl*4+4], b[ihl*4+5] = byte(id>>8), byte(id)
	return b
}

func TestTheICMPFilterKeepsOnlyThisSessionsReplies(t *testing.T) {
	client, err := bpf.NewVM(icmpFilter(0, 4242))
	if err != nil {
		t.Fatal(err)
	}
	server, err := bpf.NewVM(icmpFilter(8, -1))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		vm     *bpf.VM
		packet []byte
		keep   bool
	}{
		{"the client's own reply", client, pkt(5, 0, 4242), true},
		{"the client's own reply, IPv4 options present", client, pkt(7, 0, 4242), true},
		{"another session's reply", client, pkt(5, 0, 4243), false},
		{"an echo request on the client", client, pkt(5, 8, 4242), false},
		{"an unreachable on the client", client, pkt(5, 3, 4242), false},
		{"any request on the server", server, pkt(5, 8, 7), true},
		{"a reply on the server", server, pkt(5, 0, 7), false},
	} {
		n, err := tc.vm.Run(tc.packet)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if (n > 0) != tc.keep {
			t.Errorf("%s: kept=%v, want %v", tc.name, n > 0, tc.keep)
		}
	}
}

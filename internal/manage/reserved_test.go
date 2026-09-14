package manage

import (
	"reflect"
	"testing"
)

// Only the left of a mapping is local. The right names a service on the far
// side of the tunnel, and reserving its port here would protect a number this
// machine never binds while telling the operator it had done something.
func TestListenPortsOfReadsOnlyTheLocalSide(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []int
	}{
		{"a bare port", "443", []int{443}},
		{"mapped to a far service", "443=127.0.0.1:2096", []int{443}},
		{"mapped to another host", "443=10.0.0.5:8443", []int{443}},
		{"a range", "10000-10002", []int{10000, 10001, 10002}},
		{"a range with an offset target", "10000-10002=20000-20002", []int{10000, 10001, 10002}},
		{"pinned to an address", "1.2.3.4:443=127.0.0.1:2096", []int{443}},
		{"pinned to an IPv6 address", "[2a01:4f8::1]:443=127.0.0.1:2096", []int{443}},
		{"a range pinned to an address", "1.2.3.4:500-501", []int{500, 501}},
		{"surrounding whitespace", "  443 = 127.0.0.1:2096 ", []int{443}},

		{"empty", "", nil},
		{"not a port", "http=127.0.0.1:80", nil},
		{"zero", "0=127.0.0.1:80", nil},
		{"past the top", "65536", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := listenPortsOf(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("listenPortsOf(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// A range written backwards is a typo, not a thousand ports. It must not turn
// into an enormous list, and it must not be silently dropped either — the first
// port is real and reserving it is right.
func TestListenPortsOfRefusesABackwardsRange(t *testing.T) {
	got := listenPortsOf("500-400")
	if !reflect.DeepEqual(got, []int{500}) {
		t.Errorf("listenPortsOf(\"500-400\") = %v, want [500]", got)
	}
}

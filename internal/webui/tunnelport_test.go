package webui

import "testing"

// The tunnel port is the one number on a server card that cannot be found
// anywhere else on the page — and it was declared, documented, and never
// assigned, so every card showed a dash there.
func TestTunnelPortIsReducedToThePortAlone(t *testing.T) {
	for _, tc := range []struct{ addr, want string }{
		{"0.0.0.0:1231", "1231"},   // the usual server bind
		{"[::]:8443", "8443"},      // dual-stack
		{"127.0.0.1:9000", "9000"}, // loopback
		{":443", "443"},            // bare port
		{"", ""},                   // nothing to report rather than a guess
		{"not-an-address", ""},
	} {
		if got := tunnelPortOf(tc.addr); got != tc.want {
			t.Errorf("tunnelPortOf(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

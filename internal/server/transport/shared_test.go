package transport

import "testing"

// The bound is half the point of this helper: 1 and 65535 are valid ports and
// the config validator lets them through, so the mapping parser has to as well.
// The other half is the optional bind address, which a multi-homed host needs in
// order to put the control channel and the exposed ports on separate public IPs.
func TestExpandListenSpec(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		addrs []string
		ports []string
	}{
		{"lowest port", "1", []string{":1"}, []string{"1"}},
		{"highest port", "65535", []string{":65535"}, []string{"65535"}},
		{"ordinary port", "443", []string{":443"}, []string{"443"}},
		{"surrounding whitespace", " 8080 ", []string{":8080"}, []string{"8080"}},
		{"wildcard address", ":443", []string{":443"}, []string{"443"}},

		{"IPv4 address and port", "127.0.0.1:443", []string{"127.0.0.1:443"}, []string{"443"}},
		{"IPv6 address and port", "[::1]:443", []string{"[::1]:443"}, []string{"443"}},

		{"range", "443-445", []string{":443", ":444", ":445"}, []string{"443", "444", "445"}},
		{"single-port range", "443-443", []string{":443"}, []string{"443"}},

		// The case this helper exists for: every transport used to test for "-"
		// before it looked for a host, so an address in front of a range was
		// handed whole to strconv.Atoi and the tunnel died on startup.
		{
			"IPv4 address and range",
			"10.0.0.5:443-445",
			[]string{"10.0.0.5:443", "10.0.0.5:444", "10.0.0.5:445"},
			[]string{"443", "444", "445"},
		},
		{
			"IPv6 address and range",
			"[::1]:443-444",
			[]string{"[::1]:443", "[::1]:444"},
			[]string{"443", "444"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandListenSpec(tt.in)
			if err != nil {
				t.Fatalf("expandListenSpec(%q) returned an error: %v", tt.in, err)
			}
			if len(got) != len(tt.addrs) {
				t.Fatalf("expandListenSpec(%q) produced %d listeners, want %d",
					tt.in, len(got), len(tt.addrs))
			}
			for i, l := range got {
				if l.addr != tt.addrs[i] {
					t.Errorf("listener %d address = %q, want %q", i, l.addr, tt.addrs[i])
				}
				if l.port != tt.ports[i] {
					t.Errorf("listener %d port = %q, want %q", i, l.port, tt.ports[i])
				}
			}
		})
	}
}

// A bad mapping has to be reported rather than turned into a listener on some
// address nobody asked for.
func TestExpandListenSpecRejectsWhatIsNotAPort(t *testing.T) {
	for _, in := range []string{
		"",
		"0",           // zero is not a port
		"65536",       // past the top of the range
		"-1",          // negative
		"http",        // not a number at all
		"450-443",     // ends before it starts
		"443-450-460", // not a range
		"10.0.0.5:0",
		"10.0.0.5:65536",
	} {
		t.Run(in, func(t *testing.T) {
			if got, err := expandListenSpec(in); err == nil {
				t.Fatalf("expandListenSpec(%q) = %v, want an error", in, got)
			}
		})
	}
}

package transport

import "testing"

// The bound is the whole point of this helper: 1 and 65535 are valid ports and
// the config validator lets them through, so the mapping parser has to as well.
func TestListenAddrForAcceptsTheWholePortRange(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"lowest port", "1", ":1"},
		{"highest port", "65535", ":65535"},
		{"ordinary port", "443", ":443"},
		{"surrounding whitespace", " 8080 ", ":8080"},
		{"zero is not a port", "0", "0"},
		{"past the top of the range", "65536", "65536"},
		{"negative", "-1", "-1"},
		{"IPv4 address and port", "127.0.0.1:443", "127.0.0.1:443"},
		{"IPv6 address and port", "[::1]:443", "[::1]:443"},
		{"wildcard address", ":443", ":443"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := listenAddrFor(tt.in); got != tt.want {
				t.Fatalf("listenAddrFor(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

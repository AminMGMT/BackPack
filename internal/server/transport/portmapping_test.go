package transport

import "testing"

func TestMappingListenAddress(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"lowest port", "1", ":1"},
		{"highest port", "65535", ":65535"},
		{"typical port", "443", ":443"},
		{"whitespace", " 443 ", ":443"},
		{"IPv4 address", "127.0.0.1:443", "127.0.0.1:443"},
		{"IPv6 address", "[::1]:443", "[::1]:443"},
		{"below range", "0", "0"},
		{"above range", "65536", "65536"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mappingListenAddress(tt.value); got != tt.want {
				t.Fatalf("mappingListenAddress(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

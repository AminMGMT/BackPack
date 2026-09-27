package network

import "testing"

func TestATargetResolvesWithOrWithoutAHostIncludingIPv6(t *testing.T) {
	for in, want := range map[string]struct {
		port int
		addr string
	}{
		"8080":                 {8080, "127.0.0.1:8080"},
		"10.0.0.5:443":         {443, "10.0.0.5:443"},
		"example.com:80":       {80, "example.com:80"},
		"[2001:db8::1]:443":    {443, "[2001:db8::1]:443"},
		"[::1]:53":             {53, "[::1]:53"},
		"10.0.0.1:80|[::1]:81": {80, "10.0.0.1:80|[::1]:81"},
	} {
		port, addr, err := ResolveRemoteAddr(in)
		if err != nil || port != want.port || addr != want.addr {
			t.Errorf("ResolveRemoteAddr(%q) = %d, %q, %v; want %d, %q", in, port, addr, err, want.port, want.addr)
		}
	}
	for _, bad := range []string{"", "abc", "10.0.0.1:x", "::1"} {
		if _, _, err := ResolveRemoteAddr(bad); err == nil {
			t.Errorf("ResolveRemoteAddr(%q) accepted a target with no usable port", bad)
		}
	}
}

package manage

import (
	"fmt"
	"testing"

	"github.com/backpack/backpack/internal/app"
)

// The whole point of the change: 1080 is bound only when a tunnel genuinely
// still maps to it. A machine with no such tunnel — every fresh install, and
// every install whose tunnels use the derived port — must report false, so the
// monitor leaves the well-known port for whatever else wants it.
func TestLegacySocksInUse(t *testing.T) {
	legacyMapping := fmt.Sprintf("9000=127.0.0.1:%d", app.SocksInternalPort)

	cases := []struct {
		name    string
		tunnels []Tunnel
		want    bool
	}{
		{"no tunnels", nil, false},
		{"a tunnel with no ports", []Tunnel{{Name: "a"}}, false},
		{
			"only derived-port mappings",
			[]Tunnel{{Name: "a", Ports: []string{"9000=127.0.0.1:34567", "22=127.0.0.1:22"}}},
			false,
		},
		{
			"a legacy 1080 mapping",
			[]Tunnel{{Name: "a", Ports: []string{legacyMapping}}},
			true,
		},
		{
			"legacy mapping among others",
			[]Tunnel{
				{Name: "a", Ports: []string{"80=127.0.0.1:8080"}},
				{Name: "b", Ports: []string{"443=127.0.0.1:443", legacyMapping}},
			},
			true,
		},
		{
			"whitespace around the mapping",
			[]Tunnel{{Name: "a", Ports: []string{"  " + legacyMapping + "  "}}},
			true,
		},
		{
			// 1080 as the SOURCE port, not the destination, is not a SOCKS
			// relay mapping and must not trigger the bind.
			"1080 on the wrong side of the mapping",
			[]Tunnel{{Name: "a", Ports: []string{"1080=127.0.0.1:9000"}}},
			false,
		},
		{
			// A destination of 1080 on some other host is not the loopback
			// relay port.
			"1080 on another host",
			[]Tunnel{{Name: "a", Ports: []string{"9000=10.0.0.5:1080"}}},
			false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LegacySocksInUse(tc.tunnels); got != tc.want {
				t.Fatalf("LegacySocksInUse = %v, want %v", got, tc.want)
			}
		})
	}
}

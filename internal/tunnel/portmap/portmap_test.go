package portmap

import (
	"strings"
	"testing"
)

const peer = "10.10.0.2"

func TestExpandForwards(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want []string // "listen -> target|target"
	}{
		{
			name: "bare port goes to the same port on the peer",
			spec: "443",
			want: []string{":443 -> 10.10.0.2:443"},
		},
		{
			name: "explicit target port",
			spec: "443=8443",
			want: []string{":443 -> 10.10.0.2:8443"},
		},
		{
			name: "explicit target host",
			spec: "443=192.168.1.5:8443",
			want: []string{":443 -> 192.168.1.5:8443"},
		},
		{
			name: "local bind address",
			spec: "127.0.0.1:443=8443",
			want: []string{"127.0.0.1:443 -> 10.10.0.2:8443"},
		},
		{
			name: "range to the same ports",
			spec: "10000-10002",
			want: []string{
				":10000 -> 10.10.0.2:10000",
				":10001 -> 10.10.0.2:10001",
				":10002 -> 10.10.0.2:10002",
			},
		},
		{
			name: "range preserving the offset",
			spec: "10000-10002=20000-20002",
			want: []string{
				":10000 -> 10.10.0.2:20000",
				":10001 -> 10.10.0.2:20001",
				":10002 -> 10.10.0.2:20002",
			},
		},
		{
			name: "range fanning into one port",
			spec: "10000-10002=8080",
			want: []string{
				":10000 -> 10.10.0.2:8080",
				":10001 -> 10.10.0.2:8080",
				":10002 -> 10.10.0.2:8080",
			},
		},
		{
			name: "several backends",
			spec: "443=10.0.0.1:80|10.0.0.2:80",
			want: []string{":443 -> 10.0.0.1:80|10.0.0.2:80"},
		},
		{
			name: "backends defaulting to the peer",
			spec: "443=80|8080",
			want: []string{":443 -> 10.10.0.2:80|10.10.0.2:8080"},
		},
		{
			name: "bracketed IPv6 target",
			spec: "443=[fd00::2]:8443",
			want: []string{":443 -> [fd00::2]:8443"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Expand([]string{tc.spec}, peer)
			if err != nil {
				t.Fatalf("Expand(%q): %v", tc.spec, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("Expand(%q) produced %d mappings, want %d:\n%v",
					tc.spec, len(got), len(tc.want), got)
			}
			for i, want := range tc.want {
				if got[i].String() != want {
					t.Fatalf("mapping %d = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}

// A bare port must forward to the peer, not back to the local bind address it
// was given. Getting this wrong would quietly forward a port to itself.
func TestBarePortWithLocalHostStillTargetsThePeer(t *testing.T) {
	got, err := Expand([]string{"127.0.0.1:443"}, peer)
	if err != nil {
		t.Fatalf("expandForwards: %v", err)
	}
	want := "127.0.0.1:443 -> 10.10.0.2:443"
	if got[0].String() != want {
		t.Fatalf("mapping = %q, want %q", got[0], want)
	}
}

func TestExpandForwardsRejectsBadMappings(t *testing.T) {
	cases := map[string]string{
		"port zero":                    "0",
		"port too large":               "70000",
		"not a number":                 "https",
		"backwards range":              "200-100",
		"range too wide":               "1-2000",
		"mismatched ranges":            "10000-10002=20000-20005",
		"range with several backends":  "10000-10002=1|2",
		"backend that is a range":      "443=100-200|300",
		"unbracketed IPv6":             "fd00::2:443",
		"empty":                        "=",
		"target range, single listen":  "443=100-200",
		"unparseable bracketed target": "443=[fd00::2]8443",
	}
	for name, spec := range cases {
		if _, err := Expand([]string{spec}, peer); err == nil {
			t.Fatalf("%s: Expand(%q) was accepted", name, spec)
		}
	}
}

// The whole set is bounded too, not just each range.
func TestExpandForwardsBoundsTheTotal(t *testing.T) {
	specs := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		lo := 10000 + i*1000
		specs = append(specs, itoa(lo)+"-"+itoa(lo+999))
	}
	if _, err := Expand(specs, peer); err == nil {
		t.Fatal("expandForwards accepted more than the total port limit")
	}
}

func itoa(n int) string {
	var b strings.Builder
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append(digits, byte('0'+n%10))
		n /= 10
	}
	for i := len(digits) - 1; i >= 0; i-- {
		b.WriteByte(digits[i])
	}
	return b.String()
}

// A target with no host and no peer to default to cannot be resolved, and must
// be refused rather than silently becoming a bind to nothing.
func TestExpandForwardsNeedsAPeerForBareTargets(t *testing.T) {
	if _, err := Expand([]string{"443"}, ""); err == nil {
		t.Fatal("a bare target was accepted with no peer address")
	}
	// An explicit host needs no peer.
	if _, err := Expand([]string{"443=192.168.1.5:80"}, ""); err != nil {
		t.Fatalf("an explicit target was refused with no peer address: %v", err)
	}
}

func TestExpandForwardsSkipsBlankSpecs(t *testing.T) {
	got, err := Expand([]string{"", "  ", "443"}, peer)
	if err != nil {
		t.Fatalf("expandForwards: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("produced %d mappings, want 1", len(got))
	}
}

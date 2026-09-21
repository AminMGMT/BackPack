package manage

import (
	"strings"
	"testing"
)

func TestCleanChainRejectsWhatCannotWork(t *testing.T) {
	cases := []struct {
		name    string
		primary string
		list    []string
		want    string // substring of the expected error, "" for accepted
	}{
		{"a carrier that is not a reverse transport", "tcp", []string{"sni"}, "not a reverse tunnel transport"},
		{"a typo", "tcp", []string{"wss", "tcpp"}, "not a reverse tunnel transport"},
		{"the l3-only spoof carrier", "tcp", []string{"spoof"}, "not a reverse tunnel transport"},
		{"a good chain", "tcp", []string{"wss", "quic"}, ""},
		{"an empty chain", "tcp", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := cleanChain(c.primary, c.list)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("rejected a valid chain: %v", err)
			case c.want != "" && err == nil:
				t.Fatalf("accepted %v, expected an error about %q", c.list, c.want)
			case c.want != "" && !strings.Contains(err.Error(), c.want):
				t.Fatalf("error was %q, expected it to mention %q", err, c.want)
			}
		})
	}
}

// The chain always starts at the tunnel's own transport, so naming it again is
// noise in the file rather than a second attempt at it.
func TestCleanChainDropsThePrimaryAndDuplicates(t *testing.T) {
	got, err := cleanChain("tcp", []string{"TCP", "wss", " quic ", "wss", ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"wss", "quic"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// The summary is what an operator reads to decide whether this is on, so "no
// chain" has to read as off rather than as an empty list.
func TestChainSummary(t *testing.T) {
	if got := chainSummary("tcp", nil); !strings.Contains(got, "only") {
		t.Fatalf("an absent chain rendered as %q", got)
	}
	if got := chainSummary("tcp", []string{"wss", "quic"}); got != "tcp → wss → quic" {
		t.Fatalf("chain rendered as %q", got)
	}
}

// The rendered config must carry the chain, and must not carry the key at all
// when there is none — an empty list written out would change every existing
// tunnel's file on the next edit for no reason.
func TestTheChainIsRenderedOnlyWhenItExists(t *testing.T) {
	s := TunnelSpec{
		Role: "client", Name: "t", Transport: "tcp",
		RemoteAddr: "1.2.3.4:443", Token: "x", LogLevel: "info",
	}
	if out := s.Render(); strings.Contains(out, "fallback_transports") {
		t.Fatalf("a tunnel with no chain wrote the key:\n%s", out)
	}

	s.FallbackTransports = []string{"wss", "quic"}
	s.FallbackDwell = 45
	out := s.Render()
	for _, want := range []string{"fallback_transports", `"wss"`, `"quic"`, "fallback_dwell = 45"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered config is missing %q:\n%s", want, out)
		}
	}
	// Order is the whole mechanism: both ends walk the list in the order it is
	// written, so a render that reorders would desynchronise the two ends.
	if strings.Index(out, `"wss"`) > strings.Index(out, `"quic"`) {
		t.Fatalf("the chain was rendered out of order:\n%s", out)
	}
}

// A server tunnel writes the chain too. It has to: a chain on the client alone
// leaves the server listening for one carrier, which is the failure mode the
// menu screen warns about and the one most likely to be shipped by accident.
func TestAServerTunnelRendersTheChain(t *testing.T) {
	s := TunnelSpec{
		Role: "server", Name: "t", Transport: "tcp",
		BindAddr: "0.0.0.0:443", Token: "x", LogLevel: "info",
		Ports:              []string{"80=127.0.0.1:80"},
		FallbackTransports: []string{"wss"},
	}
	if out := s.Render(); !strings.Contains(out, "fallback_transports") {
		t.Fatalf("a server tunnel dropped the chain:\n%s", out)
	}
}

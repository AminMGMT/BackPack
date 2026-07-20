package manage

import (
	"strings"
	"testing"
)

// The relay mappings are internal plumbing the bot adds for itself. Showing
// them in a ports list is noise at best; it also advertises which tunnel
// carries the bot and where it points.
func TestRelayMappingsAreHiddenFromPortLists(t *testing.T) {
	const token = "tok-0123456789abcdefghijklmnop"

	ports := []string{
		"3232",
		"8080=127.0.0.1:2096",
		"43643=api.telegram.org:443", // the Telegram forward
		"28454=127.0.0.1:1080",       // the old SOCKS mapping
	}

	got := VisiblePorts(ports, token)
	joined := strings.Join(got, ", ")

	for _, hidden := range []string{"api.telegram.org", "127.0.0.1:1080", "43643", "28454"} {
		if strings.Contains(joined, hidden) {
			t.Errorf("%q is still visible in %q", hidden, joined)
		}
	}
	for _, shown := range []string{"3232", "8080=127.0.0.1:2096"} {
		if !strings.Contains(joined, shown) {
			t.Errorf("the user's own port %q was hidden: %q", shown, joined)
		}
	}
	if len(got) != 2 {
		t.Errorf("got %d visible ports, want 2: %v", len(got), got)
	}
}

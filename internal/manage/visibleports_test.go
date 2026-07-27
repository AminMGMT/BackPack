package manage

import (
	"fmt"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/app"
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

// SplitBotRelay is what the web panel shows, and it exists because the panel
// used to carry its own idea of what a relay mapping looks like — one that knew
// only the oldest of the three shapes. The current shape, the mapping straight
// to the Telegram API, was therefore listed among the operator's forwarded
// ports as "127.0.0.1:28583=api.telegram.org:443": machinery presented as a
// decision somebody made, and an invitation to delete it and wonder later why
// the bot stopped.
func TestSplitBotRelayHidesEveryShapeOfRelayMapping(t *testing.T) {
	const token = "relay-token-0123456789abcdefgh"

	for _, tc := range []struct {
		name    string
		mapping string
		port    int // the port worth naming, 0 when there is none
	}{
		{"the mapping to the Telegram API", "127.0.0.1:28583=" + TelegramHost, 28583},
		{"the same mapping written without a bind address", "28583=" + TelegramHost, 28583},
		{"the legacy fixed SOCKS port", fmt.Sprintf("9050=127.0.0.1:%d", app.SocksInternalPort), 0},
		{"the SOCKS port derived from the token",
			fmt.Sprintf("9050=127.0.0.1:%d", app.SocksPortForToken(token)), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ports := []string{"443", "8080=127.0.0.1:8080", tc.mapping}

			visible, relayPort, found := SplitBotRelay(ports, token)

			if !found {
				t.Fatalf("%q was not recognised as the relay mapping", tc.mapping)
			}
			for _, p := range visible {
				if p == tc.mapping {
					t.Errorf("%q is still listed as a forwarded port", tc.mapping)
				}
			}
			if len(visible) != 2 {
				t.Errorf("kept %d ports, want the 2 the operator configured: %v", len(visible), visible)
			}
			if relayPort != tc.port {
				t.Errorf("relay port = %d, want %d", relayPort, tc.port)
			}
		})
	}
}

// A tunnel with no relay must report none, or the panel draws a Bot Relay
// section for every tunnel whether or not the bot uses it.
func TestSplitBotRelayReportsNothingWhenThereIsNoRelay(t *testing.T) {
	visible, port, found := SplitBotRelay([]string{"443", "8080=127.0.0.1:8080"}, "some-token")

	if found {
		t.Error("a relay was reported on a tunnel that has none")
	}
	if port != 0 {
		t.Errorf("relay port = %d with no relay present, want 0", port)
	}
	if len(visible) != 2 {
		t.Errorf("kept %d ports, want 2 — nothing should have been filtered", len(visible))
	}
}

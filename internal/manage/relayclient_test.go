package manage

import (
	"fmt"
	"testing"

	"github.com/backpack/backpack/internal/app"
)

// The updater reaches GitHub either directly or through the tunnel relay. On an
// Iran server the direct path does not work, so the relay is the only one — and
// finding it means recognising the relay mapping in a tunnel's config.
//
// There are two forms of that mapping: the legacy fixed 1080, and the port
// derived from the tunnel token. Recognising only one of them leaves the
// updater with no path at all on exactly the machines that need the relay.

func TestRelayFoundForTokenDerivedPort(t *testing.T) {
	const token = "a-real-looking-token-0123456789abcdef"
	peer := app.SocksPortForToken(token)
	ports := []string{"443=1.1.1.1:443", fmt.Sprintf("41234=127.0.0.1:%d", peer)}

	if got := relayExposedPort(ports, token); got != "41234" {
		t.Fatalf("relayExposedPort = %q, want \"41234\" — the updater would be left "+
			"with nothing but a direct connection to GitHub", got)
	}
}

// Configs written before the port became token-derived must keep working.
func TestRelayFoundForLegacyPort(t *testing.T) {
	ports := []string{fmt.Sprintf("41234=127.0.0.1:%d", app.SocksInternalPort)}
	if got := relayExposedPort(ports, "some-token"); got != "41234" {
		t.Errorf("relayExposedPort = %q, want \"41234\" for a legacy 1080 mapping", got)
	}
}

// A mapping carrying an explicit bind address must still yield the port alone.
func TestRelayPortIsReadThroughABindAddress(t *testing.T) {
	const token = "another-token-0123456789abcdefghij"
	peer := app.SocksPortForToken(token)
	ports := []string{fmt.Sprintf("127.0.0.1:41234=127.0.0.1:%d", peer)}

	if got := relayExposedPort(ports, token); got != "41234" {
		t.Errorf("relayExposedPort = %q, want \"41234\" — the bind address was not stripped", got)
	}
}

// Ordinary forwarded ports must not be mistaken for the relay.
func TestNoRelayWhenNoMappingExists(t *testing.T) {
	for _, ports := range [][]string{
		{"443=1.1.1.1:443", "8080"},
		{"443-450"},
		nil,
	} {
		if got := relayExposedPort(ports, "some-token"); got != "" {
			t.Errorf("relayExposedPort(%v) = %q, want \"\" — an ordinary port was "+
				"treated as the relay mapping", ports, got)
		}
	}
}

// A tunnel with no token must not match another tunnel's derived port.
func TestEmptyTokenOnlyMatchesTheLegacyPort(t *testing.T) {
	const other = "someone-elses-token-0123456789abcd"
	ports := []string{fmt.Sprintf("41234=127.0.0.1:%d", app.SocksPortForToken(other))}

	if got := relayExposedPort(ports, ""); got != "" {
		t.Errorf("relayExposedPort = %q for an unrelated tunnel's derived port", got)
	}
}

package manage

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/backpack/backpack/internal/socks"
)

// Fetching an update through a tunnel when there is no way out directly.
//
// The updater already tries the tunnel relay after a direct connection, but it
// could only use a tunnel that already carried the mapping — the port a server
// tunnel exposes onto the peer's own SOCKS proxy. That mapping exists when the
// Telegram bot has been set up to relay through that tunnel, and otherwise it
// does not exist at all. So on the machine this matters most for — an Iran
// server with working tunnels and no route to GitHub — the updater found no
// relay and gave up, while a perfectly good way out was running the whole time.
//
// What was missing is the step the bot already takes: pick a tunnel that is up
// and add the mapping to it. That restarts the tunnel for a moment, which is
// why it is not done silently. The CLI asks first, names the tunnel, and says
// what it will cost; see runUpdate.

// RelayOption is a tunnel that could carry the download, and what using it
// would cost.
type RelayOption struct {
	Name string
	// Ready is true when the tunnel already exposes the relay port, so using it
	// costs nothing. Otherwise it has to be restarted to gain the mapping.
	Ready bool
}

// RelayOptions lists the tunnels that could fetch the update, cheapest first.
//
// Only server tunnels qualify: the relay works by exposing a local port that
// maps to a port on the peer, and only the server side exposes ports. Only
// online ones qualify either — a tunnel that is not carrying traffic cannot
// carry this, and offering it would turn a clear failure into a timeout.
func RelayOptions() []RelayOption {
	var out []RelayOption
	health := AllHealth()
	for name, h := range health {
		t, ok := Find(name)
		if !ok || t.Role != "server" || h.State != "online" {
			continue
		}
		out = append(out, RelayOption{Name: name, Ready: hasSocksPort(name)})
	}
	return orderRelayOptions(out)
}

// orderRelayOptions puts the cheapest choice first: a tunnel that already
// carries the relay port costs nothing, and one that does not has to be
// restarted. Ties break by name so the offer does not reorder itself between
// two runs that saw the same tunnels.
func orderRelayOptions(in []RelayOption) []RelayOption {
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].Ready != in[j].Ready {
			return in[i].Ready
		}
		return in[i].Name < in[j].Name
	})
	return in
}

// hasSocksPort reports whether a tunnel already exposes the peer's SOCKS proxy.
func hasSocksPort(name string) bool {
	spec, err := loadServerSpec(name)
	if err != nil {
		return false
	}
	return relayExposedPort(spec.Ports, spec.Token) != ""
}

// RelayClientVia prepares the named tunnel to carry outbound traffic and
// returns a client that goes through it.
//
// Preparing means adding the port mapping if it is not there, which restarts
// the tunnel. Whoever calls this has already decided that is acceptable.
func RelayClientVia(name string, timeout time.Duration) (*http.Client, error) {
	spec, err := loadServerSpec(name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	port, err := EnsureSocksPort(name)
	if err != nil {
		return nil, fmt.Errorf("could not prepare %s to relay: %w", name, err)
	}
	return socks.HTTPClient(fmt.Sprintf("127.0.0.1:%d", port), "backpack", spec.Token, timeout), nil
}

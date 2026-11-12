package config

import (
	"errors"
	"strings"
)

// LegacyDirectConfig is what is left of the [direct] stream tunnel, which
// Backpack v2.0.0 removed.
//
// It was a port forwarder dialled from Iran over tcp, stealth, ws or wss. The
// wizard stopped building it in v1.7.3 — Setup → Direct has made an [l3]
// tunnel since — so what remains are files written by hand or by a version
// older than that. The engine is gone; this table is not, for one reason: a
// file with a [direct] table and nothing else has to be recognised as one.
//
// Without it the decoder would drop the table without a word (unknown tables
// are not an error), and the file would read as a config with no tunnel in it
// at all. Starting it would fail with "neither server nor client configuration
// is properly set", and every management screen would skip it, so the tunnel
// would vanish from the menus while its service was still installed — with
// nothing anywhere saying why. Keeping the few keys a screen shows means the
// tunnel is still listed, still deletable, and refused with ErrDirectRemoved,
// which says what happened and what to build instead.
//
// Every other key the table used to take is ignored.
type LegacyDirectConfig struct {
	// Role is "iran" or "kharej" ("edge" and "origin" were synonyms). Empty
	// means the file has no [direct] table, which is every file but these.
	Role string `toml:"role"`
	// Addr is the kharej server's host:port on Iran, or the bind address on
	// kharej — shown on the tunnel list so the operator can tell which one it
	// was.
	Addr string `toml:"addr"`
	// Transport was tcp, stealth, ws or wss.
	Transport string `toml:"transport"`
	// Ports are the forwarded ports the Iran side exposed, kept so the list
	// shows what the rebuilt tunnel has to forward.
	Ports []string `toml:"ports"`
}

// Present reports whether the file has a [direct] table. It is the only
// question anything asks of one now.
func (d LegacyDirectConfig) Present() bool {
	return strings.TrimSpace(d.Role) != ""
}

// OnIran reports whether the table was the Iran side — the one that held the
// forwarded ports.
func (d LegacyDirectConfig) OnIran() bool {
	switch strings.ToLower(strings.TrimSpace(d.Role)) {
	case "kharej", "origin":
		return false
	}
	return true
}

// ErrDirectRemoved is what a [direct] tunnel is refused with: at start, on a
// reload, by `backpack check`, and on every screen that would otherwise offer
// to run or edit it. One sentence, so the engine's log line and the menus say
// the same thing.
var ErrDirectRemoved = errors.New("the [direct] stream tunnel (tcp, stealth, ws, wss) was removed " +
	"in Backpack v2.0.0 and this config can no longer run. Delete it and build the tunnel " +
	"again on both servers with sudo backpack → Setup Iran / Setup Kharej → Direct, which " +
	"makes a layer-3 tunnel forwarding the same ports")

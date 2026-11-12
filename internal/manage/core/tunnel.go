package core

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/quota"
	"github.com/backpack/backpack/internal/tunnel/l3"
	"github.com/backpack/backpack/internal/utils/network"
)

// Tunnel is a discovered tunnel derived from a config file on disk.
type Tunnel struct {
	Name      string
	Role      string // "server" or "client"
	Transport string
	Addr      string   // bind_addr (server) or remote_addr (client)
	Ports     []string // server only
	Service   string

	// Unrunnable says why this tunnel's config can no longer start, in the
	// engine's own words; empty when it can. Set for the two kinds v2.0.0
	// took out — a [direct] stream tunnel, and an [l3] tunnel on the udp or
	// quic carrier (or on none, which meant udp). Such a tunnel is still
	// listed, so it can be seen, fixed or deleted: dropping it from the list
	// would leave an installed service that no screen shows.
	Unrunnable string
}

// List scans the config directory and returns all tunnels, sorted by name.
func List() []Tunnel {
	var tunnels []Tunnel
	matches, _ := filepath.Glob(app.ConfigDir + "/*.toml")
	for _, path := range matches {
		var cfg config.Config
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(path), ".toml")
		t, ok := tunnelOf(name, cfg)
		if !ok {
			continue
		}
		tunnels = append(tunnels, t)
	}
	sort.Slice(tunnels, func(i, j int) bool { return tunnels[i].Name < tunnels[j].Name })
	return tunnels
}

// tunnelOf is what List makes of one parsed config, with no filesystem in it,
// so a test can hand it a file directly. false means the file describes no
// tunnel this machine lists.
func tunnelOf(name string, cfg config.Config) (Tunnel, bool) {
	t := Tunnel{Name: name, Service: app.ServiceName(name)}
	switch {
	case cfg.Server.BindAddr != "":
		t.Role = "server"
		t.Transport = string(cfg.Server.Transport)
		t.Addr = cfg.Server.BindAddr
		t.Ports = cfg.Server.Ports
	case cfg.Client.RemoteAddr != "":
		t.Role = "client"
		t.Transport = string(cfg.Client.Transport)
		t.Addr = cfg.Client.RemoteAddr
	// A direct tunnel is listed on the same terms as a reverse tunnel, so it
	// can be started, stopped, watched and deleted from the same menu.
	// Without this it would run perfectly well and be invisible to every
	// management screen, which is worse than not working: nothing would say
	// why. The same holds for the two kinds that can no longer run — they
	// are listed with the reason, so they can still be fixed or deleted.
	case cfg.L3.Enabled():
		t.Role = L3Role(cfg.L3.Mode)
		t.Transport = "l3/" + OrDefault(cfg.L3.Carrier, "udp")
		t.Addr = cfg.L3.Addr
		t.Ports = cfg.L3.Ports
		if l3.RemovedCarrier(cfg.L3.Carrier) {
			t.Unrunnable = strings.TrimPrefix(l3.RemovedCarrierError(cfg.L3.Carrier).Error(), "l3: ")
		}
	// After [l3], in the order the engine picks them.
	case cfg.Direct.Present():
		t.Role = "iran"
		if !cfg.Direct.OnIran() {
			t.Role = "kharej"
		}
		t.Transport = "direct/" + OrDefault(cfg.Direct.Transport, "tcp")
		t.Addr = cfg.Direct.Addr
		t.Ports = cfg.Direct.Ports
		t.Unrunnable = config.ErrDirectRemoved.Error()
	default:
		return t, false
	}
	return t, true
}

// LoadTunnelConfig reads a tunnel's full config file. Callers that need more
// than the summary List gives — preset, limits, certificate, fallbacks — read
// it through this rather than parsing the TOML themselves.
func LoadTunnelConfig(name string) (config.Config, error) {
	var cfg config.Config
	// The other half of the guard in LoadSpec: this is the direct tunnel's
	// reader, reached the same way from the same two places.
	if err := CheckName(name); err != nil {
		return cfg, err
	}
	_, err := toml.DecodeFile(app.ConfigPath(name), &cfg)
	return cfg, err
}

// Delete removes a tunnel: stops/disables the service, deletes the unit,
// config, any per-tunnel refresh script, and reloads systemd. Its traffic
// moves to the server's ledger (see metrics.Retire).
func Delete(name string) error {
	if err := CheckName(name); err != nil {
		return err
	}
	cfg, cfgErr := LoadTunnelConfig(name)
	service := app.ServiceName(name)
	if IsActive(service) || IsEnabled(service) {
		if err := DisableService(service); err != nil {
			return err
		}
	}
	if cfgErr == nil {
		network.CleanupPckGuards(cfg.Server.Token, cfg.Client.Token, cfg.L3.Token)
		// A GRE engine takes its link down when it stops; one that was killed
		// could not, and the name would then refuse the next tunnel given it.
		if cfg.L3.Enabled() && l3.KernelCarrier(cfg.L3.Carrier) {
			l3.CleanupGRE(cfg.L3.Iface, strings.ToLower(strings.TrimSpace(cfg.L3.Carrier)), cfg.L3.Addr)
		}
	}
	removeUnit(name)
	removeScheduledRestart(name)
	os.Remove(app.ConfigPath(name))
	// What it carried stays in the server's total, and its own counters go
	// with it: the service is stopped by now, so its last write is in.
	_ = metrics.Retire(app.ConfigDir, name)
	// Its traffic limit goes with it: a new tunnel given the same name starts
	// with no limit and a count of zero, not with the old one's.
	_ = quota.Remove(app.ConfigDir, name)
	deleteTunnelMeta(name)
	// The other end, if it was on a managed server, is left running there —
	// there is deliberately no operation that removes a tunnel on a node, and a
	// delete on this machine is not consent to one on another. What goes is
	// only the record that the two were a pair.
	//
	// A hook rather than a call: the pairing record belongs to the layer above
	// this one, and reaching up for it is what would make this package depend
	// on the package that depends on it.
	for _, f := range onDelete {
		f(name)
	}
	return DaemonReload()
}

// RestartAll restarts every discovered tunnel service and returns how many
// were restarted and how many failed.
func RestartAll() (ok, failed int) {
	for _, t := range List() {
		if err := RestartService(t.Service); err != nil {
			failed++
		} else {
			ok++
		}
	}
	return ok, failed
}

// Find returns one tunnel by name.
func Find(name string) (Tunnel, bool) {
	for _, t := range List() {
		if t.Name == name {
			return t, true
		}
	}
	return Tunnel{}, false
}

// onDelete are the cleanups the layers above this one register for a tunnel
// that is going away. See Delete.
var onDelete []func(name string)

// OnDelete registers a cleanup. Called at init time by the package that owns
// the state being cleaned up, so ordering never depends on what ran first.
func OnDelete(f func(name string)) { onDelete = append(onDelete, f) }

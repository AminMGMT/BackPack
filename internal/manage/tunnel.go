package manage

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
)

// Tunnel is a discovered tunnel derived from a config file on disk.
type Tunnel struct {
	Name      string
	Role      string // "server" or "client"
	Transport string
	Addr      string   // bind_addr (server) or remote_addr (client)
	Ports     []string // server only
	Service   string
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
		// The two direct kinds are listed on the same terms as a reverse
		// tunnel, so they can be started, stopped, watched and deleted from
		// the same menu. Without these they would run perfectly well and be
		// invisible to every management screen, which is worse than not
		// working: nothing would say why.
		case cfg.Direct.Enabled():
			t.Role = directRole(cfg.Direct.ResolvedRole())
			t.Transport = "direct/" + orDefault(cfg.Direct.Transport, "tcp")
			t.Addr = cfg.Direct.Addr
			t.Ports = cfg.Direct.Ports
		case cfg.L3.Enabled():
			t.Role = l3Role(cfg.L3.Mode)
			t.Transport = "l3/" + orDefault(cfg.L3.Carrier, "udp")
			t.Addr = cfg.L3.Addr
			t.Ports = cfg.L3.Ports
		default:
			continue
		}
		tunnels = append(tunnels, t)
	}
	sort.Slice(tunnels, func(i, j int) bool { return tunnels[i].Name < tunnels[j].Name })
	return tunnels
}

// LoadTunnelConfig reads a tunnel's full config file. Callers that need more
// than the summary List gives — preset, limits, certificate, fallbacks — read
// it through this rather than parsing the TOML themselves.
func LoadTunnelConfig(name string) (config.Config, error) {
	var cfg config.Config
	_, err := toml.DecodeFile(app.ConfigPath(name), &cfg)
	return cfg, err
}

// Delete removes a tunnel: stops/disables the service, deletes the unit,
// config, any per-tunnel refresh script, and reloads systemd.
func Delete(name string) error {
	service := app.ServiceName(name)
	if IsActive(service) || IsEnabled(service) {
		_ = DisableService(service)
	}
	removeUnit(name)
	os.Remove(app.ConfigPath(name))
	deleteTunnelMeta(name)
	// The other end, if it was on a managed server, is left running there —
	// there is deliberately no operation that removes a tunnel on a node, and a
	// delete on this machine is not consent to one on another. What goes is
	// only the record that the two were a pair.
	_ = ForgetNodePair(name)
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

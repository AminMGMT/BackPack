package manage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/engine"
	"github.com/backpack/backpack/internal/instanceid"
)

// Tunnel is a discovered tunnel derived from a config file on disk.
type Tunnel struct {
	Name      string
	Mode      string // "reverse" or "direct"; derived from engine metadata
	Engine    string // effective engine, including the legacy reverse default
	Role      string // "server" or "client"
	Transport string
	Addr      string   // bind_addr (server) or remote_addr (client)
	Ports     []string // server only
	Mappings  []config.ForwardMapping
	Service   string
}

// List scans the config directory and returns all tunnels, sorted by name.
func List() []Tunnel {
	var tunnels []Tunnel
	matches, _ := filepath.Glob(app.ConfigDir + "/*.toml")
	for _, path := range matches {
		cfg, err := config.LoadFile(path)
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(path), ".toml")
		meta, err := engine.MetadataFor(cfg)
		if err != nil {
			continue
		}
		t := Tunnel{Name: name, Service: app.ServiceName(name), Mode: meta.Mode, Engine: meta.Name}
		switch {
		case cfg.EffectiveEngine() == config.EngineIPTables:
			t.Mappings = append([]config.ForwardMapping(nil), cfg.Forward.Mappings...)
		case cfg.Server.BindAddr != "":
			t.Role = "server"
			t.Transport = string(cfg.Server.Transport)
			t.Addr = cfg.Server.BindAddr
			t.Ports = cfg.Server.Ports
		case cfg.Client.RemoteAddr != "":
			t.Role = "client"
			t.Transport = string(cfg.Client.Transport)
			t.Addr = cfg.Client.RemoteAddr
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
	cfg, err := config.LoadFile(app.ConfigPath(name))
	if err != nil {
		return config.Config{}, err
	}
	return *cfg, nil
}

// Delete removes a tunnel: stops/disables the service, deletes the unit,
// config, any per-tunnel refresh script, and reloads systemd.
func Delete(name string) error {
	service := app.ServiceName(name)
	cfg, cfgErr := config.LoadFile(app.ConfigPath(name))
	if IsActive(service) || IsEnabled(service) {
		_ = DisableService(service)
	}
	if cfgErr == nil {
		if p, err := engine.Resolve(cfg); err == nil {
			if err = p.Cleanup(context.Background(), engine.Request{ConfigPath: app.ConfigPath(name), Config: cfg}); err != nil {
				return fmt.Errorf("cleanup %s before delete: %w", name, err)
			}
		}
	} else if _, identityErr := os.Stat(instanceid.Path(app.ConfigPath(name))); identityErr == nil {
		// A direct config may be corrupt or already missing while its generation
		// is still live. Persistent identity metadata is sufficient for the
		// engine's ownership-safe cleanup path.
		p, err := engine.Get(config.EngineIPTables)
		if err == nil {
			err = p.Cleanup(context.Background(), engine.Request{ConfigPath: app.ConfigPath(name)})
		}
		if err != nil {
			return fmt.Errorf("cleanup unreadable direct instance %s: %w", name, err)
		}
	}
	id, _ := instanceid.Resolve(app.ConfigPath(name), false)
	removeUnit(name)
	os.Remove(app.ConfigPath(name))
	os.Remove(filepath.Join(app.ConfigDir, name+".metrics.json"))
	os.Remove(instanceid.Path(app.ConfigPath(name)))
	if id.InstanceID != "" {
		os.Remove(filepath.Join(app.ConfigDir, "forward-state", id.InstanceID+".json"))
	}
	deleteTunnelMeta(name)
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

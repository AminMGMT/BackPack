package core

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/backpack/backpack/internal/app"
)

// tunnelMeta holds extra per-tunnel info that isn't part of the engine config
// (e.g. a country label the user picks in the web UI to find nodes easily).
type tunnelMeta struct {
	Country string `json:"country"` // ISO 3166-1 alpha-2 code, uppercase
	// LinkHosts are this server's other addresses — another IP, a domain, a
	// CDN edge — that the setup link hands to the far end as backups to the
	// main one. Only a side the far end dials has them.
	LinkHosts []string `json:"link_hosts,omitempty"`
}

var metaMu sync.Mutex

func metaPath() string { return app.ConfigDir + "/meta.json" }

func loadMeta() map[string]tunnelMeta {
	m := map[string]tunnelMeta{}
	app.WarnState(app.LoadState(metaPath(), &m))
	return m
}

// saveMeta is best effort: the metadata is a display nicety, and a tunnel is
// deleted whether or not its entry could be dropped. It is written atomically
// so a crash mid-write cannot leave a file that loads as empty.
func saveMeta(m map[string]tunnelMeta) {
	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		return
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	_ = app.WriteFileAtomic(metaPath(), data, 0644)
}

// TunnelCountry returns the recorded country code for a tunnel, or "".
func TunnelCountry(name string) string {
	metaMu.Lock()
	defer metaMu.Unlock()
	return loadMeta()[name].Country
}

// deleteTunnelMeta drops a tunnel's metadata (called on delete).
func deleteTunnelMeta(name string) {
	metaMu.Lock()
	defer metaMu.Unlock()
	m := loadMeta()
	delete(m, name)
	saveMeta(m)
}

// LinkHosts returns the backup addresses the setup link carries for a tunnel.
func LinkHosts(name string) []string {
	metaMu.Lock()
	defer metaMu.Unlock()
	return loadMeta()[name].LinkHosts
}

// SetLinkHosts records them.
func SetLinkHosts(name string, hosts []string) {
	metaMu.Lock()
	defer metaMu.Unlock()
	m := loadMeta()
	e := m[name]
	e.LinkHosts = hosts
	m[name] = e
	saveMeta(m)
}

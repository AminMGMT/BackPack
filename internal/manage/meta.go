package manage

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
}

var metaMu sync.Mutex

func metaPath() string { return app.ConfigDir + "/meta.json" }

func loadMeta() map[string]tunnelMeta {
	m := map[string]tunnelMeta{}
	if data, err := os.ReadFile(metaPath()); err == nil {
		json.Unmarshal(data, &m)
	}
	return m
}

func saveMeta(m map[string]tunnelMeta) {
	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		return
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(metaPath(), data, 0644)
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

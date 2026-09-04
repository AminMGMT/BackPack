package webui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/manage"
)

// The direct form is the panel's half of the forged-source carrier. Until this,
// it could create a spoof tunnel and set exactly one thing about it — the peer
// address — while the CLI asked six questions. A form that offers a carrier and
// then cannot configure it is worse than one that does not offer it: the tunnel
// gets built on defaults nobody chose.

// What the edit sends must decode into DirectEdit, whose pointers are what let
// an absent key and a deliberate zero be told apart.
func TestDirectEditFieldsReachTheServer(t *testing.T) {
	payload := map[string]any{
		"ports": "443", "acceptUdp": true, "preset": "turbo",
		"mtu": 1400, "autoMtu": false, "maxConnections": 0, "bandwidthMbps": 0,
		"paths": 4, "fec": true, "stealth": true,
		"spoof": map[string]any{"profile": "icmp", "srcIPs": "203.0.113.10"},
	}
	raw, _ := json.Marshal(payload)
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manage.DirectEdit{}); err != nil {
		t.Errorf("the edit posts a field the server would refuse: %v", err)
	}
}

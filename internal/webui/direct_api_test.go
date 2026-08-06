package webui

import (
	"encoding/json"
	"testing"

	"github.com/backpack/backpack/config"
)

func TestTunnelInfoAdditiveEngineShapeIsStable(t *testing.T) {
	for _, info := range []TunnelInfo{
		{Name: "legacy", Mode: "reverse", Engine: "reverse", Role: "server", Transport: "tcp", Mappings: []config.ForwardMapping{}},
		{Name: "direct", Mode: "direct", Engine: "iptables", Role: "", Transport: "", Mappings: []config.ForwardMapping{}},
	} {
		b, err := json.Marshal(info)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"mode", "engine", "role", "transport", "mappings", "packetsIn", "packetsOut"} {
			if _, ok := got[field]; !ok {
				t.Errorf("%s response omitted stable field %q: %s", info.Name, field, b)
			}
		}
		if mappings, ok := got["mappings"].([]any); !ok || len(mappings) != 0 {
			t.Errorf("%s mappings must be an empty JSON array, got %#v", info.Name, got["mappings"])
		}
		if info.Mode == "direct" && (got["role"] != "" || got["transport"] != "") {
			t.Errorf("direct reverse-only fields must be stable empty strings: %s", b)
		}
	}
}

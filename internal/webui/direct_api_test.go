package webui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/backpack/backpack/config"
)

func TestTunnelInfoAdditiveEngineShapeIsStable(t *testing.T) {
	for _, info := range []TunnelInfo{
		{Name: "legacy", Mode: "reverse", Engine: "reverse", Role: "server", Transport: "tcp", Mappings: []config.ForwardMapping{}},
		{Name: "kernel-direct", Mode: "direct", Engine: "iptables", Role: "", Transport: "", Mappings: []config.ForwardMapping{}},
		{Name: "app-direct", Mode: "direct", Engine: "forward", Role: "server", Transport: "tcpmux", Mappings: []config.ForwardMapping{}},
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
		if info.Engine == "iptables" && (got["role"] != "" || got["transport"] != "") {
			t.Errorf("kernel-direct reverse-only fields must be stable empty strings: %s", b)
		}
		if info.Engine == "forward" && (got["role"] == "" || got["transport"] == "") {
			t.Errorf("application Direct must retain its selected transport: %s", b)
		}
	}
}

func TestDashboardSeparatesApplicationDirectFromKernelDirect(t *testing.T) {
	body := string(dashboardHTML)
	for _, marker := range []string{
		"kernelDirect=t.engine==='iptables'",
		"appDirect=t.engine==='forward'",
		"appDirect?'DIRECT/'+(t.transport||'')",
		"appDirect&&t.role!=='server'?T('Iran → Kharej')",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("dashboard is missing Direct-mode distinction %q", marker)
		}
	}
	if strings.Contains(body, "t.mode==='direct'?'Engine'") {
		t.Fatal("dashboard still renders every Direct instance as the iptables engine")
	}
}

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLegacyEngineDefaultsToReverse(t *testing.T) {
	c, err := LoadFile(writeConfig(t, "[server]\nbind_addr=':1'\ntransport='tcp'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.EffectiveEngine(); got != EngineReverse {
		t.Fatalf("engine=%q", got)
	}
}

func TestEngineSectionMatrix(t *testing.T) {
	cases := []string{
		"[server]\nbind_addr=':1'\n[client]\nremote_addr='x:1'\n",
		"[server]\n[client]\n",
		"[forward]\n[[forward.mappings]]\nlisten_address='0.0.0.0'\nlisten_ports='1'\ntarget_address='192.0.2.1'\ntarget_ports='1'\nprotocols=['tcp']\n",
		"engine='iptables'\n[server]\nbind_addr=':1'\n",
		"engine='iptables'\n",
		"engine='iptables'\n[forward]\n[server]\nbind_addr=':1'\n",
		"engine='reverse'\n[forward]\n",
		"engine='unknown'\n[client]\nremote_addr='x:1'\n",
	}
	for _, body := range cases {
		if _, err := LoadFile(writeConfig(t, body)); err == nil {
			t.Errorf("accepted invalid config:\n%s", body)
		}
	}
}

func TestExplicitReverseAndLegacyClientRemainValid(t *testing.T) {
	for _, body := range []string{
		"engine='reverse'\n[server]\nbind_addr=':1'\ntransport='tcp'\n",
		"[client]\nremote_addr='192.0.2.1:1'\ntransport='tcp'\n",
	} {
		cfg, err := LoadFile(writeConfig(t, body))
		if err != nil {
			t.Fatalf("valid reverse config rejected: %v\n%s", err, body)
		}
		if cfg.EffectiveEngine() != EngineReverse || cfg.HasForward() {
			t.Fatalf("legacy meaning changed: %#v", cfg)
		}
	}
}

func TestForwardValidation(t *testing.T) {
	body := "engine='iptables'\n[forward]\n[[forward.mappings]]\nlisten_address='0.0.0.0'\nlisten_ports='1000-1002'\ntarget_address='192.0.2.10'\ntarget_ports='2000-2002'\nprotocols=['tcp','udp']\n"
	if _, err := LoadFile(writeConfig(t, body)); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(body, "2000-2002", "2000-2003", 1)
	if _, err := LoadFile(writeConfig(t, bad)); err == nil || !strings.Contains(err.Error(), "same number") {
		t.Fatalf("range error=%v", err)
	}
}

func TestForwardOverlapAndLimit(t *testing.T) {
	f := ForwardConfig{Mappings: []ForwardMapping{
		{ListenAddress: "0.0.0.0", ListenPorts: "100-200", TargetAddress: "192.0.2.1", TargetPorts: "100-200", Protocols: []string{"tcp"}},
		{ListenAddress: "192.0.2.5", ListenPorts: "150", TargetAddress: "192.0.2.2", TargetPorts: "150", Protocols: []string{"tcp"}},
	}}
	if err := ValidateForward(f); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlap error=%v", err)
	}
	f.Mappings = []ForwardMapping{{ListenAddress: "::", ListenPorts: "1-1025", TargetAddress: "2001:db8::1", TargetPorts: "1-1025", Protocols: []string{"udp"}}}
	if err := ValidateForward(f); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("limit error=%v", err)
	}
	f.Mappings = nil
	for i := 0; i < 5; i++ {
		lo, hi := i*1000+1, (i+1)*1000
		f.Mappings = append(f.Mappings, ForwardMapping{
			ListenAddress: "0.0.0.0", ListenPorts: fmt.Sprintf("%d-%d", lo, hi),
			TargetAddress: "192.0.2.1", TargetPorts: fmt.Sprintf("%d-%d", lo, hi), Protocols: []string{"udp"},
		})
	}
	if err := ValidateForward(f); err == nil || !strings.Contains(err.Error(), "maximum is 4096") {
		t.Fatalf("instance expansion limit error=%v", err)
	}
}

func TestForwardRejectsInvalidTargetsAndProtocols(t *testing.T) {
	base := ForwardMapping{ListenAddress: "0.0.0.0", ListenPorts: "80", TargetAddress: "192.0.2.1", TargetPorts: "8080", Protocols: []string{"tcp"}}
	for name, mutate := range map[string]func(*ForwardMapping){
		"domain":      func(m *ForwardMapping) { m.TargetAddress = "example.com" },
		"loopback":    func(m *ForwardMapping) { m.TargetAddress = "127.0.0.1" },
		"multicast":   func(m *ForwardMapping) { m.TargetAddress = "224.0.0.1" },
		"unspecified": func(m *ForwardMapping) { m.TargetAddress = "0.0.0.0" },
		"mixed-family": func(m *ForwardMapping) {
			m.TargetAddress = "2001:db8::1"
		},
		"protocol": func(m *ForwardMapping) { m.Protocols = []string{"sctp"} },
		"empty-protocol": func(m *ForwardMapping) {
			m.Protocols = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := base
			mutate(&m)
			if err := ValidateForward(ForwardConfig{Mappings: []ForwardMapping{m}}); err == nil {
				t.Fatalf("invalid mapping accepted: %#v", m)
			}
		})
	}
}

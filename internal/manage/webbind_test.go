package manage

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/backpack/backpack/config"
)

// The knob has to survive the CLI, or it does not exist. Editing a tunnel
// re-renders its whole config file from the spec, so anything the renderer
// does not write is silently dropped the next time anyone touches the tunnel
// from the menu or the panel — which is how a hand-edited setting gets undone
// by a change to something unrelated.
func TestWebBindSurvivesARender(t *testing.T) {
	for _, role := range []string{"server", "client"} {
		t.Run(role, func(t *testing.T) {
			spec := specWithMonitor(role)
			spec.WebBind = "0.0.0.0"

			var cfg config.Config
			if _, err := toml.Decode(spec.Render(), &cfg); err != nil {
				t.Fatalf("the rendered config does not parse: %v\n%s", err, spec.Render())
			}

			got := cfg.Server.WebBind
			if role == "client" {
				got = cfg.Client.WebBind
			}
			if got != "0.0.0.0" {
				t.Fatalf("web_bind survived the render as %q, want 0.0.0.0", got)
			}
		})
	}
}

// A spec that never mentions it is written out as loopback rather than left
// blank, so the setting is visible in the file to whoever goes looking.
func TestUnsetWebBindIsWrittenAsLoopback(t *testing.T) {
	for _, role := range []string{"server", "client"} {
		t.Run(role, func(t *testing.T) {
			rendered := specWithMonitor(role).Render()
			if !strings.Contains(rendered, `web_bind = "127.0.0.1"`) {
				t.Fatalf("a config with a monitor port did not name its bind:\n%s", rendered)
			}
		})
	}
}

// No monitor, nothing to bind: the key must not appear at all.
func TestNoMonitorPortWritesNoWebBind(t *testing.T) {
	spec := specWithMonitor("server")
	spec.WebPort = 0
	if rendered := spec.Render(); strings.Contains(rendered, "web_bind") {
		t.Fatalf("a config with no monitor port still named a bind:\n%s", rendered)
	}
}

func specWithMonitor(role string) TunnelSpec {
	s := TunnelSpec{
		Name:      "t",
		Role:      role,
		Transport: "tcp",
		Token:     "secret",
		WebPort:   2060,
		Ports:     []string{"443=127.0.0.1:8443"},
	}
	if role == "server" {
		s.BindAddr = "0.0.0.0:3080"
	} else {
		s.RemoteAddr = "example.com:3080"
	}
	return s
}

package webui

import "testing"

// The panel can serve HTTPS, and the choice has to survive an upgrade without
// moving anyone's bookmark.

// Plain HTTP is the default and stays the default: a config written before any
// of this existed has no https key at all, and a panel that quietly switched
// scheme on update would be unreachable at the address people use.
func TestPanelStaysOnPlainHTTPUnlessAsked(t *testing.T) {
	var c Config // what an older config decodes to
	if c.HTTPS {
		t.Error("HTTPS is on by default; an upgrade would move the panel's address")
	}
	if got := c.Scheme(); got != "http" {
		t.Errorf("scheme = %q, want http", got)
	}
}

func TestSchemeFollowsTheSetting(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"off", Config{}, "http"},
		{"self-signed", Config{HTTPS: true}, "https"},
		{"lets encrypt", Config{HTTPS: true, TLSDomain: "panel.example.com"}, "https"},
		// A domain without HTTPS is not a half-on state: the switch is what
		// decides, so a leftover domain cannot silently turn TLS on.
		{"domain but disabled", Config{TLSDomain: "panel.example.com"}, "http"},
	} {
		if got := tc.cfg.Scheme(); got != tc.want {
			t.Errorf("%s: scheme = %q, want %q", tc.name, got, tc.want)
		}
	}
}

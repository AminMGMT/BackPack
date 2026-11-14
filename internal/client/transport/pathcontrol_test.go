package transport

import "testing"

// The path control (window that follows the queue, pace, retransmit margin) is
// on for every carrier of the KCP family: pck, and since the soak runs of
// 2026-11-14, kcp and xdi too — with it a reverse kcp or xdi tunnel under load
// went from 31-40% of new connections made and a 750 ms probe to 100% and
// 80 ms. Its ceiling is the preset's window.
func TestEveryKCPCarrierGetsThePathControl(t *testing.T) {
	base := KcpConfig{MTU: 1250, Interval: 10, SndWnd: 1024, RcvWnd: 1024}
	for _, c := range []struct {
		name     string
		set      func(*KcpConfig)
		wantPath bool
		wantWnd  int
	}{
		{"kcp", func(*KcpConfig) {}, true, 1024},
		{"xdi", func(c *KcpConfig) { c.UseICMP = true }, true, 1024},
		{"pck", func(c *KcpConfig) { c.UsePck = true }, true, 1024},
	} {
		cfg := base
		c.set(&cfg)
		s := cfg.settings()
		if (s.Path != nil) != c.wantPath {
			t.Errorf("%s: Path set = %v, want %v", c.name, s.Path != nil, c.wantPath)
			continue
		}
		if c.wantPath && s.Path.Ceiling() != c.wantWnd {
			t.Errorf("%s: the controller's ceiling is %d, want the preset's %d", c.name, s.Path.Ceiling(), c.wantWnd)
		}
	}
}

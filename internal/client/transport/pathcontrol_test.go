package transport

import "testing"

// The path control (window that follows the queue, pace, retransmit margin) is
// the pck transport's alone. kcp and xdi share every line of the session code,
// and a change that reached them through it would alter tunnels nobody
// measured, so each says here what it gets.
func TestOnlyPckGetsThePathControl(t *testing.T) {
	base := KcpConfig{MTU: 1250, Interval: 10, SndWnd: 1024, RcvWnd: 1024}
	for _, c := range []struct {
		name     string
		set      func(*KcpConfig)
		wantPath bool
		wantWnd  int
	}{
		{"kcp", func(*KcpConfig) {}, false, 0},
		{"xdi", func(c *KcpConfig) { c.UseICMP = true }, false, 0},
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

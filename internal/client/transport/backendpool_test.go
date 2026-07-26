package transport

import (
	"sync"
	"testing"
	"time"
)

// A single backend is a passthrough: no pipe, no health-checking, unchanged.
func TestBackendPoolSinglePassthrough(t *testing.T) {
	p := &backendPool{groups: map[string]*backendGroup{}}
	if got := p.pick("127.0.0.1:8443"); got != "127.0.0.1:8443" {
		t.Fatalf("single backend = %q, want unchanged", got)
	}
	if len(p.groups) != 0 {
		t.Fatal("a single backend must not create a health-checked group")
	}
}

// Round-robin spreads over the healthy backends and skips a dropped one.
func TestBackendGroupPickSkipsUnhealthy(t *testing.T) {
	g := &backendGroup{
		list:    []string{"a:1", "b:2", "c:3"},
		healthy: []bool{true, false, true}, // b is down
		fails:   make([]int, 3),
	}
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		seen[g.pick()]++
	}
	if seen["b:2"] != 0 {
		t.Fatalf("picked the unhealthy backend: %v", seen)
	}
	if seen["a:1"] == 0 || seen["c:3"] == 0 {
		t.Fatalf("did not spread over the healthy backends: %v", seen)
	}
}

// The health checker drops a backend after MaxFailed and restores it on recovery.
func TestBackendHealthCheckDropsAndRestores(t *testing.T) {
	// Substitute the probe: "down:0" always fails, everything else succeeds.
	var mu sync.Mutex
	downFails := 0
	orig := backendProbe
	backendProbe = func(addr string) bool {
		mu.Lock()
		defer mu.Unlock()
		if addr == "down:0" {
			downFails++
			return false
		}
		return true
	}
	defer func() { backendProbe = orig }()

	g := &backendGroup{
		list:    []string{"up:1", "down:0"},
		healthy: []bool{true, true},
		fails:   make([]int, 2),
	}
	// Run enough probe rounds by hand to cross the failure threshold.
	for i := 0; i < backendMaxFailed; i++ {
		for j, b := range g.list {
			ok := backendProbe(b)
			g.mu.Lock()
			if ok {
				g.healthy[j], g.fails[j] = true, 0
			} else {
				g.fails[j]++
				if g.fails[j] >= backendMaxFailed {
					g.healthy[j] = false
				}
			}
			g.mu.Unlock()
		}
	}
	g.mu.Lock()
	dropped := !g.healthy[1] && g.healthy[0]
	g.mu.Unlock()
	if !dropped {
		t.Fatal("unhealthy backend was not dropped from rotation")
	}
	// pick must now only return the healthy one.
	for i := 0; i < 4; i++ {
		if got := g.pick(); got != "up:1" {
			t.Fatalf("pick = %q, want only the healthy up:1", got)
		}
	}
	_ = time.Now
}

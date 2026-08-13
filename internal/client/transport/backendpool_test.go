package transport

import (
	"fmt"
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
		// Recent enough that pick() does not start a real probe pass.
		lastCheck: time.Now(),
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
	// "down:0" always fails, everything else answers.
	probe := func(addr string) bool { return addr != "down:0" }

	g := &backendGroup{
		list:      []string{"up:1", "down:0"},
		healthy:   []bool{true, true},
		fails:     make([]int, 2),
		lastCheck: time.Now(),
	}
	// Run enough probe rounds to cross the failure threshold.
	for i := 0; i < backendMaxFailed; i++ {
		g.check(probe)
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

	// One answering pass puts it straight back in the rotation.
	g.check(func(string) bool { return true })
	g.mu.Lock()
	restored := g.healthy[1] && g.fails[1] == 0
	g.mu.Unlock()
	if !restored {
		t.Fatal("a recovered backend was not restored to the rotation")
	}
}

// A target with a pipe in it but nothing usable on either side of it used to
// build a group with an empty list, and the round-robin's `% 0` took the
// process down with it.
func TestBackendPoolSurvivesEmptyAndSingletonLists(t *testing.T) {
	p := &backendPool{groups: map[string]*backendGroup{}}

	if got := p.pick("||"); got != "||" {
		t.Fatalf("empty backend list = %q, want the target unchanged", got)
	}
	if got := p.pick(" 127.0.0.1:8443 | "); got != "127.0.0.1:8443" {
		t.Fatalf("one real backend = %q, want it trimmed and returned", got)
	}
	if len(p.groups) != 0 {
		t.Fatalf("nothing to balance created %d groups", len(p.groups))
	}
}

// Spacing and a trailing separator describe the same two backends, so they
// must share one group rather than each getting their own.
func TestBackendPoolKeysOnTheParsedList(t *testing.T) {
	p := &backendPool{groups: map[string]*backendGroup{}}
	p.pick("a:1|b:2")
	p.pick(" a:1 | b:2 |")
	if len(p.groups) != 1 {
		t.Fatalf("equivalent targets created %d groups, want 1", len(p.groups))
	}
}

// The targets these keys come from arrive over the tunnel, so the map has to
// stop growing at some point rather than trusting the far end.
func TestBackendPoolGroupCacheIsBounded(t *testing.T) {
	p := &backendPool{groups: map[string]*backendGroup{}}
	for i := 0; i < backendMaxGroups+50; i++ {
		list := []string{fmt.Sprintf("a-%d:1", i), fmt.Sprintf("b-%d:2", i)}
		p.group(fmt.Sprintf("group-%d", i), list)
	}
	if len(p.groups) != backendMaxGroups {
		t.Fatalf("group cache holds %d entries, want at most %d", len(p.groups), backendMaxGroups)
	}
}

// A list longer than the member bound is truncated rather than allocated in
// full — and the group it builds is still a working rotation.
func TestBackendPoolBoundsMembersPerGroup(t *testing.T) {
	p := &backendPool{groups: map[string]*backendGroup{}}
	target := ""
	for i := 0; i < backendMaxMembers+20; i++ {
		if i > 0 {
			target += backendSep
		}
		target += fmt.Sprintf("host-%d:1", i)
	}
	if got := p.pick(target); got == "" {
		t.Fatal("an over-long backend list produced no backend at all")
	}
	if len(p.groups) != 1 {
		t.Fatalf("group count = %d, want 1", len(p.groups))
	}
	for _, g := range p.groups {
		if len(g.list) != backendMaxMembers {
			t.Fatalf("group holds %d members, want %d", len(g.list), backendMaxMembers)
		}
	}
}

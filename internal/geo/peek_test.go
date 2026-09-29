package geo

import (
	"sync/atomic"
	"testing"
	"time"
)

// On a server that cannot reach the providers, every lookup used to ask all of
// them again and wait out their timeouts — on every call. A miss is remembered
// now, and Peek never waits at all.
func TestAMissIsRememberedAndPeekNeverWaits(t *testing.T) {
	var asked atomic.Int32
	slow := func(string) *Info { asked.Add(1); time.Sleep(300 * time.Millisecond); return nil }
	defer func(p []func(string) *Info) { geoProviders = p }(geoProviders)
	geoProviders = []func(string) *Info{slow}

	start := time.Now()
	if g := Peek("198.51.100.7"); g != nil {
		t.Fatalf("Peek answered %v with nothing known", g)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("Peek waited %s on the network", d)
	}
	time.Sleep(400 * time.Millisecond) // the background lookup finishes

	start = time.Now()
	if g := Lookup("198.51.100.7"); g != nil || time.Since(start) > 50*time.Millisecond {
		t.Fatalf("a remembered miss was asked again (%s)", time.Since(start))
	}
	Peek("198.51.100.7")
	if n := asked.Load(); n != 1 {
		t.Errorf("the providers were asked %d times for one address", n)
	}

	found := func(string) *Info { return &Info{Country: "Germany", Code: "DE"} }
	geoProviders = []func(string) *Info{found}
	Peek("198.51.100.8")
	time.Sleep(50 * time.Millisecond)
	if g := Peek("198.51.100.8"); g == nil || g.Code != "DE" {
		t.Errorf("the background answer did not reach the cache: %v", g)
	}
}

package geo

import (
	"sync"
	"testing"
	"time"
)

// Lookup has to behave without a network, because that is the condition it was
// written for: every provider it tries may be blocked from the machine asking,
// which is the whole reason there are three of them.
//
// The contract callers depend on is that nil means "unavailable", never an
// error and never a panic. `internal/node` builds a fleet card from it and
// `internal/webui` draws a flag from it; both have to survive a lookup that
// answers nothing.

func TestAnEmptyOrUnknownAddressIsNotLookedUp(t *testing.T) {
	defer clearCache()
	for _, in := range []string{"", "-"} {
		if got := Lookup(in); got != nil {
			t.Errorf("Lookup(%q) = %+v, want nil — there is nothing to look up", in, got)
		}
	}
}

// A cached answer is returned without asking anyone. This is what keeps the
// fleet screen from making one request per card per refresh.
func TestACachedAnswerIsReusedWithoutAProvider(t *testing.T) {
	defer clearCache()

	const ip = "198.51.100.4"
	want := &Info{Country: "Nowhere", Code: "NW", City: "Nowhereville", ISP: "Example"}
	putCache(ip, want, time.Now())

	// No provider can succeed here — there is no network in a unit test — so
	// anything other than the cached value means the cache was not consulted.
	got := Lookup(ip)
	if got != want {
		t.Fatalf("Lookup returned %+v, want the cached entry", got)
	}
}

// An entry older than the cache window is not reused. Six hours is the window;
// a stale flag on a fleet screen is a small wrong answer, but a cache that
// never expires is a permanently wrong one.
func TestAStaleEntryIsNotReused(t *testing.T) {
	defer clearCache()

	const ip = "198.51.100.5"
	putCache(ip, &Info{Country: "Stale", Code: "ST"}, time.Now().Add(-7*time.Hour))

	// With no network the providers all fail, so a stale entry that was
	// correctly rejected produces nil rather than the old value.
	if got := Lookup(ip); got != nil && got.Country == "Stale" {
		t.Error("a seven-hour-old entry was served from a six-hour cache")
	}
}

// The cache is read and written from the panel, the bot and the node code at
// once. Run under -race this is the test that says so.
func TestTheCacheIsSafeUnderConcurrentLookups(t *testing.T) {
	defer clearCache()

	putCache("203.0.113.1", &Info{Country: "A", Code: "AA"}, time.Now())
	putCache("203.0.113.2", &Info{Country: "B", Code: "BB"}, time.Now())

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				Lookup("203.0.113.1")
			} else {
				Lookup("203.0.113.2")
			}
		}(i)
	}
	wg.Wait()
}

func putCache(ip string, info *Info, at time.Time) {
	geoMu.Lock()
	geoCache[ip] = geoEntry{info: info, at: at}
	geoMu.Unlock()
}

func clearCache() {
	geoMu.Lock()
	geoCache = map[string]geoEntry{}
	geoMu.Unlock()
}

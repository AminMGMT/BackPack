package web

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
)

func testUsage(t *testing.T) *Usage {
	t.Helper()
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return &Usage{
		logger:     logger,
		sniffer:    true,
		snifferLog: filepath.Join(t.TempDir(), "usage.json"),
	}
}

// The counter is written by the save loop and read by the stats endpoint —
// two goroutines, and it was a plain uint64 between them. Run under -race,
// this is the test that catches it.
func TestTrafficTotalIsSafeAcrossGoroutines(t *testing.T) {
	m := testUsage(t)

	var wg sync.WaitGroup
	for port := 0; port < 4; port++ {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				m.AddOrUpdatePort(8000+port, 64)
			}
		}(port)
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				m.saveUsageData()
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = m.totalTraffic.Load()
			}
		}()
	}
	wg.Wait()

	if got := m.totalTraffic.Load(); got == 0 {
		t.Fatal("nothing was accounted for at all")
	}
}

// Saving is a read-modify-write of one file. Two of them interleaving used to
// mean whichever finished last wrote the totals it had read before the other
// one started, discarding the difference — and a reader could catch the file
// mid-write and fail to parse it.
func TestUsageFileSurvivesConcurrentSavesAndReads(t *testing.T) {
	m := testUsage(t)
	for port := 0; port < 8; port++ {
		m.AddOrUpdatePort(9000+port, 1024)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				m.saveUsageData()
			}
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				m.getUsageFromFile()
			}
		}()
	}
	wg.Wait()

	// Whatever the interleaving, what is on disk has to be one valid document
	// naming every port exactly once.
	body, err := os.ReadFile(m.snifferLog)
	if err != nil {
		t.Fatalf("reading the usage file: %v", err)
	}
	var saved []PortUsage
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatalf("the usage file did not survive as valid JSON: %v\n%s", err, body)
	}
	seen := map[int]bool{}
	for _, u := range saved {
		if seen[u.Port] {
			t.Errorf("port %d appears more than once", u.Port)
		}
		seen[u.Port] = true
	}
	if len(seen) != 8 {
		t.Fatalf("the file names %d ports, want 8", len(seen))
	}
}

// The first read creates the file. It used to return without closing the
// handle it had just opened.
func TestFirstReadCreatesTheUsageFile(t *testing.T) {
	m := testUsage(t)

	if got := m.getUsageFromFile(); got != nil {
		t.Fatalf("a fresh install reported %v, want nothing", got)
	}
	body, err := os.ReadFile(m.snifferLog)
	if err != nil {
		t.Fatalf("the usage file was not created: %v", err)
	}
	if string(body) != "null" {
		t.Fatalf("the new usage file holds %q, want \"null\"", body)
	}
}

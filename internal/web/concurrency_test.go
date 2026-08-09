package web

import (
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestTunnelStatusIsSafeForConcurrentUpdates(t *testing.T) {
	m := &Usage{}
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for update := 0; update < 1000; update++ {
				m.SetTunnelStatus(fmt.Sprintf("%d-%d", worker, update))
				_ = m.loadTunnelStatus()
			}
		}(worker)
	}
	workers.Wait()
	if m.loadTunnelStatus() == "" {
		t.Fatal("concurrent status updates were lost")
	}
}

func TestUsageFileReadsAndWritesAreSerialized(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	m := &Usage{snifferLog: filepath.Join(t.TempDir(), "usage.json"), logger: logger}

	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for update := 0; update < 25; update++ {
				m.AddOrUpdatePort(1000+worker, 1)
				m.saveUsageData()
				_ = m.getUsageFromFile()
			}
		}(worker)
	}
	workers.Wait()
	if got := m.getUsageFromFile(); len(got) == 0 {
		t.Fatal("serialized usage file lost all records")
	}
}

func TestCachedStatsKeepLiveTunnelFields(t *testing.T) {
	m := &Usage{
		cachedStats: &SystemStats{CPUUsage: "cached", TunnelStatus: "stale", TunnelTraffic: "stale"},
		statsAt:     time.Now(),
	}
	m.SetTunnelStatus("Connected (TCP)")
	m.totalTraffic.Store(2048)

	stats, err := m.getSystemStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.CPUUsage != "cached" || stats.TunnelStatus != "Connected (TCP)" || stats.TunnelTraffic != "2.00 KB" {
		t.Fatalf("unexpected cached snapshot: %+v", stats)
	}
	if stats == m.cachedStats {
		t.Fatal("cached stats escaped without a defensive copy")
	}
}

func TestCounterDeltaHandlesCounterReset(t *testing.T) {
	if got := counterDelta(125, 100); got != 25 {
		t.Fatalf("counter delta = %d, want 25", got)
	}
	if got := counterDelta(5, 100); got != 0 {
		t.Fatalf("reset counter delta = %d, want 0", got)
	}
}

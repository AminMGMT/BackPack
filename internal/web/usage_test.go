package web

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
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
	m.saveUsageData()
	if got, want := m.totalTraffic.Load(), uint64(4*200*64); got != want {
		t.Fatalf("concurrent additions and saves recorded %d bytes, want %d", got, want)
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

// A failed save must retain the drained snapshot, including when writers add
// more traffic before the next attempt succeeds.
func TestFailedUsageSavePreservesPendingTraffic(t *testing.T) {
	m := testUsage(t)
	m.snifferLog = filepath.Join(t.TempDir(), "missing", "usage.json")
	m.AddOrUpdatePort(8080, 4096)
	m.saveUsageData()
	if got := m.totalTraffic.Load(); got != 0 {
		t.Errorf("failed save published uncommitted total %d", got)
	}
	m.AddOrUpdatePort(8080, 1024)
	if err := os.MkdirAll(filepath.Dir(m.snifferLog), 0755); err != nil {
		t.Fatal(err)
	}
	m.saveUsageData()
	if got := m.totalTraffic.Load(); got != 5120 {
		t.Fatalf("retry recorded %d bytes, want 5120", got)
	}
	m.saveUsageData()
	if got := m.totalTraffic.Load(); got != 5120 {
		t.Fatalf("next save counted the snapshot twice: %d", got)
	}
}

// Saving must not delete a counter a writer already holds. Every addition is
// assigned to either this snapshot or the next, with no gaps or duplicates.
func TestUsageSnapshotsPreserveConcurrentSamePortWriters(t *testing.T) {
	m := testUsage(t)
	var wg sync.WaitGroup
	const writers, additions = 8, 10000
	var finished atomic.Bool
	collected := make(chan uint64, 1)
	go func() {
		var sum uint64
		for !finished.Load() {
			for _, usage := range m.collectUsageDataFromSyncMap() {
				sum += usage.Usage
			}
		}
		for _, usage := range m.collectUsageDataFromSyncMap() {
			sum += usage.Usage
		}
		collected <- sum
	}()
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range additions {
				m.AddOrUpdatePort(8080, 64)
			}
		}()
	}
	wg.Wait()
	finished.Store(true)
	if got, want := <-collected, uint64(writers*additions*64); got != want {
		t.Fatalf("snapshots recorded %d bytes, want %d", got, want)
	}
}

func TestUsageSavePreservesLogPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file modes do not represent Unix permission bits")
	}
	m := testUsage(t)
	if err := os.WriteFile(m.snifferLog, []byte("null"), 0640); err != nil {
		t.Fatal(err)
	}
	m.AddOrUpdatePort(8080, 64)
	m.saveUsageData()
	info, err := os.Stat(m.snifferLog)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0640 {
		t.Fatalf("usage log permissions changed to %o, want 0640", mode)
	}
}

// A failed replacement must leave the committed document intact. In
// particular, an incomplete temporary document cannot truncate the old one.
func TestUsageReplacementFailurePreservesCommittedDocument(t *testing.T) {
	m := testUsage(t)
	m.AddOrUpdatePort(8080, 64)
	m.saveUsageData()
	original, err := os.ReadFile(m.snifferLog)
	if err != nil {
		t.Fatal(err)
	}
	// A directory is a deterministic replacement failure on every platform.
	blocked := filepath.Join(filepath.Dir(m.snifferLog), "blocked")
	if err := os.Mkdir(blocked, 0755); err != nil {
		t.Fatal(err)
	}
	oldPath := m.snifferLog
	m.snifferLog = blocked
	if err := m.writeUsageData([]byte("replacement")); err == nil {
		t.Fatal("replaced an existing directory with the usage document")
	}
	m.snifferLog = oldPath
	body, err := os.ReadFile(m.snifferLog)
	if err != nil || string(body) != string(original) {
		t.Fatalf("committed document changed: %q, err=%v", body, err)
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(oldPath), ".usage-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("failed replacement left temporary files: %v, err=%v", files, err)
	}
}

func TestUsageSaveRetriesAfterCommittedLogWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions enforced for a non-root user")
	}
	m := testUsage(t)
	m.AddOrUpdatePort(8080, 64)
	m.saveUsageData()
	dir := filepath.Dir(m.snifferLog)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })
	m.AddOrUpdatePort(8080, 1024)
	m.saveUsageData()
	if got := m.totalTraffic.Load(); got != 64 {
		t.Fatalf("failed save published uncommitted traffic: %d", got)
	}
	data := m.getUsageFromFile()
	if len(data) != 1 || data[0].Usage != 64 {
		t.Fatalf("failed write changed committed document: %v", data)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	m.saveUsageData()
	if got := m.totalTraffic.Load(); got != 1088 {
		t.Fatalf("retry lost pending traffic: %d", got)
	}
}

func TestUsageSavePreservesConfiguredSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require Windows privileges")
	}
	m := testUsage(t)
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "target.json")
	if err := os.WriteFile(target, []byte("null"), 0640); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(filepath.Dir(m.snifferLog), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relative, m.snifferLog); err != nil {
		t.Fatal(err)
	}
	m.AddOrUpdatePort(8080, 64)
	m.saveUsageData()
	if link, err := os.Readlink(m.snifferLog); err != nil || link != relative {
		t.Fatalf("configured symlink replaced: link=%q err=%v", link, err)
	}
	data := m.getUsageFromFile()
	if len(data) != 1 || data[0].Usage != 64 {
		t.Fatalf("resolved target lost usage: %v", data)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("target permissions changed: info=%v err=%v", info, err)
	}
}

func TestUsageSaveRetainsDanglingSymlinkAndPendingTraffic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require Windows privileges")
	}
	m := testUsage(t)
	if err := os.Symlink("target.json", m.snifferLog); err != nil {
		t.Fatal(err)
	}
	m.AddOrUpdatePort(8080, 64)
	m.saveUsageData()
	if _, err := os.Readlink(m.snifferLog); err != nil {
		t.Fatal("dangling symlink was replaced:", err)
	}
	if got := m.totalTraffic.Load(); got != 0 {
		t.Fatalf("failed save published %d bytes", got)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(m.snifferLog), "target.json"), []byte("null"), 0644); err != nil {
		t.Fatal(err)
	}
	m.saveUsageData()
	if got := m.totalTraffic.Load(); got != 64 {
		t.Fatalf("retry lost usage: got=%d", got)
	}
}

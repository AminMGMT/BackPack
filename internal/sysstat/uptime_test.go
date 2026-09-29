package sysstat

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The machine's uptime is what /proc/uptime says, not what the boot time in
// /proc/stat implies.
//
// On an OpenVZ or Virtuozzo guest — a large share of the VPSes this runs on —
// /proc/stat's btime is the boot of the host node, not of the container, while
// /proc/uptime is virtualised per container. gopsutil reads btime for every
// guest it does not recognise as LXC or Docker, so a server bought an hour ago
// showed the host's months of uptime on the overview.
func TestUptimeIsReadFromProcUptime(t *testing.T) {
	p := filepath.Join(t.TempDir(), "uptime")
	if err := os.WriteFile(p, []byte("4242.17 16000.00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	was := procUptimePath
	procUptimePath = p
	t.Cleanup(func() { procUptimePath = was })

	if got := Get().Uptime; got != 4242*time.Second {
		t.Fatalf("Uptime = %v, want 1h10m42s from /proc/uptime", got)
	}
}

// A file that cannot be read or parsed falls back to what the library says
// rather than to zero.
func TestUptimeFallsBackWhenProcUptimeIsUnreadable(t *testing.T) {
	for _, body := range []string{"", "not-a-number 1\n"} {
		p := filepath.Join(t.TempDir(), "uptime")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := readUptime(p); ok {
			t.Errorf("readUptime(%q) accepted a file it cannot parse", body)
		}
	}
	if _, ok := readUptime(filepath.Join(t.TempDir(), "missing")); ok {
		t.Error("readUptime accepted a missing file")
	}
}

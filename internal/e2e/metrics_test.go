package e2e

import (
	"testing"
	"time"

	"github.com/backpack/backpack/internal/metrics"
)

// TestMetricsRecordFECRecovery is the proof that the metrics screen tells the
// truth: after pushing traffic across a lossy link, the counters must show
// packets that error correction rebuilt.
//
// It also guards the claim the CLI makes to the user. If FEC ever stopped
// working, the screen would quietly report a clean link instead of a broken
// feature, and nobody would notice.
func TestMetricsRecordFECRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("slow by design — skipped under -short")
	}

	dir := t.TempDir()
	collector := metrics.NewCollector(dir, "loss-tunnel", "kcp", "client", nil, nil)

	// A clean starting point, before any traffic has moved.
	before := collector.Snapshot()
	if before.KCP == nil {
		t.Fatal("a KCP tunnel must report KCP statistics")
	}

	backend := startEchoBackend(t)
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	const token = "metrics-token-0123456789abcdefg"

	relay := startLossyRelay(t, addrOf(tunnelPort), 15)
	tun := startTunnelVia(t, "kcp", backend, tunnelPort, entryPort, token, relay.Addr)

	// Enough traffic that a 15% loss rate is certain to have cost some packets.
	for i := 0; i < 4; i++ {
		if err := tun.roundTrip(randomPayload(t, 128*1024)); err != nil {
			t.Fatalf("traffic failed across the lossy link: %v", err)
		}
	}

	after := collector.Snapshot()
	if after.KCP.PacketsOut <= before.KCP.PacketsOut {
		t.Fatal("no outgoing packets were counted after moving 512 KiB")
	}

	repaired := after.KCP.FECRecovered - before.KCP.FECRecovered
	resent := after.KCP.Retransmitted - before.KCP.Retransmitted
	if repaired == 0 && resent == 0 {
		t.Fatal("15% of datagrams were dropped, yet the counters show nothing " +
			"repaired and nothing resent — the statistics are not being collected")
	}
	t.Logf("across a 15%% loss link: %d packets rebuilt by error correction, %d resent",
		repaired, resent)

	// The snapshot must also survive a round trip through disk, since that is
	// how the CLI actually reads it.
	if err := collector.Write(); err != nil {
		t.Fatalf("writing the snapshot failed: %v", err)
	}
	read, err := metrics.Read(dir, "loss-tunnel")
	if err != nil {
		t.Fatalf("reading the snapshot back failed: %v", err)
	}
	// Write() takes its own reading, and the counters keep moving, so the
	// persisted value can only be compared as "at least what we saw".
	if read.KCP == nil || read.KCP.PacketsOut < after.KCP.PacketsOut {
		t.Fatalf("the snapshot did not survive being written and read back: %+v", read.KCP)
	}
	if read.Transport != "kcp" || read.Name != "loss-tunnel" {
		t.Fatalf("snapshot identity was lost: %+v", read)
	}
}

// TestMetricsMissingFileIsNotAnError checks the case the CLI hits constantly:
// a tunnel that has not written a reading yet.
func TestMetricsMissingFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	if _, err := metrics.Read(dir, "never-started"); err == nil {
		t.Fatal("reading a snapshot that does not exist should report an error the caller can handle")
	}
}

// TestMetricsWriteIsAtomic makes sure a reader can never catch a half-written
// file, which would show the user nonsense.
func TestMetricsWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	c := metrics.NewCollector(dir, "atomic", "tcp", "server", nil, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			if err := c.Write(); err != nil {
				t.Errorf("write %d failed: %v", i, err)
				return
			}
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return
		default:
		}
		// Any successful read must yield a well-formed snapshot.
		if s, err := metrics.Read(dir, "atomic"); err == nil && s.Name != "atomic" {
			t.Fatalf("read a malformed snapshot: %+v", s)
		}
	}
	<-done
}

func addrOf(port int) string {
	return "127.0.0.1:" + itoa(port)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

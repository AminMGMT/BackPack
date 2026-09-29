package e2e

import (
	"os"
	"testing"
	"time"
)

// TestTransportThroughput measures what each reverse transport carries on
// loopback: one connection, a payload echoed through the tunnel and back.
//
// Run with BP_THROUGHPUT=1. It is a measurement, not a gate — loopback on a
// busy machine says nothing absolute — but it is the number to compare before
// and after a change to a transport's data path, and the profile it is run
// under (-cpuprofile) is where a slow one says why.
func TestTransportThroughput(t *testing.T) {
	if os.Getenv("BP_THROUGHPUT") == "" {
		t.Skip("set BP_THROUGHPUT=1 to measure")
	}
	const size = 32 << 20
	payload := randomPayload(t, size)
	for _, transport := range []string{"tcp", "tcpmux", "ws", "wsmux", "kcp", "quic"} {
		t.Run(transport, func(t *testing.T) {
			backend := startEchoBackend(t)
			tun := startTunnel(t, transport, backend, tunnelOptions{})
			// One warm-up round, so a pool that is still filling is not what
			// gets measured.
			if err := tun.roundTrip(payload[:1<<20]); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			if err := tun.roundTrip(payload); err != nil {
				t.Fatal(err)
			}
			took := time.Since(start)
			// Each byte crosses the tunnel twice: there and back.
			mbit := float64(2*size*8) / took.Seconds() / 1e6
			t.Logf("%-7s %6.0f Mbit/s (32 MB each way in %s)", transport, mbit, took.Round(time.Millisecond))
		})
	}
}

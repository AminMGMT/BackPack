package e2e

import (
	"fmt"
	"testing"
	"time"
)

// Is zero-copy forwarding worth turning on by default?
//
// The splice path is implemented, careful and correct — it unwraps the metrics
// counter because onChunk counts exactly what it would have, and refuses to
// unwrap a rate-limited connection because pacing means looking at the bytes.
// It has been off by default since it was written, for a stated reason: it is
// the least proven path here. What was missing to change that was not code, it
// was a number.
//
// This produces the number. Run it rather than reading it:
//
//	go test ./internal/e2e/ -run TestSpliceThroughput -v -count=1
//
// Read the result with the loopback in mind. splice saves a copy between two
// sockets and the kernel; on loopback the copy is cheap and the syscall
// overhead dominates, so a gain measured here is a *lower bound* on a real NIC
// and a loss here would be damning. It is evidence for a decision, not the
// decision.
func TestSpliceThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("throughput measurement")
	}
	const payload = 8 << 20 // 8 MB per round trip

	for _, zeroCopy := range []bool{false, true} {
		name := "buffered"
		if zeroCopy {
			name = "splice"
		}
		t.Run(name, func(t *testing.T) {
			backend := startEchoBackend(t)
			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "splice-measurement-token-0123456"

			srvCfg := baseServerConfig("tcp", tunnelPort, entryPort, backend.addr, token)
			srvCfg.ZeroCopy = zeroCopy
			cliCfg := baseClientConfig("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)
			cliCfg.ZeroCopy = zeroCopy

			tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("tunnel never came up: %v", err)
			}

			data := randomPayload(t, payload)
			// One warm-up pass so the pool is full and the buffers have settled.
			if err := tun.roundTrip(data); err != nil {
				t.Fatalf("warm-up failed: %v", err)
			}

			const passes = 4
			start := time.Now()
			for i := 0; i < passes; i++ {
				if err := tun.roundTrip(data); err != nil {
					t.Fatalf("pass %d failed: %v", i+1, err)
				}
			}
			elapsed := time.Since(start)
			// Each round trip carries the payload twice — out and back.
			bits := float64(payload) * 2 * passes * 8
			t.Logf("%-9s %5.0f Mbit/s  (%s for %d x %d MB round trips)",
				name, bits/elapsed.Seconds()/1e6, elapsed.Round(time.Millisecond),
				passes, payload>>20)
		})
	}
}

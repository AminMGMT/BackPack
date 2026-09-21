package e2e

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// What happens at hour two.
//
// The suite proves correctness over seconds: `-race` and a three-minute
// end-to-end run. Both leaks this codebase has had are invisible at that
// timescale and obvious at a week —
//
//   - pooled connection slots bleeding on a pairing timeout, so a tunnel with
//     max_connections eventually refused everything with a limit that looked
//     correct in the config;
//   - a sixty-second timer allocated for every forwarded UDP packet and held
//     for its full duration, which at a thousand packets a second is sixty
//     thousand live timers for one direction of one flow.
//
// Both were found by reading the code, months after they were written. Neither
// would have survived two hours of traffic and a goroutine graph.
//
// So: real traffic, sustained, with the process watched. The shape that matters
// is many short connections rather than one long transfer, because that is what
// exercises the per-connection accounting where both leaks lived — a single
// long download allocates its buffers once and tells you nothing.
//
// Off by default. BACKPACK_SOAK_SECONDS is what turns it on, so the ordinary
// suite stays fast and CI can give it a couple of minutes without anybody
// waiting on a laptop.

const soakEnv = "BACKPACK_SOAK_SECONDS"

func TestASustainedRunDoesNotLeak(t *testing.T) {
	seconds, _ := strconv.Atoi(os.Getenv(soakEnv))
	if seconds <= 0 {
		t.Skipf("set %s to a number of seconds to run this", soakEnv)
	}
	if seconds < 30 {
		t.Fatalf("%s=%d is too short to distinguish a leak from warm-up; use 30 or more",
			soakEnv, seconds)
	}

	backend := startEchoBackend(t)
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	const token = "soak-token-0123456789abcdefghij"

	srvCfg := baseServerConfig("tcp", tunnelPort, entryPort, backend.addr, token)
	cliCfg := baseClientConfig("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("the tunnel never came up: %v", err)
	}

	stop := make(chan struct{})
	var round, failed atomic.Int64
	// A few concurrent callers, each opening and closing a connection per
	// round. That is the browser shape: thirty short connections to load one
	// page, not one long download.
	for i := 0; i < 4; i++ {
		go func() {
			payload := make([]byte, 4096)
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := oneShortConnection(tun.Entry, payload); err != nil {
					failed.Add(1)
				} else {
					round.Add(1)
				}
			}
		}()
	}

	total := time.Duration(seconds) * time.Second
	// The first quarter is warm-up: pools fill, buffers reach their working
	// size, and the goroutine count settles at whatever this configuration
	// actually needs. Measuring through it would read normal growth as a leak.
	warmup := total / 4
	t.Logf("soaking for %s (%s of it warm-up)", total, warmup)
	time.Sleep(warmup)

	var samples []sample
	deadline := time.Now().Add(total - warmup)
	for time.Now().Before(deadline) {
		samples = append(samples, takeSample())
		time.Sleep(time.Second)
	}
	close(stop)

	if len(samples) < 12 {
		t.Fatalf("only %d samples; give it longer", len(samples))
	}
	if round.Load() == 0 {
		t.Fatalf("no connection completed in %s (%d failed); the soak measured an idle tunnel",
			total, failed.Load())
	}
	t.Logf("%d round trips, %d failed", round.Load(), failed.Load())

	// A trend, not an absolute number. Goroutine counts wobble with scheduling
	// and the heap saws between collections, so a threshold on any single
	// reading would fail on runner noise. Comparing the mean of the first third
	// against the last asks the question that matters: is it going up and
	// staying up?
	third := len(samples) / 3
	early, late := samples[:third], samples[len(samples)-third:]

	checkNoSustainedRise(t, "goroutines",
		meanInt(early, func(s sample) int { return s.goroutines }),
		meanInt(late, func(s sample) int { return s.goroutines }),
		// Generous: four callers opening connections mean the count genuinely
		// moves. A leak of the kind this is for does not settle at +20%, it
		// climbs without bound.
		1.30)

	if early[0].openFiles > 0 {
		checkNoSustainedRise(t, "open file descriptors",
			meanInt(early, func(s sample) int { return s.openFiles }),
			meanInt(late, func(s sample) int { return s.openFiles }),
			1.30)
	}
}

type sample struct {
	goroutines int
	openFiles  int
}

func takeSample() sample {
	return sample{goroutines: runtime.NumGoroutine(), openFiles: countOpenFiles()}
}

func countOpenFiles() int {
	d, err := os.Open("/proc/self/fd")
	if err != nil {
		return 0
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return 0
	}
	return len(names) - 1
}

func meanInt(ss []sample, pick func(sample) int) float64 {
	if len(ss) == 0 {
		return 0
	}
	sum := 0
	for _, s := range ss {
		sum += pick(s)
	}
	return float64(sum) / float64(len(ss))
}

func checkNoSustainedRise(t *testing.T, what string, early, late, allowed float64) {
	t.Helper()
	t.Logf("%s: %.1f early -> %.1f late", what, early, late)
	if early <= 0 {
		return
	}
	if late/early > allowed {
		t.Errorf("%s rose from %.1f to %.1f over the run (%.0f%% of the start, limit %.0f%%).\n"+
			"A sustained rise under steady load is what a leak looks like: something is "+
			"being created per connection and not released.",
			what, early, late, 100*late/early, 100*allowed)
	}
}

// oneShortConnection is the unit of load: connect, write, read back, close.
func oneShortConnection(addr string, payload []byte) error {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if _, err := c.Write(payload); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	_, err = readFull(c, got)
	return err
}

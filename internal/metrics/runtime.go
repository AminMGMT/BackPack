package metrics

import (
	"os"
	"runtime"
)

// The numbers that predict a problem before it is a symptom.
//
// Two leaks have been found in this codebase by reading it: pooled connection
// slots bleeding on a pairing timeout, and a sixty-second timer allocated for
// every forwarded UDP packet. Neither was visible in anything the tunnel
// reported. Both had a monotonic curve that one counter on a graph would have
// made obvious within hours of a real deployment, and both were instead found
// in an audit — which is to say, by luck and effort rather than by the system
// saying so.
//
// The test suite cannot see them either: `-race` and a three-minute end-to-end
// run prove correctness over seconds, and these are failures of hour two and
// day thirty.
//
// So the process reports what it is holding. Goroutines and open file
// descriptors are the two that matter, because every leak this system can have
// shows up in one of them: a connection that is never closed holds a
// descriptor, and a copy loop that never returns holds a goroutine. They cost a
// runtime call and a directory read once every thirty seconds, which is the
// snapshot interval.

// RuntimeStats is what the process is holding right now.
type RuntimeStats struct {
	// Goroutines is runtime.NumGoroutine(). A tunnel at rest sits at a steady
	// number; one leaking climbs and does not come back down.
	Goroutines int `json:"goroutines"`

	// OpenFiles is how many file descriptors this process has open, or 0 where
	// that cannot be counted. Sockets are descriptors, so a connection leak
	// shows here even when it does not show in the goroutine count.
	OpenFiles int `json:"open_files,omitempty"`

	// HeapBytes and HeapObjects are the allocator's own view. Included because
	// an allocation-per-packet regression — the shape of the timer leak — moves
	// these long before it moves anything else.
	HeapBytes   uint64 `json:"heap_bytes"`
	HeapObjects uint64 `json:"heap_objects"`
}

// readRuntimeStats samples the process.
func readRuntimeStats() RuntimeStats {
	var m runtime.MemStats
	// ReadMemStats stops the world briefly. At once per snapshot — thirty
	// seconds — that is not a cost worth avoiding, and the heap figures are
	// half of what makes this useful.
	runtime.ReadMemStats(&m)
	return RuntimeStats{
		Goroutines:  runtime.NumGoroutine(),
		OpenFiles:   openFileCount(),
		HeapBytes:   m.HeapAlloc,
		HeapObjects: m.HeapObjects,
	}
}

// openFileCount counts this process's descriptors, or returns 0 where it
// cannot.
//
// /proc/self/fd is Linux's answer and this program is a Linux program, but the
// count is a diagnostic: a platform without it reports nothing rather than
// failing, and so does a hardened one that will not let the process read its
// own descriptor directory.
func openFileCount() int {
	d, err := os.Open("/proc/self/fd")
	if err != nil {
		return 0
	}
	defer d.Close()
	// Readdirnames rather than ReadDir: the names are all that is wanted, and
	// stat-ing every descriptor to throw the result away would make a counter
	// cost more than the thing it counts.
	names, err := d.Readdirnames(-1)
	if err != nil {
		return 0
	}
	// Minus one for the handle opened to do the counting, which is not a
	// descriptor the tunnel is holding.
	if n := len(names) - 1; n > 0 {
		return n
	}
	return 0
}

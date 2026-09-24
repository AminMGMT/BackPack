package network

import "github.com/xtaci/kcp-go/v5"

// One timer heap for every KCP session in the process, not one per CPU.
//
// kcp-go flushes each session on a timer, every kcp_interval milliseconds
// whether or not it has anything to send, and schedules those timers across
// runtime.NumCPU() goroutines — each with its own heap and its own timer, the
// session handed to one of them at random every time. On an idle tunnel that
// is nothing but wake-ups spread over every core: a KCP, pck or xdi pair with
// the default pool, doing nothing at all, cost 6% of a core per process on a
// 16-core machine, against 0.15% for every TCP transport. A profile of it is
// the scheduler and the clock and nothing else.
//
// With a single scheduler the timers share one heap and the runtime coalesces
// the wake-ups. Measured: idle 6% → 3%, and the same CPU per byte under load.
// The one cost is peak throughput with sixteen concurrent streams on loopback,
// about 5% lower at 2 Gbit/s — a rate no Iran-to-abroad path comes near, and
// one goroutine flushing every session keeps up with any real link.
func init() {
	kcp.SystemTimedSched.Close()
	kcp.SystemTimedSched = kcp.NewTimedSched(1)
}

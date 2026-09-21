package manage

import (
	"fmt"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/metrics"
)

// Health defined by what a tunnel is carrying, not by whether it is running.
//
// The watchdog's question has always been "is the control channel up". The
// failure that costs an operator the most is the one that answers yes: a tunnel
// that is up, reports a peer, shows green in the panel, and moves nothing. A
// path that drops full-sized packets does this. So does a far end whose relay
// goroutines are wedged, and so does a middlebox that lets the handshake
// through and then holds the flow.
//
// # Why "no bytes" is not the test
//
// An idle tunnel moves no bytes and is perfectly healthy, and most tunnels are
// idle most of the time. A watchdog that restarted on silence would restart
// every tunnel on the host every night. Silence is not a symptom.
//
// The symptom is *asymmetry*. If a tunnel is sending and nothing at all is
// coming back — not less, none — across several checks in a row, then something
// is being sent that is not arriving or not being answered. That is unambiguous
// in a way silence never is, it cannot be produced by an idle tunnel, and it is
// exactly the shape of every failure listed above.
//
// # Why the response is graduated
//
// Restarting is the only lever this side of the fence actually has: an engine
// runs as its own process with no control socket, so "re-dial", "rebuild the
// pool" and "restart the transport" are not available to ask for without
// building an IPC channel first. Pretending otherwise would mean a ladder whose
// rungs are all the same rung.
//
// What is available is knowing when to stop. A stall that survives a restart is
// not a tunnel that got stuck; it is a path, an MTU or a far-end service, and
// restarting it every three minutes makes it worse while burying the evidence.
// So the ladder here is: notice it and say so, restart once it has persisted,
// and if restarting does not fix it, stop restarting and say what to look at.

const (
	// stallProgress is how many bytes one direction must advance before its
	// silent partner counts as a stall rather than a rounding error. A
	// heartbeat is tens of bytes; this is well past anything keepalive traffic
	// alone produces.
	stallProgress = 64 * 1024

	// stallChecks is how many consecutive watchdog intervals the asymmetry must
	// hold. At the 25-second interval this is a little over two minutes of one
	// side talking and the other saying nothing.
	stallChecks = 5

	// stallRestartAfter is how long a noticed stall is left alone before a
	// restart. A long transfer in one direction can legitimately produce a
	// lopsided reading for a while — an upload is not a stall — so the first
	// response is to watch, not to act.
	stallRestartAfter = 3 * time.Minute

	// stallGiveUpAfter is how many restarts may be spent on one stall before
	// the watchdog concludes that restarting is not the fix.
	stallGiveUpAfter = 2

	// stallQuietFor is how long a tunnel is left alone after giving up, so the
	// operator gets one clear report rather than a restart loop.
	stallQuietFor = 30 * time.Minute
)

// flowState is what the counters say about one tunnel.
type flowState int

const (
	flowUnknown flowState = iota // no usable snapshot
	flowIdle                     // nothing moving either way; not a fault
	flowOK                       // moving in both directions
	flowStalled                  // one direction moving, the other frozen
)

func (f flowState) String() string {
	switch f {
	case flowIdle:
		return "idle"
	case flowOK:
		return "carrying"
	case flowStalled:
		return "stalled"
	}
	return "unknown"
}

// flowReading is one tunnel's place in the state machine.
type flowReading struct {
	in, out uint64
	// runs counts consecutive checks that looked stalled.
	runs int
	// since is when the stall was first noticed, zero when there is none.
	since time.Time
	// direction names the frozen side, for the report.
	direction string
	// restarts is how many restarts this stall has cost.
	restarts int
	// quietUntil suppresses action after giving up.
	quietUntil time.Time
	// reported is whether the current stall has been announced.
	reported bool
}

// flowWatch tracks every tunnel's throughput across watchdog intervals.
//
// It is a value with no I/O of its own: observe() is handed a snapshot and a
// clock, which is what makes the whole state machine testable without a tunnel,
// a timer or a filesystem.
type flowWatch struct {
	seen map[string]*flowReading
}

func newFlowWatch() *flowWatch { return &flowWatch{seen: map[string]*flowReading{}} }

// forget drops a tunnel's history, for one that has been stopped on purpose.
func (w *flowWatch) forget(name string) { delete(w.seen, name) }

// observe folds one reading in and reports what the counters now say.
func (w *flowWatch) observe(name string, in, out uint64, now time.Time) flowState {
	r, ok := w.seen[name]
	if !ok {
		w.seen[name] = &flowReading{in: in, out: out}
		return flowUnknown // nothing to compare against yet
	}

	dIn, dOut := delta(r.in, in), delta(r.out, out)
	r.in, r.out = in, out

	switch {
	case dIn == 0 && dOut == 0:
		// Idle. Not a fault, and it also must not *clear* a stall: a stalled
		// tunnel whose sender finally gives up goes quiet, and treating that as
		// recovery would be the watchdog looking away at the worst moment.
		if r.runs >= stallChecks {
			return flowStalled
		}
		return flowIdle

	case dIn >= stallProgress && dOut == 0:
		r.runs++
		r.direction = "nothing is going out"
	case dOut >= stallProgress && dIn == 0:
		r.runs++
		r.direction = "nothing is coming back"

	default:
		// Both directions moved, or the lopsided side moved too little to say
		// anything. Either way this is not a stall.
		w.clear(r)
		return flowOK
	}

	if r.runs < stallChecks {
		return flowOK
	}
	if r.since.IsZero() {
		r.since = now
	}
	return flowStalled
}

// clear resets a tunnel's stall state. Called when the counters recover, which
// is also the only thing that ends a give-up.
func (w *flowWatch) clear(r *flowReading) {
	r.runs = 0
	r.since = time.Time{}
	r.direction = ""
	r.restarts = 0
	r.reported = false
	r.quietUntil = time.Time{}
}

// stallAction is what the watchdog should do about a stall right now.
type stallAction int

const (
	stallWatch   stallAction = iota // nothing yet
	stallReport                     // say it out loud, take no action
	stallRestart                    // restart the unit
	stallGiveUp                     // stop restarting and say why
)

// decide turns a stalled tunnel into the next step on the ladder. It is
// separate from observe so the escalation can be read — and tested — without
// the counter arithmetic in the way.
func (w *flowWatch) decide(name string, now time.Time) (stallAction, string) {
	r, ok := w.seen[name]
	if !ok || r.since.IsZero() {
		return stallWatch, ""
	}
	if now.Before(r.quietUntil) {
		return stallWatch, ""
	}
	if !r.reported {
		r.reported = true
		return stallReport, fmt.Sprintf(
			"🟠 Tunnel %s is connected but %s. Watching it before acting — a long "+
				"one-way transfer can look like this.", name, r.direction)
	}
	if now.Sub(r.since) < stallRestartAfter {
		return stallWatch, ""
	}
	if r.restarts >= stallGiveUpAfter {
		r.quietUntil = now.Add(stallQuietFor)
		return stallGiveUp, fmt.Sprintf(
			"🔴 Tunnel %s is still stalled after %d restarts (%s). Restarting is not "+
				"fixing this, so it will be left alone for %s. A stall that survives a "+
				"restart is the path, not the tunnel: check the MTU clamp, whether the "+
				"far end's service is listening, and run Diagnose on this tunnel.",
			name, r.restarts, r.direction, stallQuietFor)
	}
	r.restarts++
	// The clock restarts so the next rung is measured from this restart rather
	// than from when the stall began.
	r.since = now
	return stallRestart, fmt.Sprintf(
		"🟠 Tunnel %s has been connected but %s for %s — restarting it (%d/%d).",
		name, r.direction, stallRestartAfter, r.restarts, stallGiveUpAfter)
}

// delta handles a counter that went backwards, which is what a restarted engine
// looks like: it starts from zero. Treating that as a huge advance would make a
// freshly restarted tunnel look busy in one direction and stalled in the other,
// which is precisely the wrong reading at precisely the wrong moment.
func delta(prev, now uint64) uint64 {
	if now < prev {
		return 0
	}
	return now - prev
}

// tunnelFlow reads a tunnel's counters. It returns ok=false for a snapshot that
// is missing, too old, or from a tunnel whose engine says it is not connected —
// a disconnected tunnel is already the watchdog's existing business and must not
// also be reported as a stall.
func tunnelFlow(name string) (in, out uint64, ok bool) {
	snap, err := metrics.Read(app.ConfigDir, name)
	if err != nil {
		return 0, 0, false
	}
	if time.Since(snap.Taken) > datagramPeerWindow {
		return 0, 0, false
	}
	if snap.Connected == nil || !*snap.Connected {
		return 0, 0, false
	}
	return snap.BytesIn, snap.BytesOut, true
}

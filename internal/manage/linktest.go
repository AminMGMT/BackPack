package manage

import "time"

// Measuring the path a tunnel runs over.
//
// This lives here rather than in the panel because it is not a panel feature:
// it is a measurement of a tunnel, and the machine that can take it is the one
// whose tunnel dials out. That is often not the machine the panel runs on — an
// Iran panel manages a fleet, and the tunnel's dialling half is on a server in
// it — so the same code has to be reachable from the panel and from a managed
// server answering for itself.

// LinkTestResult is what one measurement found.
type LinkTestResult struct {
	Name     string   `json:"name"`
	Target   string   `json:"target"`
	Sent     int      `json:"sent"`
	Received int      `json:"received"`
	MinMs    int      `json:"minMs"`
	AvgMs    int      `json:"avgMs"`
	MaxMs    int      `json:"maxMs"`
	JitterMs int      `json:"jitterMs"`
	LossPct  float64  `json:"lossPct"`
	Usable   bool     `json:"usable"`
	Error    string   `json:"error,omitempty"`
	RecLabel string   `json:"recLabel,omitempty"`
	RecWhy   []string `json:"recWhy,omitempty"`
	Caveats  []string `json:"caveats,omitempty"`

	// RecFEC is the parity ratio ("data:parity") recommended when the pick is a
	// KCP transport, sized to the measured loss. Empty otherwise.
	RecFEC    string `json:"recFEC,omitempty"`
	RecFECWhy string `json:"recFECWhy,omitempty"`

	// RanOn names the machine that took the measurement, when it was not this
	// one. A latency figure means nothing without knowing where it was measured
	// from, and a panel that shows a reading taken on another server without
	// saying so is presenting somebody else's path as its own.
	RanOn string `json:"ranOn,omitempty"`
}

// MeasureLink measures the path one tunnel dials out over.
//
// Named apart from the CLI's LinkTest screen, which is the menu around it.
func MeasureLink(t Tunnel) LinkTestResult {
	q := ProbePath(t.Addr)
	res := LinkTestResult{
		Name:     t.Name,
		Target:   q.Target,
		Sent:     q.Sent,
		Received: q.Received,
		MinMs:    int(q.Min / time.Millisecond),
		AvgMs:    int(q.Avg / time.Millisecond),
		MaxMs:    int(q.Max / time.Millisecond),
		JitterMs: int(q.Jitter / time.Millisecond),
		LossPct:  q.LossPercent(),
		Usable:   q.Usable(),
	}
	if q.Err != nil {
		res.Error = q.Err.Error()
		return res
	}
	rec := RecommendTransport(q, t.Transport, ProbeUDPEgress())
	res.RecLabel = rec.Label
	res.RecWhy = rec.Why
	res.Caveats = rec.Caveats
	if rec.FEC.Set() {
		res.RecFEC = rec.FEC.Ratio()
		res.RecFECWhy = rec.FEC.Why
	}
	return res
}

// LinkTestable reports whether a tunnel can be measured on the machine holding
// it, and why not when it cannot.
//
// The two reasons are the same on any machine, which is why they are decided
// here rather than in whichever caller happens to ask: a server-role tunnel has
// no address to probe, and a datagram tunnel probed over TCP would report a
// working link as dead.
func LinkTestable(t Tunnel) (bool, string) {
	if t.Role != "client" {
		return false, "the link test measures the path a tunnel dials out over, and this " +
			"end listens rather than dialling"
	}
	if IsDatagram(t.Transport) {
		return false, "a UDP-based tunnel cannot be probed over TCP — its metrics " +
			"(loss, FEC repairs) are the honest measure of this link"
	}
	return true, ""
}

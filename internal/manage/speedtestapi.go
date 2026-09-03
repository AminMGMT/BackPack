package manage

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// The Speed Test, without a terminal in front of it.
//
// ThroughputSender next door is the same measurement wearing a menu: it asks
// which tunnel, which mapping, and whether the far end is ready, then measures.
// The panel needs to ask those questions in its own way, so what it needs is
// the answers underneath — which tunnel can be measured from this machine, what
// it would be measured through, and what it costs to do it.
//
// Nothing here talks to a user. The one rule the menu enforces that this cannot
// is the confirmation that the receiver is running on the other server, because
// there is no way to find out from here: a measurement against a machine that
// is not sinking simply fails, and it fails in about a second.

// SpeedTestTarget is one route a measurement could take through a port
// forwarder: a port on this machine, and the port the far end must be sinking
// on for it to arrive.
type SpeedTestTarget struct {
	// Spec is the mapping as written in the config.
	Spec string `json:"spec"`
	// ListenPort is the port on this machine to push bytes at.
	ListenPort int `json:"listenPort"`
	// BackendPort is where the receiver on the other server must listen, which
	// is the port the real backend normally holds — so for as long as the
	// measurement runs, that service is down.
	BackendPort int `json:"backendPort"`
	// Reason is why this mapping cannot carry a measurement, empty when it can.
	Reason string `json:"reason,omitempty"`
}

// Usable reports whether a measurement can run through this target.
func (t SpeedTestTarget) Usable() bool { return t.Reason == "" }

// SpeedTestPlan is what this machine can measure for one tunnel.
type SpeedTestPlan struct {
	// Kind is "l3" for a layer-3 tunnel, measured across its own private
	// subnet, or "forward" for a port forwarder, measured through a mapping.
	Kind string `json:"kind"`
	// Peer is the address the bytes are pushed at, for an l3 tunnel.
	Peer string `json:"peer,omitempty"`
	// Port is the port they are pushed at, for an l3 tunnel.
	Port int `json:"port,omitempty"`
	// Targets are the mappings to choose between, for a forwarder.
	Targets []SpeedTestTarget `json:"targets,omitempty"`
	// Blocked is why no measurement can start from this machine at all.
	Blocked string `json:"blocked,omitempty"`
	// Seconds is roughly how long a measurement takes, so the page can say so
	// before somebody starts one.
	Seconds int `json:"seconds"`
}

// speedTestBudget bounds one measurement. The run itself is the warm-up plus
// the timed stretch; the rest covers the connect and a slow start on a long
// path. It stays well inside the panel's write timeout, because a request that
// outlives the response is a measurement nobody gets to see.
const speedTestBudget = 25 * time.Second

// SpeedTestPlanFor reports how this machine would measure one tunnel, or why it
// cannot.
func SpeedTestPlanFor(name string) (SpeedTestPlan, error) {
	t, ok := Find(name)
	if !ok {
		return SpeedTestPlan{}, fmt.Errorf("no tunnel named %q", name)
	}
	plan := SpeedTestPlan{Seconds: int((throughputWarmup + throughputRun) / time.Second)}

	if !isForwardKind(t) {
		plan.Kind = "l3"
		plan.Peer, plan.Port = tunnelPeerIP(t.Name), throughputPort
		if plan.Peer == "" {
			plan.Blocked = "this tunnel has no peer address to measure against"
		}
		return plan, nil
	}

	plan.Kind = "forward"
	// The side holding the backends keeps no port list, so there is nothing
	// here to measure through — that side runs the receiver instead.
	if !HoldsPorts(t) {
		plan.Blocked = "this side holds the backends, so it is the side that receives — " +
			"measure from the server that exposes the ports"
		return plan, nil
	}
	targets, err := forwardMappings(t)
	if err != nil {
		return SpeedTestPlan{}, err
	}
	for _, m := range targets {
		plan.Targets = append(plan.Targets, SpeedTestTarget{
			Spec: m.Spec, ListenPort: m.ListenPort, BackendPort: m.TargetPort, Reason: m.Reason,
		})
	}
	if len(plan.Targets) == 0 {
		plan.Blocked = "this tunnel forwards no ports, so there is nothing to measure through"
	}
	return plan, nil
}

// RunSpeedTest measures one tunnel and reports what it carried.
//
// listenPort selects the mapping for a port forwarder and is ignored for a
// layer-3 tunnel, which has its own subnet to work in. The far end must already
// be sinking; if it is not, this returns the connection error rather than
// waiting, which is the honest answer and a fast one.
func RunSpeedTest(ctx context.Context, name string, listenPort int) (ThroughputResult, error) {
	plan, err := SpeedTestPlanFor(name)
	if err != nil {
		return ThroughputResult{}, err
	}
	if plan.Blocked != "" {
		return ThroughputResult{}, fmt.Errorf("%s", plan.Blocked)
	}

	peer, port := plan.Peer, plan.Port
	if plan.Kind == "forward" {
		var chosen *SpeedTestTarget
		for i := range plan.Targets {
			if plan.Targets[i].ListenPort == listenPort {
				chosen = &plan.Targets[i]
				break
			}
		}
		if chosen == nil {
			return ThroughputResult{}, fmt.Errorf("port %d is not one of this tunnel's mappings", listenPort)
		}
		if !chosen.Usable() {
			return ThroughputResult{}, fmt.Errorf("that mapping cannot carry a measurement: %s", chosen.Reason)
		}
		peer, port = forwardBackendHost, chosen.ListenPort
	}

	ctx, cancel := context.WithTimeout(ctx, speedTestBudget)
	defer cancel()
	res, err := MeasureThroughputOn(ctx, peer, port)
	if err != nil && plan.Kind == "forward" && isRefused(err) {
		return res, refusedLocally(name, port)
	}
	return res, err
}

// isRefused reports whether nothing was listening at the address dialled.
func isRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

// refusedLocally explains a refused connection on a forwarder's own port.
//
// The port a forwarding measurement dials is on this machine: it is the
// tunnel's own listener, and the bytes only reach the other server because
// that listener carries them. So a refusal means the connection never left the
// box, and the far end had no part in it.
//
// This used to come back as "is the receiver running there?", with an
// instruction to go and start one on the other server. That is the wrong
// machine. The report that prompted this said the speed test failed with a
// connected server and a tunnel in place, and told them to turn on a receiver —
// which they did, on a server that was never the problem.
func refusedLocally(name string, port int) error {
	if !IsActive(app.ServiceName(name)) {
		return fmt.Errorf("this tunnel is not running on this server, so nothing is "+
			"listening on port %d — start it and measure again", port)
	}
	return fmt.Errorf("nothing is listening on port %d on this server even though %s is "+
		"running — check the tunnel's forwarded ports, and its log for a listener that "+
		"could not bind", port, name)
}

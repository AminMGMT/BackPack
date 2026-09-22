package control

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/node"
)

// What a managed server is supposed to be running, and what it is running.
//
// Every fleet operation was imperative: the panel told a node to create a
// tunnel, the node said it had, and that was the end of it. Nothing remembered
// the instruction, so nothing could ever notice that it had stopped being true.
// A tunnel deleted on the far machine by somebody with a terminal, a unit
// disabled during an incident and never re-enabled, an apply that reported
// success and then lost its config to a rollback — all of them leave a fleet
// that looks correct on the panel and is not, and the only way to find out was
// to go and look.
//
// This is the missing half: the panel writes down what it asked for, and the
// answer to "list your tunnels" can then be compared with it.
//
// # Detection, not remediation
//
// Nothing here changes a remote machine. Reconcile returns differences and that
// is all it does.
//
// That is a deliberate stopping point rather than an unfinished one. A
// reconciler that re-applies on its own is a loop that can fight an operator
// who is in the middle of something, and on this product the thing it would be
// fighting over is the tunnel that operator is reaching the machine through.
// What an operator needs first is to be told; what to do about it is a decision
// with their hand on it.
//
// # Why the intent is not derived from the config
//
// The panel writes both ends when it builds a pair, so it could in principle
// re-read its own side and infer the far one. It cannot infer the two facts
// that matter most: that the far end was *meant* to exist at all, and that it
// was meant to be running. A tunnel that is absent and a tunnel that was never
// created look identical from here, and those are the two cases this exists to
// tell apart.

// desiredPath is where the intent is kept: beside the fleet it describes, with
// the same permissions as every other secret this panel holds. It holds no
// credentials, but it does describe somebody's infrastructure.
var desiredPath = app.ConfigDir + "/desired.json"

// TunnelIntent is one tunnel the panel expects a node to be running.
//
// It carries what identifies a tunnel and what is worth noticing when it
// changes, and nothing else. The token is not here on purpose: the intent is
// read to answer a question about a machine, and a file that answers that
// question should not also be a file that opens the tunnel.
type TunnelIntent struct {
	Name string `json:"name"`
	// Role is "server" or "client" — which half of the pair this node holds.
	Role string `json:"role,omitempty"`
	// TunnelPort is the port the pair meets on. It is the one field that ties
	// the two ends together, because nothing in either configuration names the
	// other by name.
	TunnelPort string `json:"tunnelPort,omitempty"`
	// Running is whether the panel expects the service to be up. False is a
	// tunnel deliberately stopped, which is a state worth keeping so that
	// finding it stopped is not reported as drift.
	Running bool `json:"running"`
	// Recorded is when the panel last wrote this down, so a report can say how
	// old the expectation is.
	Recorded int64 `json:"recorded,omitempty"`
}

// DriftKind is what is different.
type DriftKind string

const (
	// DriftMissing: the panel put a tunnel there and the node does not have it.
	DriftMissing DriftKind = "missing"
	// DriftStopped: it is there and not running when it should be.
	DriftStopped DriftKind = "stopped"
	// DriftRunning: it is running when the panel was told to stop it.
	DriftRunning DriftKind = "running"
	// DriftChanged: it is there under the same name and does not match what
	// was written — a different port, or the other role.
	DriftChanged DriftKind = "changed"
	// DriftUnexpected: the node is running a tunnel the panel did not put
	// there. Not a fault, and worth naming: it is how a tunnel created by hand
	// on the machine, or one left behind by a panel that was restored from a
	// backup, shows up.
	DriftUnexpected DriftKind = "unexpected"
)

// Drift is one difference between what a node should be running and what it is.
type Drift struct {
	Node   string    `json:"node"`
	Tunnel string    `json:"tunnel"`
	Kind   DriftKind `json:"kind"`
	// Detail is the sentence an operator reads. It says what differs, not what
	// to do about it.
	Detail string `json:"detail"`
}

// Desired is the panel's record of what its fleet should be running.
//
// The zero value is not usable; use NewDesired, which is what the panel's
// constructor calls. It is safe for concurrent use: the panel reads it from
// every request and writes it from the fleet operations.
type Desired struct {
	mu    sync.Mutex
	path  string
	nodes map[string]map[string]TunnelIntent // node -> tunnel -> intent
}

// NewDesired loads the intent from disk, or starts empty when there is none.
//
// A file that cannot be read is an empty intent rather than an error. The panel
// has to come up: an intent nobody can read makes every node look unexpected,
// which is noise, while a panel that refuses to start makes the fleet
// unreachable — and the fleet is how somebody would fix it.
func NewDesired() *Desired { return NewDesiredAt(desiredPath) }

// NewDesiredAt is NewDesired against a chosen file.
//
// It exists for the panel's own tests, which drive the drift report through the
// handler and cannot write to /etc. The alternative was a package-level path
// variable that any code could move, which is a bigger hole than a constructor
// argument nobody in the product passes.
func NewDesiredAt(path string) *Desired {
	d := &Desired{path: path, nodes: map[string]map[string]TunnelIntent{}}
	d.load()
	return d
}

func (d *Desired) load() {
	data, err := os.ReadFile(d.path)
	if err != nil {
		return
	}
	var stored map[string]map[string]TunnelIntent
	if err := json.Unmarshal(data, &stored); err != nil || stored == nil {
		return
	}
	d.nodes = stored
}

// save writes the intent out. Failures are returned but never stop the
// operation that produced them: an intent the panel could not write is a
// missing record, and refusing to create the tunnel because of it would trade
// a diagnostic for the feature.
func (d *Desired) save() error {
	data, err := json.MarshalIndent(d.nodes, "", "  ")
	if err != nil {
		return err
	}
	return app.WriteFileAtomic(d.path, data, 0600)
}

// Record writes down that a node is expected to run this tunnel.
func (d *Desired) Record(nodeName string, t TunnelIntent) error {
	if nodeName == "" || t.Name == "" {
		return fmt.Errorf("an intent needs a node and a tunnel name")
	}
	t.Recorded = time.Now().Unix()

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.nodes[nodeName] == nil {
		d.nodes[nodeName] = map[string]TunnelIntent{}
	}
	d.nodes[nodeName][t.Name] = t
	return d.save()
}

// SetRunning records that a tunnel was deliberately started or stopped, so that
// finding it in that state is not reported as drift.
func (d *Desired) SetRunning(nodeName, tunnel string, running bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.nodes[nodeName][tunnel]
	if !ok {
		// Nothing was expected here, so there is nothing to correct. Silence is
		// right: the panel drives tunnels it did not create, and inventing an
		// intent from a start button would record a guess.
		return nil
	}
	t.Running = running
	t.Recorded = time.Now().Unix()
	d.nodes[nodeName][tunnel] = t
	return d.save()
}

// Forget drops one tunnel's intent, which is what a deliberate delete means.
func (d *Desired) Forget(nodeName, tunnel string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.nodes[nodeName] == nil {
		return nil
	}
	delete(d.nodes[nodeName], tunnel)
	if len(d.nodes[nodeName]) == 0 {
		delete(d.nodes, nodeName)
	}
	return d.save()
}

// ForgetNode drops everything expected of a node, for a node leaving the fleet.
func (d *Desired) ForgetNode(nodeName string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.nodes, nodeName)
	return d.save()
}

// Intent returns what a node is expected to run, in name order.
func (d *Desired) Intent(nodeName string) []TunnelIntent {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]TunnelIntent, 0, len(d.nodes[nodeName]))
	for _, t := range d.nodes[nodeName] {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Nodes returns every node something is expected of.
func (d *Desired) Nodes() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.nodes))
	for name := range d.nodes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Reconcile compares what a node should be running with what it reports, and
// returns the differences in a stable order.
//
// It is a pure function of its two arguments and touches nothing: the whole
// point of the split is that the comparison can be tested without a fleet, and
// that a report can be produced from a list somebody already fetched rather
// than by asking again.
func (d *Desired) Reconcile(nodeName string, actual []node.TunnelState) []Drift {
	d.mu.Lock()
	want := map[string]TunnelIntent{}
	for name, t := range d.nodes[nodeName] {
		want[name] = t
	}
	d.mu.Unlock()

	have := map[string]node.TunnelState{}
	for _, t := range actual {
		have[t.Name] = t
	}

	var out []Drift
	add := func(tunnel string, kind DriftKind, format string, a ...any) {
		out = append(out, Drift{Node: nodeName, Tunnel: tunnel, Kind: kind,
			Detail: fmt.Sprintf(format, a...)})
	}

	for name, w := range want {
		got, ok := have[name]
		if !ok {
			add(name, DriftMissing,
				"this panel created %s on %s and the server does not have it — "+
					"it was removed there, or an apply reported success and did not last",
				name, nodeName)
			continue
		}
		// A tunnel under the same name that is not the same tunnel is worth
		// more than a stopped one: the pair no longer meets.
		if w.TunnelPort != "" && got.TunnelPort != "" && w.TunnelPort != got.TunnelPort {
			add(name, DriftChanged,
				"%s meets its pair on port %s here and on port %s there — the two ends "+
					"no longer name the same port", name, w.TunnelPort, got.TunnelPort)
		}
		if w.Role != "" && got.Role != "" && !strings.EqualFold(w.Role, got.Role) {
			add(name, DriftChanged,
				"%s was created as the %s end and the server holds the %s end",
				name, w.Role, got.Role)
		}
		switch {
		case w.Running && !got.Active:
			add(name, DriftStopped,
				"%s is not running on %s, and nothing here asked for it to be stopped",
				name, nodeName)
		case !w.Running && got.Active:
			add(name, DriftRunning,
				"%s was stopped from here and is running on %s", name, nodeName)
		}
	}

	for name := range have {
		if _, ok := want[name]; !ok {
			add(name, DriftUnexpected,
				"%s is running on %s and this panel did not create it — made on the "+
					"machine itself, or left from a panel restored from a backup",
				name, nodeName)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Tunnel != out[j].Tunnel {
			return out[i].Tunnel < out[j].Tunnel
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

package manage

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/backpack/backpack/internal/app"
)

// Which tunnels have their other end on a managed server.
//
// A tunnel built across a node is one tunnel in two places, and the panel has
// to keep knowing that after the form is closed. Without it the second half of
// the feature cannot exist: an edit here would change this end and leave the
// far one on the configuration it was created with, which is the exact
// disagreement the whole thing was built to prevent.
//
// It lives in this package rather than in internal/node — which is where the
// fleet itself is kept — for two reasons. That package already imports this
// one, so putting it there and reading it from here would be a cycle. And the
// CLI needs to read it: a tunnel edited from the menu cannot push anything,
// because the channel to the node belongs to the running panel process, so the
// least the menu can do is say so rather than quietly breaking the pair.
//
// It is a record of where a tunnel's twin is, not a claim that it is reachable.
// Nothing here talks to anything.

// NodePairPath is the file. It is not secret — it holds names — but it is
// written with the same permissions as its neighbours.
var NodePairPath = app.ConfigDir + "/node-pairs.json"

// Pair is where one tunnel's other end lives.
type Pair struct {
	Node string `json:"node"`

	// PeerName is what that end is called over there.
	//
	// It is derived from this one's — see peerName — but it is recorded rather
	// than recomputed, because every operation that reaches across (restart,
	// stop, start) has to name it, and a rule re-applied in four places is a
	// rule that will disagree with itself in one of them.
	PeerName string `json:"peerName,omitempty"`
}

type pairFile struct {
	// Pairs maps a tunnel name to where its other end is.
	Pairs map[string]Pair `json:"pairs,omitempty"`
}

var pairMu sync.Mutex

func loadPairs() pairFile {
	var f pairFile
	if data, err := os.ReadFile(NodePairPath); err == nil {
		json.Unmarshal(data, &f)
	}
	if f.Pairs == nil {
		f.Pairs = map[string]Pair{}
	}
	return f
}

func savePairs(f pairFile) error {
	if len(f.Pairs) == 0 {
		// Nothing left to remember. Removing the file rather than leaving an
		// empty object means a server that has never used the feature and one
		// that has stopped using it look the same on disk.
		err := os.Remove(NodePairPath)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	data, _ := json.MarshalIndent(f, "", "  ")
	return app.WriteFileAtomic(NodePairPath, data, 0600)
}

// NoteNodePair records that a tunnel's other end lives on a node, and what it
// is called there.
//
// PeerName is the one field here that the far machine chose, and this file
// outlives the connection it came over: what is written now is read back on
// every poll and put on screen long afterwards. So it is checked against the
// same rule a name created here has to pass, and dropped rather than stored
// when it fails.
//
// The pairing itself is still kept when that happens. The tunnel really does
// have its other end on that server — losing the whole record over a name
// would take the fleet's edits, logs and speed test with it — and every use of
// PeerName already falls back to the local name when it is empty.
func NoteNodePair(tunnel, node, peerName string) error {
	tunnel, node = strings.TrimSpace(tunnel), strings.TrimSpace(node)
	if tunnel == "" || node == "" {
		return nil
	}
	peerName = strings.TrimSpace(peerName)
	if peerName != "" && !validName(peerName) {
		peerName = ""
	}
	pairMu.Lock()
	defer pairMu.Unlock()
	f := loadPairs()
	f.Pairs[tunnel] = Pair{Node: node, PeerName: peerName}
	return savePairs(f)
}

// NodeFor returns the node a tunnel's other end is on.
func NodeFor(tunnel string) (string, bool) {
	p, ok := PairFor(tunnel)
	return p.Node, ok
}

// PairFor returns where a tunnel's other end is and what it is called there.
func PairFor(tunnel string) (Pair, bool) {
	pairMu.Lock()
	defer pairMu.Unlock()
	p, ok := loadPairs().Pairs[strings.TrimSpace(tunnel)]
	return p, ok && p.Node != ""
}

// ForgetNodePair drops one tunnel's pairing.
func ForgetNodePair(tunnel string) error {
	pairMu.Lock()
	defer pairMu.Unlock()
	f := loadPairs()
	delete(f.Pairs, strings.TrimSpace(tunnel))
	return savePairs(f)
}

// ForgetNodePairs drops every pairing for a node, for when it is removed from
// the fleet. The tunnels stay exactly as they are on both machines; what is
// forgotten is only that this panel can still reach one end of them.
func ForgetNodePairs(node string) error {
	pairMu.Lock()
	defer pairMu.Unlock()
	f := loadPairs()
	for tunnel, p := range f.Pairs {
		if strings.EqualFold(p.Node, node) {
			delete(f.Pairs, tunnel)
		}
	}
	return savePairs(f)
}

// TunnelsOnNode lists the tunnels this panel built on one node, sorted.
func TunnelsOnNode(node string) []string {
	pairMu.Lock()
	defer pairMu.Unlock()
	var out []string
	for tunnel, p := range loadPairs().Pairs {
		if strings.EqualFold(p.Node, node) {
			out = append(out, tunnel)
		}
	}
	sort.Strings(out)
	return out
}

package node

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// StorePath is where the panel keeps its side of the fleet: the hub key, the
// nodes it has enrolled, and the tokens it is waiting to have redeemed.
//
// It sits beside webui.json and is written with the same permissions and for
// the same reason — every secret that lets a machine act on this fleet is in
// here, so it is root-only and never world-readable, even briefly.
var StorePath = app.ConfigDir + "/nodes.json"

// Node is one registered server.
type Node struct {
	// Name is what the operator called it and how every other part of the
	// panel refers to it. It is fixed at enrolment: renaming would strand the
	// tunnels already pointing at it.
	Name string `json:"name"`

	// Key is the node's credential. Comparing it is a constant-time operation
	// (see ByKey) because a comparison that returns early leaks, one byte at a
	// time, to anyone who can time a connection.
	Key string `json:"key"`

	// Port is the listener this server dials, and only this server.
	//
	// One port for the whole fleet would work — it is a listener, and many
	// peers on one port is what a listener is for. A port each buys something
	// that shape cannot: the port is a second, independent fact about who is
	// calling. A connection arriving on this port carrying another node's
	// credential is refused for that reason alone, so a leaked key is useless
	// without also knowing which door it belongs to.
	//
	// The cost is that the operator opens a port per server in the firewall.
	Port int `json:"port"`

	Enrolled int64 `json:"enrolled"`           // unix seconds
	LastSeen int64 `json:"lastSeen,omitempty"` // unix seconds, updated on auth

	// Info is what the node last said about itself. It is stored rather than
	// asked for on demand so the fleet screen can draw a node that is offline.
	Info Info `json:"info,omitempty"`
}

// Pending is an enrolment token that has been issued and not yet redeemed.
type Pending struct {
	Name    string `json:"name"`
	Token   string `json:"token"`
	Port    int    `json:"port"`
	Created int64  `json:"created"`
}

// Expired reports whether a token has been outstanding too long.
//
// A token is one line pasted into one terminal, and the gap between generating
// it and running the command is a minute. Anything still unredeemed a day later
// is a token that went somewhere other than the server it was meant for — into
// a chat log, a screenshot, a support thread — and the only safe assumption
// about a secret that has been sitting in one of those is that it is no longer
// a secret.
func (p Pending) Expired(now time.Time) bool {
	return now.Unix()-p.Created > int64(enrollTTL/time.Second)
}

const enrollTTL = 24 * time.Hour

// Store is the whole persisted state.
type Store struct {
	// HubKey is the Noise pre-shared key every node uses to reach this panel.
	// It authorises nothing on its own; see the package comment.
	HubKey string `json:"hubKey,omitempty"`

	// Enabled is whether the panel accepts managed servers at all. Each one
	// has its own port; this is the switch over all of them.
	Enabled bool `json:"enabled,omitempty"`

	Nodes   []Node    `json:"nodes,omitempty"`
	Pending []Pending `json:"pending,omitempty"`
}

// storeMu serialises read-modify-write cycles. Two enrolments landing together
// would otherwise each read the file, add their own node, and write back — and
// whichever wrote second would silently drop the other.
var storeMu sync.Mutex

// LoadStore reads the persisted state. A missing or unreadable file is an empty
// fleet, not an error: the panel has to start on a server that has never used
// this feature.
func LoadStore() Store {
	var s Store
	if data, err := os.ReadFile(StorePath); err == nil {
		json.Unmarshal(data, &s)
	}
	return s
}

// SaveStore persists the state, root-only.
func SaveStore(s Store) error {
	data, _ := json.MarshalIndent(s, "", "  ")
	return app.WriteFileAtomic(StorePath, data, 0600)
}

// update runs fn against the store under the lock and saves the result.
func update(fn func(*Store) error) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	s := LoadStore()
	if err := fn(&s); err != nil {
		return err
	}
	return SaveStore(s)
}

// randomKey returns n bytes of hex.
func randomKey(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is not a condition to paper over with a weaker
		// source: every secret in this file would be predictable.
		panic("node: no entropy available: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// EnsureHubKey returns the panel's hub key, generating one the first time.
func EnsureHubKey() (string, error) {
	var key string
	err := update(func(s *Store) error {
		if s.HubKey == "" {
			s.HubKey = randomKey(16)
		}
		key = s.HubKey
		return nil
	})
	return key, err
}

// SetEnabled records whether the panel accepts managed servers.
func SetEnabled(on bool) error {
	return update(func(s *Store) error {
		s.Enabled = on
		return nil
	})
}

// PortTaken reports whether a port is already promised to a node or a pending
// enrolment. Two servers on one port would both be told to dial it and only one
// listener could exist, so the second would never connect and nothing would say
// why.
func PortTaken(port int) (string, bool) {
	s := LoadStore()
	for _, n := range s.Nodes {
		if n.Port == port {
			return n.Name, true
		}
	}
	for _, p := range s.Pending {
		if p.Port == port {
			return p.Name, true
		}
	}
	return "", false
}

// Ports lists every port the hub should be listening on, with the node each one
// belongs to.
func Ports() map[int]string {
	s := LoadStore()
	out := map[int]string{}
	now := time.Now()
	for _, n := range s.Nodes {
		if n.Port > 0 {
			out[n.Port] = n.Name
		}
	}
	for _, p := range s.Pending {
		if p.Port > 0 && !p.Expired(now) {
			out[p.Port] = p.Name
		}
	}
	return out
}

// nameRx is deliberately strict. A node name reaches a systemd unit name and a
// config path on the far machine, so anything that could be read as a path
// separator or a shell metacharacter is refused at the door rather than escaped
// at each of the places it is later used.
func validName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("the node needs a name")
	}
	if len(name) > 40 {
		return fmt.Errorf("that name is longer than 40 characters")
	}
	for _, r := range name {
		ok := r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("a node name can hold letters, digits, - and _ only")
		}
	}
	return nil
}

// NewEnrollToken issues a single-use token for a node that is about to be set
// up, and returns the token together with the hub key the command also needs.
func NewEnrollToken(name string, port int) (token, hub string, err error) {
	name = strings.TrimSpace(name)
	if err := validName(name); err != nil {
		return "", "", err
	}
	if port < 1 || port > 65535 {
		return "", "", fmt.Errorf("choose a port between 1 and 65535")
	}
	if on, taken := PortTaken(port); taken {
		return "", "", fmt.Errorf("port %d is already %s's", port, on)
	}
	err = update(func(s *Store) error {
		for _, n := range s.Nodes {
			if strings.EqualFold(n.Name, name) {
				return fmt.Errorf("a node called %q is already enrolled", name)
			}
		}
		if s.HubKey == "" {
			s.HubKey = randomKey(16)
		}
		hub = s.HubKey
		token = randomKey(16)

		// Replace any earlier token for this name rather than accumulating
		// them: an operator who generates a second command for a server they
		// have not set up yet means the first one is not going to be used, and
		// leaving it live is one more valid secret for no gain.
		kept := s.Pending[:0]
		now := time.Now()
		for _, p := range s.Pending {
			if !strings.EqualFold(p.Name, name) && !p.Expired(now) {
				kept = append(kept, p)
			}
		}
		s.Pending = append(kept, Pending{Name: name, Token: token, Port: port, Created: now.Unix()})
		return nil
	})
	return token, hub, err
}

// Redeem trades a valid enrolment token for a node key, burning the token.
func Redeem(token string, info Info) (Node, error) {
	var out Node
	err := update(func(s *Store) error {
		now := time.Now()
		idx := -1
		for i, p := range s.Pending {
			if subtle.ConstantTimeCompare([]byte(p.Token), []byte(token)) == 1 {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("that setup token is not valid")
		}
		p := s.Pending[idx]
		s.Pending = append(s.Pending[:idx], s.Pending[idx+1:]...)
		if p.Expired(now) {
			return fmt.Errorf("that setup token has expired — generate a new one")
		}
		for _, n := range s.Nodes {
			if strings.EqualFold(n.Name, p.Name) {
				return fmt.Errorf("a node called %q is already enrolled", p.Name)
			}
		}
		info.Name = p.Name
		out = Node{
			Name:     p.Name,
			Key:      randomKey(24),
			Port:     p.Port,
			Enrolled: now.Unix(),
			LastSeen: now.Unix(),
			Info:     info,
		}
		s.Nodes = append(s.Nodes, out)
		return nil
	})
	if err != nil {
		return Node{}, err
	}
	return out, nil
}

// ByKey finds the node holding a key and records that it has been seen.
//
// The comparison runs over every node rather than stopping at the first match,
// so the time it takes does not depend on where in the list the key sits.
func ByKey(key string) (Node, bool) {
	if key == "" {
		return Node{}, false
	}
	storeMu.Lock()
	s := LoadStore()
	hit := -1
	for i, n := range s.Nodes {
		if subtle.ConstantTimeCompare([]byte(n.Key), []byte(key)) == 1 {
			hit = i
		}
	}
	if hit < 0 {
		storeMu.Unlock()
		return Node{}, false
	}
	s.Nodes[hit].LastSeen = time.Now().Unix()
	out := s.Nodes[hit]
	_ = SaveStore(s)
	storeMu.Unlock()
	return out, true
}

// NoteInfo stores what a node last reported about itself.
func NoteInfo(name string, info Info) error {
	return update(func(s *Store) error {
		for i := range s.Nodes {
			if s.Nodes[i].Name == name {
				info.Name = name
				s.Nodes[i].Info = info
				s.Nodes[i].LastSeen = time.Now().Unix()
				return nil
			}
		}
		return nil
	})
}

// List returns the enrolled nodes, newest last, with keys blanked.
//
// Nothing that reads this list needs the credential, and a struct that carries
// one travels: into a JSON response, into a log line, into a bug report. The
// one place that does need it looks it up by name.
func List() []Node {
	s := LoadStore()
	out := make([]Node, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		n.Key = ""
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Enrolled < out[j].Enrolled })
	return out
}

// Find returns one node by name, with its key blanked, as List does.
func Find(name string) (Node, bool) {
	for _, n := range List() {
		if strings.EqualFold(n.Name, name) {
			return n, true
		}
	}
	return Node{}, false
}

// PendingList returns the tokens still waiting to be redeemed, without them.
func PendingList() []Pending {
	s := LoadStore()
	now := time.Now()
	out := make([]Pending, 0, len(s.Pending))
	for _, p := range s.Pending {
		if p.Expired(now) {
			continue
		}
		p.Token = "" // the port is not secret; the token is
		out = append(out, p)
	}
	return out
}

// Remove revokes a node. Its key stops working immediately; the tunnels it is
// already running keep running, because they are systemd services on that
// machine and have nothing to do with this channel.
func Remove(name string) error {
	return update(func(s *Store) error {
		kept := s.Nodes[:0]
		found := false
		for _, n := range s.Nodes {
			if strings.EqualFold(n.Name, name) {
				found = true
				continue
			}
			kept = append(kept, n)
		}
		s.Nodes = kept

		// Removing a node that has not finished setting up has to withdraw the
		// token as well, or the command the operator already pasted somewhere
		// would still enrol it.
		before := len(s.Pending)
		pk := s.Pending[:0]
		for _, p := range s.Pending {
			if !strings.EqualFold(p.Name, name) {
				pk = append(pk, p)
			}
		}
		s.Pending = pk
		if !found && len(pk) == before {
			return fmt.Errorf("no node called %q", name)
		}
		return nil
	})
}

package node

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// StorePath is where the panel keeps its side of the fleet: the servers it
// manages and how to reach them.
//
// It sits beside webui.json and is written with the same permissions and for
// the same reason — a root password for another machine is in here, so it is
// root-only and never world-readable, even briefly.
var StorePath = app.ConfigDir + "/nodes.json"

// Node is one managed server.
type Node struct {
	// Name is what the operator called it and how every other part of the
	// panel refers to it. It is fixed when the server is added: renaming would
	// strand the tunnels already pointing at it.
	Name string `json:"name"`

	// Host is its address, and SSHPort the port sshd answers on. The panel
	// dials out to these; nothing is opened here.
	Host    string `json:"host"`
	SSHPort int    `json:"sshPort,omitempty"` // 0 means 22

	// User and Password are the login. Root, in practice: the panel installs
	// services and writes into /etc on that machine, which is what managing it
	// means.
	User     string `json:"user"`
	Password string `json:"password,omitempty"`

	// Fingerprint is the SHA-256 of the host key this server presented the
	// first time it answered. Every connection after that must match it. Empty
	// until the first successful call.
	Fingerprint string `json:"fingerprint,omitempty"`

	Added    int64 `json:"added"`              // unix seconds
	LastSeen int64 `json:"lastSeen,omitempty"` // unix seconds

	// Info is what the server last said about itself. It is stored rather than
	// asked for on demand so the fleet screen can draw a server that is down.
	Info Info `json:"info,omitempty"`
}

// Store is the whole persisted state.
//
// There is no "enabled" here any more. It existed to say whether the panel
// should open its listeners; the panel dials out now, so an empty fleet is
// already the off state and a switch for it was one more thing to be wrong.
type Store struct {
	Nodes []Node `json:"nodes,omitempty"`
}

// storeMu serialises read-modify-write cycles. Two writes landing together
// would otherwise each read the file, make their change, and write back — and
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

// nameRx is deliberately strict. A node name reaches a systemd unit name and a
// config path on the far machine, so anything that could be read as a path
// separator or a shell metacharacter is refused at the door rather than escaped
// at each of the places it is later used.
func validName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("the server needs a name")
	}
	if len(name) > 40 {
		return fmt.Errorf("that name is longer than 40 characters")
	}
	for _, r := range name {
		ok := r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("a server name can hold letters, digits, - and _ only")
		}
	}
	return nil
}

// Add records a server the panel will manage over SSH.
//
// Nothing is contacted here. Whether the address answers, whether the password
// is right and whether Backpack is installed there are all questions with the
// same answer — try it — and the caller does that once, so a failure is
// reported as itself rather than as four checks that each half-worked.
func Add(name, host string, sshPort int, user, password string) (Node, error) {
	name = strings.TrimSpace(name)
	if err := validName(name); err != nil {
		return Node{}, err
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return Node{}, fmt.Errorf("the server needs an address")
	}
	if strings.ContainsAny(host, " \t/\\") {
		return Node{}, fmt.Errorf("%q is not an address", host)
	}
	if sshPort < 0 || sshPort > 65535 {
		return Node{}, fmt.Errorf("choose an SSH port between 1 and 65535")
	}
	user = strings.TrimSpace(user)
	if user == "" {
		return Node{}, fmt.Errorf("the server needs a username")
	}
	if password == "" {
		return Node{}, fmt.Errorf("the server needs a password")
	}

	var out Node
	err := update(func(s *Store) error {
		for _, n := range s.Nodes {
			if strings.EqualFold(n.Name, name) {
				return fmt.Errorf("a server called %q is already in the fleet", name)
			}
			if strings.EqualFold(n.Host, host) && n.SSHPort == sshPort {
				return fmt.Errorf("%s is already in the fleet, as %q", host, n.Name)
			}
		}
		out = Node{
			Name: name, Host: host, SSHPort: sshPort, User: user, Password: password,
			Added: time.Now().Unix(),
		}
		s.Nodes = append(s.Nodes, out)
		return nil
	})
	if err != nil {
		return Node{}, err
	}
	return out, nil
}

// blank returns a copy with the password and host key removed.
//
// Nothing that reads the fleet list needs the credential, and a struct that
// carries one travels: into a JSON response, into a log line, into a bug
// report. The one place that does need it looks it up by name.
func blank(n Node) Node {
	n.Password = ""
	return n
}

// List returns the fleet, oldest first, without credentials.
func List() []Node {
	s := LoadStore()
	out := make([]Node, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		out = append(out, blank(n))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Added < out[j].Added })
	return out
}

// Find returns one server by name, without its credential.
func Find(name string) (Node, bool) {
	n, ok := findWithSecret(name)
	if !ok {
		return Node{}, false
	}
	return blank(n), true
}

// findWithSecret returns one server as stored, credential included. Unexported
// on purpose: everything outside this package wants Find.
func findWithSecret(name string) (Node, bool) {
	for _, n := range LoadStore().Nodes {
		if strings.EqualFold(n.Name, name) {
			return n, true
		}
	}
	return Node{}, false
}

// NoteFingerprint records the host key a server presented, the first time it
// answered. It refuses to overwrite one that is already there: a key that
// changed is a thing to report, not to accept quietly.
func NoteFingerprint(name, fp string) error {
	return update(func(s *Store) error {
		for i := range s.Nodes {
			if !strings.EqualFold(s.Nodes[i].Name, name) {
				continue
			}
			if s.Nodes[i].Fingerprint != "" && s.Nodes[i].Fingerprint != fp {
				return ErrHostKeyChanged{Name: name, Had: s.Nodes[i].Fingerprint, Got: fp}
			}
			s.Nodes[i].Fingerprint = fp
			return nil
		}
		return fmt.Errorf("no server called %q", name)
	})
}

// NoteInfo stores what a server last reported about itself.
func NoteInfo(name string, info Info) error {
	return update(func(s *Store) error {
		for i := range s.Nodes {
			if strings.EqualFold(s.Nodes[i].Name, name) {
				info.Name = s.Nodes[i].Name
				s.Nodes[i].Info = info
				s.Nodes[i].LastSeen = time.Now().Unix()
				return nil
			}
		}
		return fmt.Errorf("no server called %q", name)
	})
}

// SetCredentials changes how a server is reached. Changing the address clears
// the host key: a different machine is entitled to a different one.
func SetCredentials(name, host string, sshPort int, user, password string) error {
	return update(func(s *Store) error {
		for i := range s.Nodes {
			if !strings.EqualFold(s.Nodes[i].Name, name) {
				continue
			}
			if host != "" && !strings.EqualFold(host, s.Nodes[i].Host) {
				s.Nodes[i].Host = host
				s.Nodes[i].Fingerprint = ""
			}
			if sshPort > 0 {
				s.Nodes[i].SSHPort = sshPort
			}
			if user != "" {
				s.Nodes[i].User = user
			}
			if password != "" {
				s.Nodes[i].Password = password
			}
			return nil
		}
		return fmt.Errorf("no server called %q", name)
	})
}

// Remove takes a server out of the fleet. Its tunnels keep running there,
// because they are systemd services on that machine and have nothing to do with
// this panel being able to reach it.
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
		if !found {
			return fmt.Errorf("no server called %q", name)
		}
		return nil
	})
}

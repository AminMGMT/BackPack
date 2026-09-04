package webui

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/node"
)

// Managed servers.
//
// A node is a server that has been told, once, which panel manages it, and has
// been connecting outward to that panel ever since. From here it looks like a
// place a tunnel can be put: the fleet screen lists them, and the setup form
// can build both ends of a tunnel in one submission instead of leaving the
// operator to repeat every paired value on a second machine by hand.
//
// The panel holds no login for any of them. See internal/node.

// fleet owns the panel's side of the managed servers.
//
// There is no lifetime to manage any more. The channel this replaces had a
// listener per server that had to be opened, moved when its port changed, and
// closed when the server went — three things that could each be wrong on their
// own, and each of which left a server listed here and unreachable. The panel
// dials out now, so the only state worth holding is the connections it is
// reusing, and those look after themselves.
type fleet struct {
	mu  sync.Mutex
	run node.Runner
}

// start makes the runner if there is none. It contacts nothing: whether a
// server answers is a question asked when something is asked of it.
func (f *fleet) start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run == nil {
		f.run = node.NewSSHRunner(func(m string) { log.Printf("fleet: %s", m) })
	}
	return nil
}

func (f *fleet) stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Whatever it is holding open is dropped; an interface that cannot be
	// closed simply has nothing to drop.
	if c, ok := f.run.(interface{ Close() }); ok {
		c.Close()
	}
	f.run = nil
}

func (f *fleet) get() node.Runner {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.run
}

// nodeView is one row of the fleet screen.
type nodeView struct {
	Name string `json:"name"`

	// How the panel reaches it. The password is never sent back.
	Host    string `json:"host"`
	SSHPort int    `json:"sshPort,omitempty"`
	User    string `json:"user"`

	// Fingerprint is the host key this server is known by. Shown because a
	// server whose key has changed refuses to answer, and the operator has no
	// way to tell that from a server that is simply down unless it is here.
	Fingerprint string `json:"fingerprint,omitempty"`

	Online   bool      `json:"online"`
	Why      string    `json:"why,omitempty"` // why not, when it is not
	Added    int64     `json:"added"`
	LastSeen int64     `json:"lastSeen,omitempty"`
	Info     node.Info `json:"info,omitempty"`

	// Tunnels are the ones this panel built there. It is what this panel
	// remembers, not what the server has: a tunnel someone set up on that
	// machine by hand is real and is not in this list.
	Tunnels []string `json:"tunnels,omitempty"`
}

// handleNodes serves the fleet and the actions on it.
func (s *server) handleNodes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.writeNodeState(w)
	case http.MethodPost:
		s.nodeAction(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) writeNodeState(w http.ResponseWriter) { s.writeNodeStateWith(w, "") }

func (s *server) writeNodeStateWith(w http.ResponseWriter, warning string) {
	run := s.nodes.get()

	// Every card asks whether its server is up, and asking means a connection.
	// Doing that one after another would make the page take as long as the
	// slowest server times the number of them, so they are asked together and
	// the runner's own short memory keeps a poll from costing anything at all.
	list := node.List()
	rows := make([]nodeView, len(list))
	var wg sync.WaitGroup
	for i, n := range list {
		rows[i] = nodeView{
			Name: n.Name, Host: n.Host, SSHPort: n.SSHPort, User: n.User,
			Fingerprint: n.Fingerprint, Added: n.Added, LastSeen: n.LastSeen,
			Info: n.Info, Tunnels: manage.TunnelsOnNode(n.Name),
		}
		if run == nil {
			continue
		}
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			rows[i].Online, rows[i].Why = run.Reachable(name)
		}(i, n.Name)
	}
	wg.Wait()

	out := map[string]any{
		"enabled": run != nil,
		"nodes":   rows,
	}
	if warning != "" {
		out["warning"] = warning
	}
	writeJSON(w, out)
}

func (s *server) nodeAction(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	switch r.FormValue("action") {
	case "enable":
		if err := s.nodes.start(); err != nil {
			// Some ports came up and some did not. Reported rather than
			// refused: the servers that can connect should be able to.
			s.writeNodeStateWith(w, err.Error())
			return
		}
		if err := node.SetEnabled(true); err != nil {
			http.Error(w, "could not save: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.writeNodeState(w)

	case "disable":
		// The servers stay in the fleet. Turning it off is "stop reaching out",
		// not "forget the fleet" — an operator who wanted the second one would
		// remove the servers.
		s.nodes.stop()
		if err := node.SetEnabled(false); err != nil {
			http.Error(w, "could not save: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.writeNodeState(w)

	case "add":
		if s.nodes.get() == nil {
			http.Error(w, "turn Managed servers on first", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		host := strings.TrimSpace(r.FormValue("host"))
		user := strings.TrimSpace(r.FormValue("user"))
		if user == "" {
			user = "root"
		}
		port := 22
		if v := strings.TrimSpace(r.FormValue("sshPort")); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 65535 {
				http.Error(w, "choose an SSH port between 1 and 65535", http.StatusBadRequest)
				return
			}
			port = n
		}
		if _, err := node.Add(name, host, port, user, r.FormValue("password")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Reached once, now, while the operator is looking at the form.
		//
		// The alternative is to save the details and let the first real
		// operation discover that the password is wrong — which is the shape
		// the old flow had, and it is why a server could sit in the fleet doing
		// nothing with nothing saying why. A server that cannot be reached is
		// taken back out, because a fleet entry that has never worked is not a
		// server, it is a typo.
		var info node.Info
		err := s.nodes.get().Call(name, node.OpHello, nil, &info)

		// A machine that answers SSH and has no Backpack on it is the ordinary
		// case, not a failure: it is a server the operator has just bought. The
		// panel installs it rather than sending them to a terminal on it, which
		// is the whole point of managing it from here.
		if err != nil && strings.Contains(err.Error(), "not installed") {
			inst, ok := s.nodes.get().(interface{ Install(string) (string, error) })
			if ok && r.FormValue("install") != "0" {
				if _, ierr := inst.Install(name); ierr != nil {
					_ = node.Remove(name)
					http.Error(w, ierr.Error(), http.StatusBadGateway)
					return
				}
				err = s.nodes.get().Call(name, node.OpHello, nil, &info)
			}
		}
		if err != nil {
			_ = node.Remove(name)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		_ = node.NoteInfo(name, info)
		s.writeNodeState(w)

	case "credentials":
		// The address, the login or the password changed. The host key is
		// dropped with the address inside SetCredentials, and the connection is
		// dropped here so the next call dials with what was just saved.
		name := strings.TrimSpace(r.FormValue("name"))
		port := 0
		if v := strings.TrimSpace(r.FormValue("sshPort")); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 65535 {
				http.Error(w, "choose an SSH port between 1 and 65535", http.StatusBadRequest)
				return
			}
			port = n
		}
		if err := node.SetCredentials(name, strings.TrimSpace(r.FormValue("host")), port,
			strings.TrimSpace(r.FormValue("user")), r.FormValue("password")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if run := s.nodes.get(); run != nil {
			run.Forget(name)
		}
		s.writeNodeState(w)

	case "upgrade":
		// One click, from here, for a server the operator may never log into.
		// It is the same installer that put Backpack there: it fetches the
		// current release, replaces the binary and restarts what was running.
		run := s.nodes.get()
		up, ok := run.(interface{ Upgrade(string) (string, error) })
		if !ok {
			http.Error(w, "this panel cannot upgrade servers", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		if _, err := up.Upgrade(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// Read back what it is now, so the card does not keep showing the
		// version it had before.
		var info node.Info
		if err := run.Call(name, node.OpHello, nil, &info); err == nil {
			_ = node.NoteInfo(name, info)
		}
		s.writeNodeState(w)

	case "upgradeall":
		// Every server behind this panel, in one action.
		//
		// A release lands on the panel and every managed server is then a
		// version behind, which is a thing to fix once rather than a thing to
		// remember for each of them. They are done in parallel because each is
		// minutes of download, and one that fails is reported without stopping
		// the others: a fleet where nine upgraded and one did not is a better
		// place to be than a fleet where the first failure stopped the rest.
		run := s.nodes.get()
		up, ok := run.(interface{ Upgrade(string) (string, error) })
		if !ok {
			http.Error(w, "this panel cannot upgrade servers", http.StatusBadRequest)
			return
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		var failed []string
		for _, n := range node.List() {
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				if _, err := up.Upgrade(name); err != nil {
					mu.Lock()
					failed = append(failed, name+" ("+err.Error()+")")
					mu.Unlock()
					return
				}
				var info node.Info
				if err := run.Call(name, node.OpHello, nil, &info); err == nil {
					_ = node.NoteInfo(name, info)
				}
			}(n.Name)
		}
		wg.Wait()
		if len(failed) > 0 {
			sort.Strings(failed)
			s.writeNodeStateWith(w, "could not upgrade "+strings.Join(failed, ", "))
			return
		}
		s.writeNodeState(w)

	case "remove":
		name := strings.TrimSpace(r.FormValue("name"))
		if err := node.Remove(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// The tunnels on it stay exactly as they are, on both machines. What is
		// dropped is only this panel's record that it could still reach one end
		// of them — keeping that would make a later edit report a node that is
		// no longer in the fleet.
		_ = manage.ForgetNodePairs(name)
		if run := s.nodes.get(); run != nil {
			run.Forget(name)
		}
		s.writeNodeState(w)

	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// panelHost is the address a foreign server should use to reach this panel.
//
// The host in the request is tried first, and not as a shortcut. It is the
// address the operator is reaching this page on right now, so it is a fact:
// something outside this machine sent a packet to it and arrived. Asking a
// remote service for "my public IP" is a guess by comparison — it answers with
// the address the panel's own outbound traffic appears from, which on a box
// behind NAT, or one with several addresses, need not be an address anything
// can dial back on.
//
// It is also the difference between an instant answer and a wait. The lookup
// tries five services with their own timeouts, so on a server with no route out
// — which is the normal state of the machine this panel runs on — pressing the
// button would hang for the better part of a minute before producing "-".
//
// The lookup is still there for the case the request host cannot be used: a
// panel reached over the LAN, through a tunnel, or on loopback gives an address
// that is true here and useless on another continent.
func panelHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host != "" && !privateHost(host) {
		return host
	}
	if ip := manage.PublicIPv4(); ip != "" && ip != "-" {
		return ip
	}
	return host
}

// privateHost reports whether an address is one only this network can reach.
// A name is assumed to be public: a domain that resolves here resolves there.
func privateHost(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return strings.EqualFold(host, "localhost")
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// pairRequest is one submission of the setup form that builds both ends.
type pairRequest struct {
	Node   string                  `json:"node"`
	Kind   string                  `json:"kind"` // "reverse" or "direct"
	Tunnel *manage.NewTunnel       `json:"tunnel,omitempty"`
	Direct *manage.NewDirectTunnel `json:"direct,omitempty"`

	// PeerConn is the far end's own connectivity: the proxy it dials through,
	// the CDN edge it fronts, the interface it leaves by, its backup addresses.
	//
	// These are the settings a mirror cannot produce. Everything else about the
	// other end follows from this one — the port both must agree on, the token
	// both must hold — but which network card a machine on another continent
	// should use is a fact about that machine, and the only way to know it is
	// to be told. Without this they could only be set by logging in there,
	// which is the thing this feature exists to avoid.
	PeerConn *manage.ConnTune `json:"peerConn,omitempty"`
}

// handleNodePair creates a tunnel here and its other end on a managed server.
//
// The far end is not built from the form. It is built from this end's finished
// configuration, through exactly the path that produces a setup link: the
// tunnel is created here first, read back, mirrored, and the mirror is what
// travels. Deriving it from the form instead would mean two pieces of code
// deciding what the other side should be, and the whole class of bug this
// feature exists to remove is the two ends disagreeing.
func (s *server) handleNodePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req pairRequest
	if err := decodeJSON(w, r, &req); err != nil {
		http.Error(w, "could not read the form: "+err.Error(), http.StatusBadRequest)
		return
	}
	run := s.nodes.get()
	if run == nil {
		http.Error(w, "managed servers are turned off", http.StatusBadRequest)
		return
	}
	// Checked before anything is written. Creating this end and then finding
	// the other server unreachable leaves half a tunnel and an operator who has
	// to know that is what happened.
	if ok, why := run.Reachable(req.Node); !ok {
		msg := req.Node + " could not be reached — nothing was created"
		if why != "" {
			msg += ": " + why
		}
		http.Error(w, msg, http.StatusBadGateway)
		return
	}

	var (
		name    string
		service string
		active  bool
		err     error
	)
	switch req.Kind {
	case "direct":
		if req.Direct == nil {
			http.Error(w, "the form is missing its direct settings", http.StatusBadRequest)
			return
		}
		name = strings.TrimSpace(req.Direct.Name)
		service, active, err = manage.CreateDirectTunnel(*req.Direct)
	case "reverse", "":
		if req.Tunnel == nil {
			http.Error(w, "the form is missing its tunnel settings", http.StatusBadRequest)
			return
		}
		name = strings.TrimSpace(req.Tunnel.Name)
		service, active, err = manage.CreateTunnel(*req.Tunnel)
	default:
		http.Error(w, "unknown tunnel kind", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	peer, perr := s.pushPeerEnd(run, req.Node, name, req.PeerConn, r)
	resp := map[string]any{
		"status":  "ok",
		"name":    name,
		"service": service,
		"active":  active,
		"node":    req.Node,
	}
	if perr != nil {
		// This end is real and running; the other is not. Said plainly, with
		// what to do about it, because the panel cannot undo the half that
		// worked and should not pretend the whole thing failed.
		resp["status"] = "partial"
		resp["peerError"] = perr.Error()
		resp["peerHint"] = "This end was created. The other end was not — " +
			"open the tunnel's setup link and paste it on " + req.Node + ", or try again once it is back."
	} else {
		resp["peer"] = peer
		// Recorded only once both ends exist. A pairing written before the push
		// would send later edits at a server that never took the tunnel.
		peerName := ""
		if m, ok := peer.(map[string]any); ok {
			peerName, _ = m["peerName"].(string)
		}
		if err := manage.NoteNodePair(name, req.Node, peerName); err != nil {
			resp["pairWarning"] = "The tunnel is up on both servers, but this panel could not " +
				"record where the other end is, so edits will not carry across: " + err.Error()
		}
	}
	writeJSON(w, resp)
}

// peerConnOnNode reads back the far end's own connectivity answers.
//
// A node that cannot be reached, or has no such tunnel yet, returns nothing:
// this is a carry-forward, so having nothing to carry is an ordinary answer and
// not a reason to fail an edit that is otherwise fine.
func peerConnOnNode(run node.Runner, nodeName, tunnel string) *manage.ConnTune {
	if run == nil {
		return nil
	}
	var cur manage.TunnelSettings
	if err := run.Call(nodeName, node.OpSettings, node.NameRequest{Name: tunnel}, &cur); err != nil {
		return nil
	}
	conn := cur.Conn
	return &conn
}

// pushPeerEnd mirrors a freshly created tunnel and applies it on the node.
func (s *server) pushPeerEnd(run node.Runner, nodeName, tunnel string, peerConn *manage.ConnTune, r *http.Request) (any, error) {
	link, err := manage.ShareLinkFor(tunnel, panelHost(r))
	if err != nil {
		return nil, fmt.Errorf("could not read back the tunnel just created: %w", err)
	}
	parsed, err := manage.DecodeShareLink(link)
	if err != nil {
		return nil, err
	}
	form := manage.MirrorForPeer(parsed)

	req := node.ApplyRequest{Kind: form.Kind}
	if form.Kind == "direct" {
		d := form.ToNewDirectTunnel()
		req.Direct = &d
	} else {
		t := form.ToNewTunnel()
		// Laid over the mirror, not merged into it: these are the far end's own
		// answers and nothing on this side has an opinion to defend.
		if peerConn == nil {
			// An edit sends none, because an edit is about this end. Carrying
			// the far end's current ones across keeps a rebuild from dropping
			// settings the operator gave when the tunnel was paired.
			peerConn = peerConnOnNode(run, nodeName, form.Name)
		}
		t.Conn = peerConn
		req.Tunnel = &t
	}
	var res node.ApplyResult
	if err := run.Call(nodeName, node.OpApply, req, &res); err != nil {
		return nil, err
	}
	return map[string]any{
		"service":  res.Service,
		"active":   res.Active,
		"created":  res.Created,
		"note":     form.Note,
		"peerName": form.Name,
	}, nil
}

package webui

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/backpack/backpack/internal/app"
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

// hubRunner owns the listener's lifetime.
//
// The port is a setting the operator can change while the panel is running, so
// the listener has to be able to move; and because moving it drops every node,
// which then reconnect, that is done by cancelling one context and starting
// again rather than by anything more delicate.
type hubRunner struct {
	mu     sync.Mutex
	hub    *node.Hub
	cancel context.CancelFunc
}

// start brings the hub up and opens a listener for every server the panel knows
// about — the enrolled ones and the ones still waiting for their command to be
// run. A port that cannot be taken is reported and the rest still come up: one
// server whose port is occupied must not keep the other nine offline.
func (r *hubRunner) start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
		r.cancel, r.hub = nil, nil
	}
	key, err := node.EnsureHubKey()
	if err != nil {
		return err
	}
	h := node.NewHub(func(m string) { log.Printf("node hub: %s", m) })
	ctx, cancel := context.WithCancel(context.Background())
	if err := h.Start(ctx, key); err != nil {
		cancel()
		return err
	}
	var failed []string
	for port, name := range node.Ports() {
		if err := h.Open(port, name); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", name, err))
		}
	}
	r.hub, r.cancel = h, cancel
	if len(failed) > 0 {
		sort.Strings(failed)
		return fmt.Errorf("listening for %s failed", strings.Join(failed, ", "))
	}
	return nil
}

func (r *hubRunner) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
	r.cancel, r.hub = nil, nil
}

func (r *hubRunner) get() *node.Hub {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hub
}

// nodeView is one row of the fleet screen.
type nodeView struct {
	Name     string    `json:"name"`
	Port     int       `json:"port"`
	Online   bool      `json:"online"`
	Enrolled int64     `json:"enrolled"`
	LastSeen int64     `json:"lastSeen,omitempty"`
	Info     node.Info `json:"info,omitempty"`

	// Listening is whether the panel currently has this server's port open.
	// A node can be enrolled, and its port refused at startup because something
	// else took it — in which case that server can never connect and nothing
	// else on the screen would say so.
	Listening bool `json:"listening"`

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
	hub := s.nodes.get()
	open := map[int]string{}
	if hub != nil {
		open = hub.Listening()
	}

	rows := []nodeView{}
	for _, n := range node.List() {
		_, listening := open[n.Port]
		rows = append(rows, nodeView{
			Name:      n.Name,
			Port:      n.Port,
			Online:    hub != nil && hub.IsOnline(n.Name),
			Listening: listening,
			Enrolled:  n.Enrolled,
			LastSeen:  n.LastSeen,
			Info:      n.Info,
			Tunnels:   manage.TunnelsOnNode(n.Name),
		})
	}
	pending := []map[string]any{}
	for _, p := range node.PendingList() {
		pending = append(pending, map[string]any{
			"name": p.Name, "port": p.Port, "created": p.Created,
		})
	}
	out := map[string]any{
		"enabled": hub != nil,
		"nodes":   rows,
		"pending": pending,
		// The port the next server would be offered. Suggested on every read
		// rather than held anywhere: the operator may add one now or in a week,
		// and a port that was free then may not be free now.
		"suggestPort": suggestNodePort(),
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
		// The servers stay enrolled. Turning it off is "stop accepting
		// connections", not "forget the fleet" — an operator who wanted the
		// second one would remove the servers.
		s.nodes.stop()
		if err := node.SetEnabled(false); err != nil {
			http.Error(w, "could not save: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.writeNodeState(w)

	case "add":
		hub := s.nodes.get()
		if hub == nil {
			http.Error(w, "turn Accept servers on first — a server has nothing to connect to yet",
				http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		port, err := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
		if err != nil || port < 1 || port > 65535 {
			http.Error(w, "choose a port between 1 and 65535", http.StatusBadRequest)
			return
		}
		token, hubKey, err := node.NewEnrollToken(name, port)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// The door is opened before the command is handed over, so a port that
		// cannot be taken is a message now rather than a server that pastes a
		// line and waits for nothing.
		if err := hub.Open(port, name); err != nil {
			_ = node.Remove(name)
			http.Error(w, "could not listen on port "+strconv.Itoa(port)+": "+err.Error(),
				http.StatusBadRequest)
			return
		}
		addr := nodeDialAddr(port, r)
		key := node.SetupKey(hubKey, token)
		writeJSON(w, map[string]any{
			"name":         name,
			"port":         port,
			"command":      setupCommand(addr, key),
			"commandShort": fmt.Sprintf("backpack node setup --panel %s --key %s", addr, key),
			"panel":        addr,
		})

	case "remove":
		name := strings.TrimSpace(r.FormValue("name"))
		// Read before the removal, which is what forgets it.
		port := 0
		if n, ok := node.Find(name); ok {
			port = n.Port
		} else {
			for _, p := range node.PendingList() {
				if strings.EqualFold(p.Name, name) {
					port = p.Port
				}
			}
		}
		if err := node.Remove(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// The tunnels on it stay exactly as they are, on both machines. What is
		// dropped is only this panel's record that it could still reach one end
		// of them — keeping that would make a later edit report a node that is
		// no longer in the fleet.
		_ = manage.ForgetNodePairs(name)
		if hub := s.nodes.get(); hub != nil && port > 0 {
			hub.Close(port)
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

// nodeDialAddr is the address to put in the setup command.
func nodeDialAddr(port int, r *http.Request) string {
	host := panelHost(r)
	if host == "" {
		host = "<this-server>"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// suggestNodePort picks a free port for the listener.
//
// One port serves the whole fleet — it is a listener, and every managed server
// dials the same one, the way every browser dials the same 443. There is no
// per-server port to allocate.
//
// What matters is which port it is. A fixed, well-known default is the first
// thing an automated scan looks for and the first thing a filter learns to drop,
// and on the network this project exists for that is not a theoretical cost. So
// the suggestion is random and high: five digits, above everything with a
// registered name, and checked to be free before it is offered.
func suggestNodePort() int {
	for i := 0; i < 200; i++ {
		p := 20000 + rand.Intn(40000) // 20000–59999, always five digits
		if !manage.PortInUse(strconv.Itoa(p)) {
			return p
		}
	}
	return 0
}

// setupCommand is the line an operator pastes on a bare server.
//
// It installs Backpack and enrols the machine in one go, because those are one
// intention. Handing over two lines means the second one is run on the wrong
// server, or run before the first has finished, or not run at all — and the
// symptom of the last is a panel with a server that never appears and nothing
// anywhere saying why.
//
// The installer is fetched straight from GitHub rather than proxied through
// this panel: the archive and its checksum have to arrive from the same place
// for verifying one against the other to prove anything, and a panel in the
// middle would be a second thing to trust.
func setupCommand(addr, key string) string {
	return fmt.Sprintf(
		"bash <(curl -fsSL https://raw.githubusercontent.com/%s/%s/main/install.sh) "+
			"node --panel %s --key %s",
		app.RepoOwner, app.RepoName, addr, key)
}

// handleNodeTunnels lists the tunnels on one node.
func (s *server) handleNodeTunnels(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("node"))
	hub := s.nodes.get()
	if hub == nil {
		http.Error(w, "the node listener is off", http.StatusBadRequest)
		return
	}
	var out []node.TunnelState
	if err := hub.Call(name, node.OpList, nil, &out); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"node": name, "tunnels": out})
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
	hub := s.nodes.get()
	if hub == nil {
		http.Error(w, "the node listener is off", http.StatusBadRequest)
		return
	}
	// Checked before anything is written. Creating this end and then finding
	// the other server unreachable leaves half a tunnel and an operator who has
	// to know that is what happened.
	if !hub.IsOnline(req.Node) {
		http.Error(w, "node "+req.Node+" is not connected — nothing was created",
			http.StatusBadGateway)
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

	peer, perr := s.pushPeerEnd(hub, req.Node, name, req.PeerConn, r)
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
func peerConnOnNode(hub *node.Hub, nodeName, tunnel string) *manage.ConnTune {
	if hub == nil {
		return nil
	}
	var cur manage.TunnelSettings
	if err := hub.Call(nodeName, node.OpSettings, node.NameRequest{Name: tunnel}, &cur); err != nil {
		return nil
	}
	conn := cur.Conn
	return &conn
}

// pushPeerEnd mirrors a freshly created tunnel and applies it on the node.
func (s *server) pushPeerEnd(hub *node.Hub, nodeName, tunnel string, peerConn *manage.ConnTune, r *http.Request) (any, error) {
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
			peerConn = peerConnOnNode(hub, nodeName, form.Name)
		}
		t.Conn = peerConn
		req.Tunnel = &t
	}
	var res node.ApplyResult
	if err := hub.Call(nodeName, node.OpApply, req, &res); err != nil {
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

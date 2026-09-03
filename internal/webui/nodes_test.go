package webui

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/node"
)

// The fleet endpoints.
//
// What is worth holding still here is not that they return JSON. It is that a
// panel with the feature turned off cannot be talked into acting on a node, and
// that the one thing this feature is for — an edit reaching both ends — is
// reported honestly when only one of them took it.

// isolateFleet points the fleet's two state files at a temp directory, so a
// test never reads or writes the machine it runs on.
func isolateFleet(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	oldStore, oldPairs := node.StorePath, manage.NodePairPath
	node.StorePath = filepath.Join(dir, "nodes.json")
	manage.NodePairPath = filepath.Join(dir, "node-pairs.json")
	t.Cleanup(func() { node.StorePath, manage.NodePairPath = oldStore, oldPairs })
}

func newFleetServer() *server {
	return &server{sessions: newSessionStore(), nodes: &hubRunner{}}
}

func post(t *testing.T, s *server, form string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/nodes", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleNodes(w, r)
	return w
}

// With the listener off there is no fleet, and nothing can be added to it.
func TestTheFleetIsEmptyAndClosedUntilItIsTurnedOn(t *testing.T) {
	isolateFleet(t)
	s := newFleetServer()

	r := httptest.NewRequest("GET", "/api/nodes", nil)
	w := httptest.NewRecorder()
	s.handleNodes(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var state struct {
		Enabled bool  `json:"enabled"`
		Nodes   []any `json:"nodes"`
	}
	json.Unmarshal(w.Body.Bytes(), &state)
	if state.Enabled || len(state.Nodes) != 0 {
		t.Errorf("a panel that has never used the feature reports enabled=%v with %d servers",
			state.Enabled, len(state.Nodes))
	}

	// Issuing a setup command with nothing listening would hand the operator a
	// line that cannot work.
	if w := post(t, s, "action=add&name=kharej&port=40555"); w.Code == http.StatusOK {
		t.Error("a setup command was issued with no listener to connect to")
	} else if !strings.Contains(w.Body.String(), "Accept servers") {
		t.Errorf("unhelpful refusal: %q", strings.TrimSpace(w.Body.String()))
	}

	// And nothing can be pushed to a node that cannot exist.
	body, _ := json.Marshal(map[string]any{"node": "kharej", "kind": "reverse"})
	rq := httptest.NewRequest("POST", "/api/node/pair", strings.NewReader(string(body)))
	wr := httptest.NewRecorder()
	s.handleNodePair(wr, rq)
	if wr.Code == http.StatusOK {
		t.Error("a tunnel was paired with the listener off")
	}
}

// Turning it on and adding a server issues a usable command on that server's
// own port.
func TestAddingAServerIssuesAUsableCommand(t *testing.T) {
	isolateFleet(t)
	s := newFleetServer()
	t.Cleanup(s.nodes.stop)

	if w := post(t, s, "action=enable"); w.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", w.Code, w.Body.String())
	}
	if !node.LoadStore().Enabled {
		t.Error("the choice was not persisted")
	}

	// A port is suggested, and it is one nothing else on the machine holds.
	var state struct {
		SuggestPort int `json:"suggestPort"`
	}
	r := httptest.NewRequest("GET", "/api/nodes", nil)
	w := httptest.NewRecorder()
	s.handleNodes(w, r)
	json.Unmarshal(w.Body.Bytes(), &state)
	if state.SuggestPort < 10000 || state.SuggestPort > 65535 {
		t.Errorf("suggested port %d is not the five-digit one the panel promises", state.SuggestPort)
	}

	port := freeTestPort(t)
	if w := post(t, s, "action=add&name=kharej&port=notanumber"); w.Code == http.StatusOK {
		t.Error("a non-numeric port was accepted")
	}
	w = post(t, s, fmt.Sprintf("action=add&name=kharej&port=%d", port))
	if w.Code != http.StatusOK {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Command, CommandShort, Panel string
		Port                         int
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Port != port {
		t.Errorf("the server was given port %d, not the %d asked for", got.Port, port)
	}

	// Two lines, for two states the far server can be in. Both have to carry
	// this server's own port and the same key.
	want := ":" + strconv.Itoa(port)
	for _, tc := range []struct{ what, line string }{
		{"one-line", got.Command}, {"short", got.CommandShort},
	} {
		for _, frag := range []string{"--panel", "--key", want} {
			if !strings.Contains(tc.line, frag) {
				t.Errorf("the %s command has no %q: %s", tc.what, frag, tc.line)
			}
		}
	}
	if !strings.Contains(got.Command, "install.sh") {
		t.Errorf("the one-line command does not fetch the installer: %s", got.Command)
	}
	keyOf := func(line string) string { return line[strings.Index(line, "--key ")+len("--key "):] }
	if keyOf(got.Command) != keyOf(got.CommandShort) {
		t.Error("the two lines carry different keys")
	}
	if _, _, err := node.ParseSetupKey(keyOf(got.Command)); err != nil {
		t.Errorf("the key in the command does not parse: %v", err)
	}

	// A second server cannot be put on the same port.
	if w := post(t, s, fmt.Sprintf("action=add&name=other&port=%d", port)); w.Code == http.StatusOK {
		t.Error("two servers were given the same port")
	}

	// Withdrawing it takes the token and closes the door.
	if w := post(t, s, "action=remove&name=kharej"); w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if len(node.PendingList()) != 0 {
		t.Error("removing a pending server left its token live")
	}
	if hub := s.nodes.get(); hub != nil {
		if _, still := hub.Listening()[port]; still {
			t.Error("the port is still open after the server was withdrawn")
		}
	}
}

// freeTestPort takes a port and hands it straight back, so the hub can bind it.
func freeTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// The point of the feature: an edit that cannot reach the other end says so,
// names the server, and does not report plain success.
func TestAnEditThatCannotReachTheNodeSaysSo(t *testing.T) {
	isolateFleet(t)
	s := newFleetServer()

	if err := manage.NoteNodePair("fr-relay", "kharej-de", "fr-relay-kharej"); err != nil {
		t.Fatalf("pair: %v", err)
	}
	r := httptest.NewRequest("POST", "/api/tunnel/edit", nil)
	out := s.afterEdit("fr-relay", r)

	if out["status"] != "partial" {
		t.Errorf("an edit that never reached the node reported %v", out["status"])
	}
	if out["node"] != "kharej-de" {
		t.Errorf("the reply does not name the server: %v", out["node"])
	}
	for _, k := range []string{"peerError", "peerHint"} {
		if v, _ := out[k].(string); !strings.Contains(v, "kharej-de") {
			t.Errorf("%s does not tell the operator which end is behind: %q", k, v)
		}
	}

	// An unpaired tunnel is the ordinary case and gains nothing.
	plain := s.afterEdit("some-other-tunnel", r)
	if plain["status"] != "ok" || plain["node"] != nil {
		t.Errorf("an unpaired edit was reported as %v", plain)
	}
}

// A pairing is remembered until one end of it goes, and removing the server
// forgets every tunnel on it — otherwise a later edit reports a node that is no
// longer in the fleet.
func TestPairingsAreForgottenWithTheirServer(t *testing.T) {
	isolateFleet(t)

	manage.NoteNodePair("fr-relay", "kharej-de", "fr-relay-kharej")
	manage.NoteNodePair("de-edge", "kharej-de", "de-edge-kharej")
	manage.NoteNodePair("nl-ws", "kharej-nl", "nl-ws-kharej")

	if got := manage.TunnelsOnNode("kharej-de"); len(got) != 2 {
		t.Fatalf("tunnels on kharej-de: %v", got)
	}
	if err := manage.ForgetNodePairs("kharej-de"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if got := manage.TunnelsOnNode("kharej-de"); len(got) != 0 {
		t.Errorf("removing the server left %v behind", got)
	}
	if _, ok := manage.NodeFor("nl-ws"); !ok {
		t.Error("removing one server forgot another server's tunnel")
	}

	manage.ForgetNodePair("nl-ws")
	if _, ok := manage.NodeFor("nl-ws"); ok {
		t.Error("a deleted tunnel is still paired")
	}
}

// The picker and the setup-link box both apply to a reverse tunnel and a direct
// one, so neither may sit inside the half of the form that only reverse sees.
//
// This is a guard rather than a style note. Both of them did sit there, and
// nothing failed: the form rendered, the tests passed, and the feature was
// simply absent for every direct tunnel — which is half of what this project
// builds. A rule about where two ids live is cheap; discovering this from a bug
// report is not.
func TestTheNodePickerAndSetupLinkReachBothKindsOfTunnel(t *testing.T) {
	loadExperimentalPanel()

	raw, err := fs.ReadFile(panelRoot, "views/add.html")
	if err != nil {
		t.Fatalf("reading add.html: %v", err)
	}
	html := string(raw)

	rev := strings.Index(html, `<div class="step3rev">`)
	if rev < 0 {
		t.Fatal("add.html has no .step3rev — this guard needs updating")
	}
	for _, id := range []string{`id="nodeGrp"`, `id="apaste"`} {
		at := strings.Index(html, id)
		if at < 0 {
			t.Errorf("%s is not in add.html any more", id)
			continue
		}
		if at > rev {
			t.Errorf("%s sits inside .step3rev, so it is hidden for every direct "+
				"tunnel — move it above the reverse/direct split", id)
		}
	}
}

// The line the panel hands out has to be a line the installer understands.
//
// These are two files in two languages that never call each other, and the only
// place they meet is a server the operator has just pasted into. A flag renamed
// on one side fails there, minutes into an install, with an error nobody here
// would ever see. So they are pinned against each other instead.
func TestTheSetupCommandIsOneTheInstallerAccepts(t *testing.T) {
	sh, err := os.ReadFile(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		t.Fatalf("reading install.sh: %v", err)
	}
	script := string(sh)

	cmd := setupCommand("203.0.113.9:8443", "aaaa.bbbb")

	// Every token the panel emits has to be one the script branches on.
	for _, tok := range []string{"node", "--panel", "--key"} {
		if !strings.Contains(cmd, tok) {
			t.Errorf("the panel's command is missing %q: %s", tok, cmd)
		}
		if !strings.Contains(script, tok) {
			t.Errorf("install.sh does not handle %q, which the panel sends", tok)
		}
	}
	// And the installer it points at has to be this repository's.
	want := "https://raw.githubusercontent.com/" + app.RepoOwner + "/" + app.RepoName + "/main/install.sh"
	if !strings.Contains(cmd, want) {
		t.Errorf("the command does not fetch this repo's installer:\n%s", cmd)
	}
	// The script has to actually run the enrolment at the end, or it would
	// install and then drop the operator into a menu having done nothing.
	if !strings.Contains(script, "node setup --panel") {
		t.Error("install.sh never runs `backpack node setup`, so the one-line form " +
			"would install and stop")
	}
	// And the binary still has the subcommand both lines end in.
	cli, err := os.ReadFile(filepath.Join("..", "..", "nodecmd.go"))
	if err != nil {
		t.Fatalf("reading nodecmd.go: %v", err)
	}
	if !strings.Contains(string(cli), `case "setup":`) {
		t.Error("`backpack node setup` is gone, so both the one-line and the short " +
			"form now end in a command that does not exist")
	}
}

// The address put in the setup command comes from the request when it can.
//
// It has to, for two reasons that pull the same way: the host the operator is
// reaching the panel on is known to work, and asking the internet instead costs
// five timeouts on the machine most likely to have no route out. But an address
// that only means something on this network must not be handed to a server on
// another continent, so those fall through to the lookup.
func TestTheSetupAddressPrefersTheHostTheOperatorUses(t *testing.T) {
	for _, tc := range []struct{ host, want string }{
		{"203.0.113.9:7777", "203.0.113.9"},
		{"203.0.113.9", "203.0.113.9"},
		{"panel.example.com:7777", "panel.example.com"},
		{"[2001:db8::1]:7777", "2001:db8::1"},
	} {
		r := httptest.NewRequest("POST", "/api/nodes", nil)
		r.Host = tc.host
		if got := panelHost(r); got != tc.want {
			t.Errorf("panelHost(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}

	// Everything a foreign server could not dial.
	for _, host := range []string{
		"127.0.0.1", "localhost", "10.0.0.4", "192.168.1.9", "172.16.0.3",
		"169.254.1.1", "0.0.0.0",
	} {
		if !privateHost(host) {
			t.Errorf("%q was treated as an address another server can reach", host)
		}
	}
	for _, host := range []string{"203.0.113.9", "panel.example.com", "2001:db8::1"} {
		if privateHost(host) {
			t.Errorf("%q was treated as unreachable from outside", host)
		}
	}

	// And the port is the listener's, not the panel's.
	r := httptest.NewRequest("POST", "/api/nodes", nil)
	r.Host = "203.0.113.9:7777"
	if got := nodeDialAddr(8443, r); got != "203.0.113.9:8443" {
		t.Errorf("nodeDialAddr = %q, want 203.0.113.9:8443", got)
	}
}

// The card's buttons reach both ends.
//
// A tunnel across a managed server has one state, not two. Stopping only this
// end leaves the other half dialling something that will never answer, and the
// panel used to report that as a clean stop — so the guard here is that the
// reply says which ends actually moved.
func TestStartStopAndRestartReachTheOtherEnd(t *testing.T) {
	isolateFleet(t)
	s := newFleetServer()

	if err := manage.NoteNodePair("fr-relay", "kharej-de", "fr-relay-kharej"); err != nil {
		t.Fatalf("pair: %v", err)
	}
	for _, action := range []string{"start", "stop", "restart"} {
		out := s.alsoOnNode("fr-relay", action)
		if out["status"] != "partial" {
			t.Errorf("%s: an action that never reached the node reported %v", action, out["status"])
		}
		if out["node"] != "kharej-de" {
			t.Errorf("%s: the reply does not name the server: %v", action, out["node"])
		}
		if v, _ := out["peerError"].(string); !strings.Contains(v, "kharej-de") {
			t.Errorf("%s: %q does not say which end is behind", action, v)
		}
	}

	// Deleting is deliberately not one of them: there is no operation that
	// removes a tunnel on a node, and a delete here is not consent to one there.
	if out := s.alsoOnNode("fr-relay", "delete"); out["status"] != "ok" || out["node"] != nil {
		t.Errorf("delete reached across: %v", out)
	}
	// And an unpaired tunnel is the ordinary case, unchanged.
	if out := s.alsoOnNode("some-other", "restart"); out["status"] != "ok" || out["node"] != nil {
		t.Errorf("an unpaired restart was reported as %v", out)
	}
}

// The far end's own name is remembered, because every operation that reaches
// across has to name it and it is not the same name as this end's.
func TestThePeerNameIsRemembered(t *testing.T) {
	isolateFleet(t)
	manage.NoteNodePair("fr-relay", "kharej-de", "fr-relay-kharej")
	p, ok := manage.PairFor("fr-relay")
	if !ok || p.Node != "kharej-de" || p.PeerName != "fr-relay-kharej" {
		t.Fatalf("PairFor = %+v, ok=%v", p, ok)
	}
	if _, ok := manage.PairFor("nothing"); ok {
		t.Error("an unpaired tunnel reported a pair")
	}
}

// An edit rebuilds the far end, so it first asks the far end what it already
// has. A node that cannot answer must not turn that into a failed edit: the
// carry-forward is an improvement on the rebuild, not a precondition for it.
func TestTheCarryForwardNeverBlocksAnEdit(t *testing.T) {
	isolateFleet(t)
	s := newFleetServer()

	if got := peerConnOnNode(s.nodes.get(), "kharej-de", "fr-relay-kharej"); got != nil {
		t.Errorf("an unreachable node produced settings out of nowhere: %+v", got)
	}
}

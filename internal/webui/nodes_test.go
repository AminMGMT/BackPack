package webui

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	return &server{sessions: newSessionStore(), nodes: &fleet{}}
}

// fakeRunner stands in for a fleet of real machines.
//
// The transport is SSH now, so a test that wanted a live node would need a
// second computer. What the panel's own behaviour depends on is narrower than
// that: whether a server answers, and what it says — so that is what is
// substituted, and every path through the handlers is exercised for real.
type fakeRunner struct {
	up      map[string]bool
	answers map[string]any   // op -> what it returns
	fail    map[string]error // op -> what it refuses with
	calls   []string
	forgot  []string
}

func newFake() *fakeRunner {
	return &fakeRunner{up: map[string]bool{}, answers: map[string]any{}, fail: map[string]error{}}
}

func (f *fakeRunner) Call(name, op string, body, out any) error {
	f.calls = append(f.calls, name+":"+op)
	if !f.up[name] {
		return node.ErrOffline{Name: name, Why: "no route to host"}
	}
	if err := f.fail[op]; err != nil {
		return err
	}
	if v, ok := f.answers[op]; ok && out != nil {
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	}
	return nil
}

func (f *fakeRunner) IsOnline(name string) bool { return f.up[name] }

func (f *fakeRunner) Reachable(name string) (bool, string) {
	if f.up[name] {
		return true, ""
	}
	return false, "no route to host"
}

func (f *fakeRunner) Forget(name string) { f.forgot = append(f.forgot, name) }

// withFleet puts a stand-in behind the panel's fleet.
func withFleet(s *server, f *fakeRunner) { s.nodes.run = f }

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

	// Nothing can be added before the feature is on.
	if w := post(t, s, "action=add&name=kharej&host=203.0.113.9&user=root&password=x"); w.Code == http.StatusOK {
		t.Error("a server was added with the feature turned off")
	} else if !strings.Contains(w.Body.String(), "Managed servers") {
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
// Adding a server is one round trip to it, and the fleet only keeps what
// answered.
//
// The flow this replaces saved the details first and found out whether they
// worked later — which is how a server came to sit in the fleet doing nothing
// with nothing saying why. A login that does not work is a typo, and a typo is
// worth reporting while the operator is still looking at the form.
func TestAddingAServerKeepsOnlyWhatAnswers(t *testing.T) {
	isolateFleet(t)
	s := newFleetServer()
	t.Cleanup(s.nodes.stop)

	if w := post(t, s, "action=add&name=kharej&host=203.0.113.9&user=root&password=x"); w.Code == http.StatusOK {
		t.Error("a server was added before the feature was turned on")
	}
	if w := post(t, s, "action=enable"); w.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", w.Code, w.Body.String())
	}
	if !node.LoadStore().Enabled {
		t.Error("the choice was not persisted")
	}

	f := newFake()
	withFleet(s, f)

	// The form is checked before anything is dialled.
	for _, bad := range []struct{ what, form string }{
		{"no address", "action=add&name=kharej&host=&user=root&password=x"},
		{"no password", "action=add&name=kharej&host=203.0.113.9&user=root&password="},
		{"a bad name", "action=add&name=kha%2Frej&host=203.0.113.9&user=root&password=x"},
		{"a bad ssh port", "action=add&name=kharej&host=203.0.113.9&user=root&password=x&sshPort=0"},
	} {
		if w := post(t, s, bad.form); w.Code == http.StatusOK {
			t.Errorf("a server with %s was accepted", bad.what)
		}
	}

	// A server that does not answer is not kept.
	if w := post(t, s, "action=add&name=kharej&host=203.0.113.9&user=root&password=x"); w.Code == http.StatusOK {
		t.Error("a server that could not be reached was added anyway")
	}
	if len(node.List()) != 0 {
		t.Errorf("an unreachable server was left in the fleet: %+v", node.List())
	}

	// One that does is.
	f.up["kharej"] = true
	f.answers[node.OpHello] = node.Info{Version: "v1.7.6", OS: "Ubuntu 24.04"}
	if w := post(t, s, "action=add&name=kharej&host=203.0.113.9&user=root&password=x"); w.Code != http.StatusOK {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	got := node.List()
	if len(got) != 1 {
		t.Fatalf("the fleet holds %d servers", len(got))
	}
	if got[0].Host != "203.0.113.9" || got[0].User != "root" {
		t.Errorf("the server was stored as %+v", got[0])
	}
	if got[0].Password != "" {
		t.Error("List handed out the password")
	}
	if got[0].Info.Version != "v1.7.6" {
		t.Errorf("what the server said about itself was not kept: %+v", got[0].Info)
	}

	// The same machine twice is a mistake worth catching: two names for one
	// server means two cards that disagree about it.
	if w := post(t, s, "action=add&name=other&host=203.0.113.9&user=root&password=x"); w.Code == http.StatusOK {
		t.Error("the same address was added twice")
	}

	// Removing it drops the connection with the record.
	if w := post(t, s, "action=remove&name=kharej"); w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if len(node.List()) != 0 {
		t.Error("the server is still in the fleet")
	}
	if len(f.forgot) == 0 || f.forgot[0] != "kharej" {
		t.Error("the connection to a removed server was left open")
	}
}

// The password is never sent to the browser, and changing the address forgets
// the host key — a different machine is entitled to a different one.
func TestCredentialsCanBeChangedAndAreNeverSentBack(t *testing.T) {
	isolateFleet(t)
	s := newFleetServer()
	t.Cleanup(s.nodes.stop)
	post(t, s, "action=enable")
	f := newFake()
	f.up["kharej"] = true
	withFleet(s, f)
	post(t, s, "action=add&name=kharej&host=203.0.113.9&user=root&password=first")

	if err := node.NoteFingerprint("kharej", "SHA256:abc"); err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if w := post(t, s, "action=credentials&name=kharej&host=198.51.100.7&password=second"); w.Code != http.StatusOK {
		t.Fatalf("credentials: %d %s", w.Code, w.Body.String())
	}
	n, _ := node.Find("kharej")
	if n.Host != "198.51.100.7" {
		t.Errorf("the address was not changed: %s", n.Host)
	}
	if n.Fingerprint != "" {
		t.Error("the host key from the old address was kept for the new one")
	}

	r := httptest.NewRequest("GET", "/api/nodes", nil)
	w := httptest.NewRecorder()
	s.handleNodes(w, r)
	if strings.Contains(w.Body.String(), "first") || strings.Contains(w.Body.String(), "second") {
		t.Error("the fleet listing carries the server's password")
	}
	if !strings.Contains(w.Body.String(), "198.51.100.7") {
		t.Error("the fleet listing does not say where the server is")
	}
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
// A server is added and managed without anything being run on it.
//
// This is the change the whole batch is for. What used to be here checked that
// the panel handed out a setup command, that install.sh understood it, and that
// the binary had the subcommand it ended in — three things that all had to
// agree, and one line for the operator to carry to another machine.
//
// There is no line now. What has to hold instead is that the far side needs no
// state of its own: one command, which the panel runs itself.
func TestTheFarSideNeedsNothingButTheOneCommand(t *testing.T) {
	cli, err := os.ReadFile(filepath.Join("..", "..", "nodecmd.go"))
	if err != nil {
		t.Fatalf("reading nodecmd.go: %v", err)
	}
	src := string(cli)
	if !strings.Contains(src, `case "exec":`) {
		t.Fatal("`backpack node exec` is gone, and it is the only thing the panel runs " +
			"on a managed server")
	}
	for _, gone := range []string{`case "setup":`, `case "run":`, `case "remove":`} {
		if strings.Contains(src, gone) {
			t.Errorf("nodecmd.go still has %s — the far server keeps no state now, so "+
				"anything that sets it up or tears it down is a second model of the "+
				"same thing", gone)
		}
	}

	sh, err := os.ReadFile(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		t.Fatalf("reading install.sh: %v", err)
	}
	if strings.Contains(string(sh), "node setup") {
		t.Error("install.sh still ends in `backpack node setup`, which no longer exists")
	}
	// And it must still install without a terminal, because that is how the
	// panel runs it on a server that has no Backpack yet.
	if !strings.Contains(string(sh), "if [ -t 0 ]") {
		t.Error("install.sh no longer checks for a terminal, so a remote install would " +
			"open a menu nobody can answer")
	}
}

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

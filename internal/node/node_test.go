package node

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/manage"
)

// isolate points the two state files at a temp directory, so a test never
// reads or writes the fleet of the machine it runs on.
func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	oldStore, oldAgent := StorePath, AgentPath
	StorePath = filepath.Join(dir, "nodes.json")
	AgentPath = filepath.Join(dir, "node-agent.json")
	t.Cleanup(func() { StorePath, AgentPath = oldStore, oldAgent })
}

// startHub brings a hub up with no doors open yet.
func startHub(t *testing.T, ctx context.Context) *Hub {
	t.Helper()
	key, err := EnsureHubKey()
	if err != nil {
		t.Fatalf("hub key: %v", err)
	}
	h := NewHub(func(string) {})
	if err := h.Start(ctx, key); err != nil {
		t.Fatalf("start: %v", err)
	}
	return h
}

// freePort takes a port from the kernel and gives it straight back, so the hub
// can bind it a moment later. It is the only way to get one that is free
// without guessing, and the window between the two is not a race any test here
// can lose: nothing else on the machine is opening ports during a test run.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// openFor issues an enrolment for a name on a free port, opens that port, and
// returns the address a node would dial and the setup key it would carry.
func openFor(t *testing.T, h *Hub, name string) (addr, key string) {
	t.Helper()
	port := freePort(t)
	token, hub, err := NewEnrollToken(name, port)
	if err != nil {
		t.Fatalf("token for %s: %v", name, err)
	}
	if err := h.Open(port, name); err != nil {
		t.Fatalf("open %d: %v", port, err)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), SetupKey(hub, token)
}

func waitOnline(t *testing.T, h *Hub, name string, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if h.IsOnline(name) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("node %q online=%v, wanted %v", name, h.IsOnline(name), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A node that has never connected enrols with the token, is issued a key, and
// is reachable from the panel afterwards.
func TestANodeEnrollsAndAnswers(t *testing.T) {
	isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := startHub(t, ctx)
	addr, key := openFor(t, h, "kharej")

	hk, ek, err := ParseSetupKey(key)
	if err != nil {
		t.Fatalf("setup key does not round-trip: %v", err)
	}

	a := NewAgent(AgentConfig{Server: addr, HubKey: hk, Enroll: ek}, func(string) {})
	go a.Run(ctx)
	waitOnline(t, h, "kharej", true)

	var info Info
	if err := h.Call("kharej", OpHello, nil, &info); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if info.Version == "" {
		t.Error("the node reported no version")
	}

	// The token is spent and the credential is on the node's disk.
	if len(PendingList()) != 0 {
		t.Error("the enrolment token was not burned")
	}
	saved, err := LoadAgent()
	if err != nil {
		t.Fatalf("the node saved no config: %v", err)
	}
	if saved.NodeKey == "" {
		t.Error("the node was not issued a key")
	}
	if saved.Enroll != "" {
		t.Error("the spent enrolment token is still on disk")
	}
	if got := List(); len(got) != 1 || got[0].Name != "kharej" {
		t.Fatalf("registry holds %+v", got)
	}
	if got := List(); got[0].Key != "" {
		t.Error("List handed out the node's credential")
	}
}

// The same token cannot enrol a second server, and an operation that is not on
// the list is refused however it is asked for.
func TestATokenIsSingleUseAndOpsAreAWhitelist(t *testing.T) {
	isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := startHub(t, ctx)
	addr, key := openFor(t, h, "kharej")
	hk, token, _ := ParseSetupKey(key)

	a := NewAgent(AgentConfig{Server: addr, HubKey: hk, Enroll: token}, func(string) {})
	go a.Run(ctx)
	waitOnline(t, h, "kharej", true)

	// Redeeming it again fails, which is what a second server pasting the same
	// command would find.
	if _, err := Redeem(token, Info{}); err == nil {
		t.Fatal("the enrolment token was accepted twice")
	}

	for _, op := range []string{"exec", "read", "shell", "update", "remove", ""} {
		if err := h.Call("kharej", op, nil, nil); err == nil {
			t.Errorf("the node accepted %q", op)
		}
	}
}

// An enrolled node reconnects with its key rather than the spent token, and
// removing it from the panel locks it out.
func TestRemovingANodeRevokesIt(t *testing.T) {
	isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := startHub(t, ctx)
	addr, key := openFor(t, h, "kharej")
	hk, token, _ := ParseSetupKey(key)

	first, firstStop := context.WithCancel(ctx)
	a := NewAgent(AgentConfig{Server: addr, HubKey: hk, Enroll: token}, func(string) {})
	go a.Run(first)
	waitOnline(t, h, "kharej", true)

	firstStop()
	waitOnline(t, h, "kharej", false)

	// A restart reads the saved credential. Enrolment is not attempted again —
	// it could not succeed, because the token is gone from both ends.
	saved, err := LoadAgent()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if saved.Enroll != "" {
		t.Fatal("the node would try to enrol again")
	}
	second, secondStop := context.WithCancel(ctx)
	go NewAgent(saved, func(string) {}).Run(second)
	waitOnline(t, h, "kharej", true)
	secondStop()
	waitOnline(t, h, "kharej", false)

	// Revoked at the panel, the same credential no longer authenticates.
	if err := Remove("kharej"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	third, thirdStop := context.WithCancel(ctx)
	defer thirdStop()
	go NewAgent(saved, func(string) {}).Run(third)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.IsOnline("kharej") {
			t.Fatal("a removed node was let back in")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The hub key is what the Noise handshake runs on, so a wrong one never reaches
// authentication at all.
func TestAWrongHubKeyNeverConnects(t *testing.T) {
	isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := startHub(t, ctx)
	addr, key := openFor(t, h, "kharej")
	_, token, _ := ParseSetupKey(key)

	go NewAgent(AgentConfig{
		Server: addr, HubKey: "00000000000000000000000000000000", Enroll: token,
	}, func(string) {}).Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.Online()) != 0 {
			t.Fatal("a peer with the wrong hub key connected")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(PendingList()) != 1 {
		t.Error("the enrolment token should still be waiting")
	}
}

// Calling a node that is not connected fails immediately and says so in words
// an operator can act on.
func TestCallingAnOfflineNodeFails(t *testing.T) {
	isolate(t)
	h := NewHub(nil)
	err := h.Call("kharej", OpHello, nil, nil)
	if err == nil {
		t.Fatal("calling an offline node succeeded")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// A name that could be read as a path or a shell fragment is refused when the
// token is issued, not when it reaches a systemd unit on the far machine.
func TestNodeNamesAreRestricted(t *testing.T) {
	isolate(t)
	for _, bad := range []string{"", "  ", "../etc", "a b", "a;b", "a/b", strings.Repeat("x", 41)} {
		if _, _, err := NewEnrollToken(bad, 40000); err == nil {
			t.Errorf("accepted %q as a node name", bad)
		}
	}
	for i, good := range []string{"kharej", "de-1", "node_2"} {
		if _, _, err := NewEnrollToken(good, 40001+i); err != nil {
			t.Errorf("refused %q: %v", good, err)
		}
	}
}

// An expired token is refused and removed, so the pending list does not fill
// with commands that can no longer be used.
func TestAnExpiredTokenIsRefused(t *testing.T) {
	isolate(t)
	token, _, err := NewEnrollToken("kharej", 40100)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	s := LoadStore()
	s.Pending[0].Created = time.Now().Add(-2 * enrollTTL).Unix()
	if err := SaveStore(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := Redeem(token, Info{}); err == nil {
		t.Fatal("an expired token was redeemed")
	}
	if len(PendingList()) != 0 {
		t.Error("the expired token is still listed")
	}
}

// A port belongs to one server, and a credential is only accepted at its own.
//
// This is what a port each buys over one port for everyone. The key alone is no
// longer enough: it has to arrive at the door it was issued for, so a key that
// leaks is worth nothing to anyone who does not also know which of the
// operator's ports it belongs to.
func TestACredentialIsOnlyAcceptedAtItsOwnPort(t *testing.T) {
	isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := startHub(t, ctx)
	deAddr, deKey := openFor(t, h, "kharej-de")
	nlAddr, _ := openFor(t, h, "kharej-nl")

	// kharej-de enrols normally, at its own port.
	hk, ek, _ := ParseSetupKey(deKey)
	deCtx, deStop := context.WithCancel(ctx)
	go NewAgent(AgentConfig{Server: deAddr, HubKey: hk, Enroll: ek}, func(string) {}).Run(deCtx)
	waitOnline(t, h, "kharej-de", true)
	saved, err := LoadAgent()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	deStop()
	waitOnline(t, h, "kharej-de", false)

	// The same credential, offered at the other server's port.
	wrong := saved
	wrong.Server = nlAddr
	wrongCtx, wrongStop := context.WithCancel(ctx)
	defer wrongStop()
	go NewAgent(wrong, func(string) {}).Run(wrongCtx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.IsOnline("kharej-de") {
			t.Fatal("a node's credential was accepted at another node's port")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// And it still works at its own.
	rightCtx, rightStop := context.WithCancel(ctx)
	defer rightStop()
	go NewAgent(saved, func(string) {}).Run(rightCtx)
	waitOnline(t, h, "kharej-de", true)
}

// Two servers cannot be given the same port: only one listener can exist there,
// so the second would be told to dial a door that is not its own and nothing
// would ever say why.
func TestTwoServersCannotShareAPort(t *testing.T) {
	isolate(t)
	if _, _, err := NewEnrollToken("kharej-de", 41000); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, _, err := NewEnrollToken("kharej-nl", 41000); err == nil {
		t.Fatal("two servers were given the same port")
	}
	if on, taken := PortTaken(41000); !taken || on != "kharej-de" {
		t.Errorf("PortTaken(41000) = %q, %v", on, taken)
	}
	if _, taken := PortTaken(41001); taken {
		t.Error("an unused port reported as taken")
	}
}

// The speed test's sink: it opens where it is asked, sinks what arrives, and
// closes itself.
//
// The closing is the part worth pinning. This exists so that nobody has to
// remember to start a receiver on the other server — which means nobody is
// going to remember to stop one either, and a listener left open on a forwarded
// port is worse than the manual step it replaced.
func TestTheReceiverOpensAndClosesItself(t *testing.T) {
	isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := startHub(t, ctx)
	addr, key := openFor(t, h, "kharej")
	hk, ek, _ := ParseSetupKey(key)
	go NewAgent(AgentConfig{Server: addr, HubKey: hk, Enroll: ek}, func(string) {}).Run(ctx)
	waitOnline(t, h, "kharej", true)

	port := freePort(t)
	var got struct{ Port, Seconds int }
	if err := h.Call("kharej", OpReceive, ReceiveRequest{Port: port, Seconds: 1}, &got); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if got.Port != port {
		t.Errorf("the sink opened on %d, not the %d asked for", got.Port, port)
	}

	// It is there, and it takes what is sent without answering.
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		t.Fatalf("the sink is not listening: %v", err)
	}
	if _, err := c.Write(make([]byte, 4096)); err != nil {
		t.Errorf("the sink refused bytes: %v", err)
	}
	c.Close()

	// And it is gone once its window is up.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond)
		if err != nil {
			return // closed, as it should be
		}
		c.Close()
		time.Sleep(200 * time.Millisecond)
	}
	t.Error("the sink was still listening after its window — it never closes itself")
}

// A port that is not a port is refused rather than handed to net.Listen.
func TestTheReceiverRefusesANonPort(t *testing.T) {
	isolate(t)
	for _, p := range []int{0, -1, 70000} {
		if r := Execute(Request{Op: OpReceive,
			Body: mustJSON(t, ReceiveRequest{Port: p, Seconds: 1})}); r.OK {
			t.Errorf("the node opened a sink on %d", p)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// The panel can read one tunnel's settings back off a node.
//
// This is what keeps an edit from silently dropping the far end's own answers:
// the mirror an edit is built from carries only what both ends must agree on,
// so the panel asks the node for the rest. The operation has to be reachable
// over a live session, and it has to name what it could not find rather than
// answer with an empty form that would look like "this tunnel has no settings".
func TestTheFarEndCanBeAskedForItsOwnSettings(t *testing.T) {
	isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := startHub(t, ctx)
	addr, key := openFor(t, h, "kharej")
	hk, token, _ := ParseSetupKey(key)

	a := NewAgent(AgentConfig{Server: addr, HubKey: hk, Enroll: token}, func(string) {})
	go a.Run(ctx)
	waitOnline(t, h, "kharej", true)

	var got manage.TunnelSettings
	err := h.Call("kharej", OpSettings, NameRequest{Name: "not-there-kharej"}, &got)
	if err == nil {
		t.Fatal("a tunnel the node does not have was reported as readable")
	}
	if !strings.Contains(err.Error(), "not-there-kharej") {
		t.Errorf("the failure does not say which tunnel was missing: %v", err)
	}
}

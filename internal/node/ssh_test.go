package node

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/manage"
	"golang.org/x/crypto/ssh"
)

// The transport, against a real SSH implementation.
//
// A managed server is another machine, so the honest test needs one — and the
// same library that dials also serves, which makes one. What runs here is the
// panel's own client against a server that speaks the protocol properly: the
// key exchange, the password, the host key, the exec channel and the exit
// status are all real, and only the machine is not.

// fakeServer is an SSH server that answers exec requests.
type fakeServer struct {
	ln     net.Listener
	signer ssh.Signer
	user   string
	pass   string

	mu      sync.Mutex
	ran     []string
	prefix  string // printed before the answer, like a login banner
	failing bool   // the binary is not there
	old     bool   // the binary is there and predates "node exec"
	refuse  string // the far side answers, and says no
}

// oldNodeUsage is verbatim what Backpack v1.7.6 and earlier print when asked to
// run a subcommand they do not have. It is reproduced here rather than
// summarised because the point of the test is that this exact output — a whole
// screen of another machine's help, on stderr, with a non-zero exit — is what a
// panel meets when it reaches a server that has not been upgraded yet.
const oldNodeUsage = `unknown command "exec"

backpack node — connect this server to a Backpack panel

  backpack node setup --panel <host:port> --key <setup-key>
        Register this server with a panel and start the agent.
        The panel shows this line ready to paste, on Nodes → Add server.

  backpack node status
        Show whether this server is managed, and by which panel.

  backpack node run
        Run the agent in the foreground. This is what the service executes;
        there is no reason to run it by hand.

  backpack node remove
        Stop being managed. Tunnels already on this server keep running.
`

func newFakeServer(t *testing.T, user, pass string) *fakeServer {
	t.Helper()
	// A throwaway host key. Generating one is the point: the client has to
	// accept it the first time and insist on it afterwards.
	key, err := ssh.ParsePrivateKey(testHostKey)
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeServer{ln: ln, signer: key, user: user, pass: pass}
	go s.serve()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *fakeServer) addr() (string, int) {
	h, p, _ := net.SplitHostPort(s.ln.Addr().String())
	var n int
	fmt.Sscanf(p, "%d", &n)
	return h, n
}

func (s *fakeServer) serve() {
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == s.user && string(pw) == s.pass {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	cfg.AddHostKey(s.signer)

	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c, cfg)
	}
}

func (s *fakeServer) handle(c net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		c.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)

	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range creqs {
				if req.Type != "exec" {
					req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				ssh.Unmarshal(req.Payload, &payload)
				req.Reply(true, nil)

				s.mu.Lock()
				s.ran = append(s.ran, payload.Command)
				failing, isOld, prefix, refuse := s.failing, s.old, s.prefix, s.refuse
				s.mu.Unlock()

				if failing {
					fmt.Fprintln(ch.Stderr(), "sh: backpack: command not found")
					ch.SendRequest("exit-status", false, ssh.Marshal(struct{ S uint32 }{127}))
					return
				}
				if isOld {
					fmt.Fprint(ch.Stderr(), oldNodeUsage)
					ch.SendRequest("exit-status", false, ssh.Marshal(struct{ S uint32 }{2}))
					return
				}
				if prefix != "" {
					fmt.Fprintln(ch, prefix)
				}
				if refuse != "" {
					out, _ := json.Marshal(Response{Err: refuse})
					fmt.Fprintln(ch, base64.StdEncoding.EncodeToString(out))
					ch.SendRequest("exit-status", false, ssh.Marshal(struct{ S uint32 }{0}))
					return
				}
				fmt.Fprintln(ch, answerTo(payload.Command))
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ S uint32 }{0}))
				return
			}
		}()
	}
}

// answerTo does what `backpack node exec` does on the far machine, except that
// it echoes the request back instead of performing it — so a test can see
// exactly what arrived.
func answerTo(cmd string) string {
	i := strings.LastIndex(cmd, " ")
	arg := strings.Trim(cmd[i+1:], "'")
	raw, err := base64.StdEncoding.DecodeString(arg)
	if err != nil {
		return "not base64"
	}
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return "not json"
	}
	out, _ := json.Marshal(Response{OK: true, Body: mustJSON(map[string]any{
		"op": req.Op, "body": req.Body,
	})})
	return base64.StdEncoding.EncodeToString(out)
}

// isolateStore points the fleet file at a temp directory.
func isolateStore(t *testing.T) {
	t.Helper()
	old := StorePath
	StorePath = filepath.Join(t.TempDir(), "nodes.json")
	t.Cleanup(func() { StorePath = old })
}

func TestTheRunnerReachesAServerOverSSH(t *testing.T) {
	isolateStore(t)
	srv := newFakeServer(t, "root", "hunter2")
	host, port := srv.addr()

	if _, err := Add("kharej", host, port, "root", "hunter2"); err != nil {
		t.Fatalf("add: %v", err)
	}
	r := NewSSHRunner(nil)
	defer r.Close()

	var got struct{ Op string }
	if err := r.Call("kharej", OpHello, nil, &got); err != nil {
		t.Fatalf("call: %v", err)
	}
	if got.Op != OpHello {
		t.Errorf("the far side was asked for %q", got.Op)
	}
	if !r.IsOnline("kharej") {
		t.Error("a server that just answered is reported offline")
	}

	// The command it ran is the one command a managed server has.
	srv.mu.Lock()
	ran := append([]string(nil), srv.ran...)
	srv.mu.Unlock()
	if len(ran) == 0 || !strings.Contains(ran[0], "node exec") {
		t.Errorf("the panel ran %q on the server", ran)
	}
}

// The host key is recorded the first time and required afterwards. Without
// this, anything that can answer on that address gets a root shell and the
// panel would never say a word about it.
func TestTheHostKeyIsRememberedAndThenRequired(t *testing.T) {
	isolateStore(t)
	srv := newFakeServer(t, "root", "hunter2")
	host, port := srv.addr()
	Add("kharej", host, port, "root", "hunter2")

	r := NewSSHRunner(nil)
	if err := r.Call("kharej", OpHello, nil, nil); err != nil {
		t.Fatalf("first call: %v", err)
	}
	r.Close()

	n, _ := Find("kharej")
	if n.Fingerprint == "" {
		t.Fatal("the host key was not recorded on first sight")
	}

	// A different machine on the same address is refused, and says why.
	if err := update(func(s *Store) error {
		s.Nodes[0].Fingerprint = "SHA256:somethingelse"
		return nil
	}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	r2 := NewSSHRunner(nil)
	defer r2.Close()
	err := r2.Call("kharej", OpHello, nil, nil)
	if err == nil {
		t.Fatal("a server presenting a different host key was accepted")
	}
	if !strings.Contains(err.Error(), "host key") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

// A wrong password is the likeliest thing to be wrong, and the library's own
// wording for it is not something an operator can act on.
func TestAWrongPasswordSaysSo(t *testing.T) {
	isolateStore(t)
	srv := newFakeServer(t, "root", "hunter2")
	host, port := srv.addr()
	Add("kharej", host, port, "root", "wrong")

	r := NewSSHRunner(nil)
	defer r.Close()
	err := r.Call("kharej", OpHello, nil, nil)
	if err == nil {
		t.Fatal("a wrong password was accepted")
	}
	if !strings.Contains(err.Error(), "username and password") {
		t.Errorf("the refusal is not one an operator can act on: %v", err)
	}
	if r.IsOnline("kharej") {
		t.Error("a server that refused the login is reported online")
	}
}

// A login banner is normal on a server somebody administers, and it arrives on
// stdout ahead of the answer.
func TestABannerDoesNotBreakTheAnswer(t *testing.T) {
	isolateStore(t)
	srv := newFakeServer(t, "root", "hunter2")
	srv.prefix = "Welcome to Ubuntu 24.04 LTS\nLast login: Tue Sep  2 09:14:01 2026"
	host, port := srv.addr()
	Add("kharej", host, port, "root", "hunter2")

	r := NewSSHRunner(nil)
	defer r.Close()
	var got struct{ Op string }
	if err := r.Call("kharej", OpStatus, nil, &got); err != nil {
		t.Fatalf("a banner broke the call: %v", err)
	}
	if got.Op != OpStatus {
		t.Errorf("the answer was read as %q", got.Op)
	}
}

// A server with no Backpack on it is a thing to install, not a mystery.
func TestAMissingBinaryIsNamed(t *testing.T) {
	isolateStore(t)
	srv := newFakeServer(t, "root", "hunter2")
	srv.failing = true
	host, port := srv.addr()
	Add("kharej", host, port, "root", "hunter2")

	r := NewSSHRunner(nil)
	defer r.Close()
	err := r.Call("kharej", OpHello, nil, nil)
	if err == nil {
		t.Fatal("a server with no Backpack answered successfully")
	}
	if !errors.Is(err, ErrNeedsInstall) {
		t.Errorf("the operator is not told what to do about it: %v", err)
	}
}

// Reaching a server that is not in the fleet is a mistake worth naming.
func TestAnUnknownServerIsRefused(t *testing.T) {
	isolateStore(t)
	r := NewSSHRunner(nil)
	defer r.Close()
	if err := r.Call("nobody", OpHello, nil, nil); err == nil {
		t.Fatal("a call to a server that does not exist succeeded")
	}
}

// The connection is reused: a panel that dialled per call would get slower the
// more servers it manages, which is the wrong way round.
func TestConnectionsAreReused(t *testing.T) {
	isolateStore(t)
	srv := newFakeServer(t, "root", "hunter2")
	host, port := srv.addr()
	Add("kharej", host, port, "root", "hunter2")

	r := NewSSHRunner(nil)
	defer r.Close()
	for i := 0; i < 5; i++ {
		if err := r.Call("kharej", OpHello, nil, nil); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	r.pool.mu.Lock()
	held := len(r.pool.conns)
	r.pool.mu.Unlock()
	if held != 1 {
		t.Errorf("five calls left %d connections open", held)
	}

	// And dropping it is what a credential change does.
	r.Forget("kharej")
	r.pool.mu.Lock()
	held = len(r.pool.conns)
	r.pool.mu.Unlock()
	if held != 0 {
		t.Error("forgetting a server left its connection open")
	}
}

// The answer must not be believed for longer than it is worth.
func TestReachabilityIsNotCachedForever(t *testing.T) {
	isolateStore(t)
	if reachTTL > time.Minute {
		t.Errorf("a server's state is trusted for %s; a server going down would go "+
			"unnoticed for that long", reachTTL)
	}
	if reachTTL < 5*time.Second {
		t.Errorf("a server's state is trusted for only %s, so every poll opens a "+
			"connection to every server in the fleet", reachTTL)
	}
}

// The whole tunnel form has to reach the far server unchanged.
//
// This is what the feature is: a tunnel built here and mirrored there, from one
// submission. If anything is dropped on the way — a preset, a port list, the
// tuning the operator set — the two ends disagree, and the symptom is a tunnel
// that comes up and carries nothing. So what arrives is compared against what
// was sent, field by field, rather than checked for looking about right.
func TestTheWholeFormReachesTheFarServer(t *testing.T) {
	isolateStore(t)
	srv := newFakeServer(t, "root", "hunter2")
	host, port := srv.addr()
	Add("kharej", host, port, "root", "hunter2")

	sent := ApplyRequest{
		Kind: "reverse",
		Tunnel: &manage.NewTunnel{
			Role: "client", Transport: "tcpmux", Name: "pairtest",
			TunnelPort: "4600", ServerAddr: "198.51.100.7", Token: "s3cret",
			Ports: "9100=9100,5353=53", Preset: "balanced",
			IPv6: true, ProxyProtocol: true,
		},
	}

	r := NewSSHRunner(nil)
	defer r.Close()
	var got struct {
		Op   string          `json:"op"`
		Body json.RawMessage `json:"body"`
	}
	if err := r.Call("kharej", OpApply, sent, &got); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got.Op != OpApply {
		t.Errorf("the far server was asked for %q", got.Op)
	}
	var arrived ApplyRequest
	if err := json.Unmarshal(got.Body, &arrived); err != nil {
		t.Fatalf("the far server could not read the form: %v", err)
	}
	if arrived.Tunnel == nil {
		t.Fatal("the tunnel form did not arrive at all")
	}
	if *arrived.Tunnel != *sent.Tunnel {
		t.Errorf("the form changed on the way:\n sent %+v\n got  %+v", *sent.Tunnel, *arrived.Tunnel)
	}
}

// A refusal from the far side is the far side's, and must arrive as itself
// rather than as a transport failure — "no tunnel named X on that server" is
// something to act on, "the server is offline" when it is not is a wild goose
// chase.
func TestARefusalFromTheFarSideArrivesAsItself(t *testing.T) {
	isolateStore(t)
	srv := newFakeServer(t, "root", "hunter2")
	srv.refuse = "no tunnel named \"ghost\" on this server"
	host, port := srv.addr()
	Add("kharej", host, port, "root", "hunter2")

	r := NewSSHRunner(nil)
	defer r.Close()
	err := r.Call("kharej", OpStatus, NameRequest{Name: "ghost"}, nil)
	if err == nil {
		t.Fatal("a refusal was reported as success")
	}
	if !strings.Contains(err.Error(), "no tunnel named") {
		t.Errorf("the far side's reason was lost: %v", err)
	}
	var off ErrOffline
	if errors.As(err, &off) {
		t.Error("a server that answered was reported as offline")
	}
	if !r.IsOnline("kharej") {
		t.Error("a server that refused an operation is treated as unreachable")
	}
}

func TestMain(m *testing.M) { os.Exit(m.Run()) }

// A server running an older Backpack has to be recognised as one.
//
// This is the bug report "adding a server says I have to make it a node like
// before". The panel reaches a new server by running `backpack node exec` on
// it over SSH. A server that has never had Backpack answers "command not
// found", which the panel understands and fixes by installing. A server that
// has Backpack v1.7.6 on it answers something else entirely: the binary is
// there, it runs, and it rejects the subcommand — printing a screen of its own
// help to stderr and exiting 2.
//
// That is not a failure, it is the ordinary state of every server in a fleet
// the day the panel is upgraded. It was treated as a hard error: the add was
// refused, the server was taken back out, and the operator was shown the far
// machine's help text for a command that no longer exists, telling them to go
// and run `backpack node setup` — the very flow this release removed.
func TestAnOlderBackpackIsRecognisedAsOneToUpgrade(t *testing.T) {
	isolateStore(t)
	srv := newFakeServer(t, "root", "hunter2")
	srv.old = true
	host, port := srv.addr()
	Add("kharej", host, port, "root", "hunter2")

	r := NewSSHRunner(nil)
	defer r.Close()
	err := r.Call("kharej", OpHello, nil, nil)
	if err == nil {
		t.Fatal("a server running an older Backpack answered successfully")
	}

	// The same condition the missing case raises, because the panel does the
	// same thing about both: it installs, which is also how it upgrades.
	if !errors.Is(err, ErrNeedsInstall) {
		t.Errorf("an out-of-date Backpack is not recognised as one to install over, "+
			"so this server is refused instead of upgraded.\ngot: %v", err)
	}
}

// And whatever the far machine printed does not become the panel's message.
//
// runOver returns the far side's stderr as the error, which is right for a
// short refusal and badly wrong for a program that answers with its usage: a
// screen of another machine's help was rendered into the add form, most of it
// describing commands this version does not have.
func TestTheFarMachinesHelpTextIsNotShownToTheOperator(t *testing.T) {
	isolateStore(t)
	srv := newFakeServer(t, "root", "hunter2")
	srv.old = true
	host, port := srv.addr()
	Add("kharej", host, port, "root", "hunter2")

	r := NewSSHRunner(nil)
	defer r.Close()
	err := r.Call("kharej", OpHello, nil, nil)
	if err == nil {
		t.Fatal("a server running an older Backpack answered successfully")
	}
	said := err.Error()

	for _, leaked := range []string{"node setup", "--setup-key", "node run", "Nodes → Add server"} {
		if strings.Contains(said, leaked) {
			t.Errorf("the far machine's help text reached the operator (%q):\n%s", leaked, said)
		}
	}
	if lines := strings.Count(said, "\n"); lines > 2 {
		t.Errorf("the message is %d lines of another machine's output:\n%s", lines+1, said)
	}
}

// Deleting a tunnel on a managed server is an operation the far side has.
//
// It deliberately did not: a delete on the panel's machine is not consent to
// one somewhere else, and there is no undo. What changed is who decides — the
// panel asks about the far end as its own question now, with the safe answer
// under every reflex, and sends this only when the answer was yes.
func TestTheFarSideCanBeAskedToDelete(t *testing.T) {
	isolateStore(t)
	srv := newFakeServer(t, "root", "hunter2")
	host, port := srv.addr()
	Add("kharej", host, port, "root", "hunter2")

	r := NewSSHRunner(nil)
	defer r.Close()

	var got struct {
		Op   string          `json:"op"`
		Body json.RawMessage `json:"body"`
	}
	if err := r.Call("kharej", OpDelete, NameRequest{Name: "test-kharej"}, &got); err != nil {
		t.Fatalf("the far side refused a delete: %v", err)
	}
	if got.Op != OpDelete {
		t.Errorf("the far side was asked for %q, not a delete", got.Op)
	}
	if !strings.Contains(string(got.Body), "test-kharej") {
		t.Errorf("the delete did not name the tunnel: %s", got.Body)
	}
}

// And the list carries what identifies a tunnel as one half of a pair, so the
// panel can work out which tunnel over there is the other end of one here
// without a round trip per candidate.
func TestTheListSaysWhereEachTunnelMeetsItsPeer(t *testing.T) {
	for _, f := range []string{"Role", "TunnelPort", "ServerHost"} {
		if !hasField(TunnelState{}, f) {
			t.Errorf("TunnelState has no %s, so a tunnel on a managed server cannot "+
				"be recognised as the other end of one here", f)
		}
	}
}

func hasField(v any, name string) bool {
	rt := reflect.TypeOf(v)
	_, ok := rt.FieldByName(name)
	return ok
}

package node

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
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
	refuse  string // the far side answers, and says no
}

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
				failing, prefix, refuse := s.failing, s.prefix, s.refuse
				s.mu.Unlock()

				if failing {
					fmt.Fprintln(ch.Stderr(), "sh: backpack: command not found")
					ch.SendRequest("exit-status", false, ssh.Marshal(struct{ S uint32 }{127}))
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
	if !strings.Contains(err.Error(), "not installed") {
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

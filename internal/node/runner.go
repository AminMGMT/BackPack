package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// Runner is how the panel reaches a managed server.
//
// It is the whole surface the rest of the panel ever used: ask one server to do
// one thing, and ask whether it can be reached at all. Everything else the old
// enrolment channel carried — the per-node listener, the setup token, the agent
// holding a session open — existed to make those two calls possible, not
// because anything needed them.
type Runner interface {
	// Call performs one operation on one server. body is encoded into the
	// request and out, when not nil, receives the answer.
	Call(name, op string, body, out any) error

	// IsOnline reports whether the server answered recently.
	IsOnline(name string) bool

	// Reachable is IsOnline with the reason it is not, for the one screen that
	// shows it. A server that is down and a server whose password was changed
	// are both "offline" and are not the same problem.
	Reachable(name string) (bool, string)

	// Forget drops what is held about one server, so the next call starts over.
	// Used when its address or login changes, and when it leaves the fleet.
	Forget(name string)
}

// SSHRunner is the one implementation. The interface exists so the panel's own
// tests can stand in for a fleet of real machines.
var _ Runner = (*SSHRunner)(nil)

// ErrOffline is what every call returns when a server cannot be reached. The
// wording is the operator's, not the protocol's: "offline" is a fact they can
// act on, where "no session" is not.
type ErrOffline struct {
	Name string
	Why  string
}

func (e ErrOffline) Error() string {
	if e.Why == "" {
		return fmt.Sprintf("%s is offline", e.Name)
	}
	return fmt.Sprintf("%s could not be reached: %s", e.Name, e.Why)
}

// SSHRunner drives managed servers over SSH.
type SSHRunner struct {
	pool *sshPool

	mu     sync.Mutex
	status map[string]reach
	onLog  func(string)
}

// reach is the last thing a server said, and when.
type reach struct {
	ok   bool
	why  string
	when time.Time
}

// reachTTL is how long a server's last answer stands for.
//
// The fleet page polls, and every card asks whether its server is up. Without
// this each poll would open a connection to every server in the fleet, so a
// panel managing ten of them would spend its time doing that and nothing else.
// Short enough that a server going down is noticed in the time it takes to
// wonder, long enough that a poll costs nothing.
const reachTTL = 20 * time.Second

// NewSSHRunner returns a runner that reaches servers over their own SSH.
func NewSSHRunner(onLog func(string)) *SSHRunner {
	if onLog == nil {
		onLog = func(string) {}
	}
	return &SSHRunner{pool: newPool(), status: map[string]reach{}, onLog: onLog}
}

// Close drops every connection the runner is holding.
func (r *SSHRunner) Close() { r.pool.closeAll() }

// targetFor reads one server's address and credentials.
func targetFor(name string) (SSHTarget, error) {
	n, ok := findWithSecret(name)
	if !ok {
		return SSHTarget{}, fmt.Errorf("no server called %q", name)
	}
	if n.Host == "" {
		return SSHTarget{}, fmt.Errorf("%s has no address recorded", name)
	}
	return SSHTarget{
		Host: n.Host, Port: n.SSHPort, User: n.User,
		Password: n.Password, Fingerprint: n.Fingerprint,
	}, nil
}

// Call performs one operation on one server.
func (r *SSHRunner) Call(name, op string, body, out any) error {
	t, err := targetFor(name)
	if err != nil {
		return err
	}
	resp, err := r.exec(context.Background(), name, t, Request{Op: op, Body: mustJSON(body)})
	if err != nil {
		r.note(name, false, err.Error())
		return ErrOffline{Name: name, Why: err.Error()}
	}
	r.note(name, true, "")
	if !resp.OK {
		return fmt.Errorf("%s", resp.Err)
	}
	if out != nil && len(resp.Body) > 0 {
		return json.Unmarshal(resp.Body, out)
	}
	return nil
}

// exec sends one request and decodes the answer.
func (r *SSHRunner) exec(ctx context.Context, name string, t SSHTarget, req Request) (Response, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}

	c, seen, err := r.pool.get(ctx, name, t)
	if err != nil {
		return Response{}, err
	}
	// First sight of a host key is recorded, so every connection after this one
	// is checked against it.
	if seen != "" && t.Fingerprint == "" {
		if err := NoteFingerprint(name, seen); err != nil {
			r.onLog("could not record " + name + "'s host key: " + err.Error())
		}
	}

	cmd := quote(app.BinPath) + " node exec " + quote(b64(raw))
	stdout, err := runOver(c, cmd, nil)
	if err != nil {
		// A connection that has gone stale looks exactly like a server that has
		// gone away, so the connection is dropped and the call tried once more
		// on a fresh one. Only once: a second failure is the server.
		r.pool.drop(name)
		c, _, derr := r.pool.get(ctx, name, t)
		if derr != nil {
			return Response{}, err
		}
		stdout, err = runOver(c, cmd, nil)
		if err != nil {
			return Response{}, backpackMissing(err)
		}
	}

	line := strings.TrimSpace(string(stdout))
	if line == "" {
		return Response{}, fmt.Errorf("%s answered nothing — is Backpack installed there?", name)
	}
	dec, err := base64.StdEncoding.DecodeString(lastLine(line))
	if err != nil {
		return Response{}, fmt.Errorf("%s answered something this panel could not read", name)
	}
	var resp Response
	if err := json.Unmarshal(dec, &resp); err != nil {
		return Response{}, fmt.Errorf("%s answered something this panel could not read", name)
	}
	return resp, nil
}

// lastLine is the answer, ignoring anything the far machine's shell printed
// first. A login banner or a message of the day is normal on a server somebody
// administers, and it arrives on stdout ahead of the answer.
func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}

// backpackMissing turns the shell's word for "no such command" into the thing
// the operator has to do about it.
func backpackMissing(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "not found") || strings.Contains(msg, "No such file") {
		return fmt.Errorf("Backpack is not installed on that server — add it from the " +
			"fleet page, or run the installer there once")
	}
	return err
}

// IsOnline reports whether a server answered recently, asking it if the last
// answer is old.
func (r *SSHRunner) IsOnline(name string) bool {
	r.mu.Lock()
	s, ok := r.status[name]
	r.mu.Unlock()
	if ok && time.Since(s.when) < reachTTL {
		return s.ok
	}
	err := r.Call(name, OpHello, nil, nil)
	return err == nil
}

// Reachable is IsOnline with the reason, for the one screen that shows it.
func (r *SSHRunner) Reachable(name string) (bool, string) {
	ok := r.IsOnline(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	return ok, r.status[name].why
}

// Forget drops what is remembered about one server, so the next look is fresh.
func (r *SSHRunner) Forget(name string) {
	r.pool.drop(name)
	r.mu.Lock()
	delete(r.status, name)
	r.mu.Unlock()
}

func (r *SSHRunner) note(name string, ok bool, why string) {
	r.mu.Lock()
	was, seen := r.status[name]
	r.status[name] = reach{ok: ok, why: why, when: time.Now()}
	r.mu.Unlock()
	if seen && was.ok != ok {
		if ok {
			r.onLog("server " + name + " is reachable again")
		} else {
			r.onLog("server " + name + " could not be reached: " + why)
		}
	}
}

func mustJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// Install puts Backpack on a server that does not have it, over the same SSH.
//
// This is what makes adding a server one action. The channel it replaces asked
// the operator to paste a line on the far machine, which meant leaving the
// panel, finding a terminal for a server they may only have a password for, and
// coming back — and if anything went wrong there, the panel's side of it was a
// server that never appeared and no reason why.
//
// The installer is fetched on the far machine from the same place it always
// was, rather than pushed from here: the archive and its checksum have to come
// from the same origin for verifying one against the other to prove anything,
// and a panel in the middle would be a second thing to trust.
//
// It is slow — a download and possibly a build — so the caller gives it room.
func (r *SSHRunner) Install(name string) (string, error) {
	t, err := targetFor(name)
	if err != nil {
		return "", err
	}
	c, seen, err := r.pool.get(context.Background(), name, t)
	if err != nil {
		return "", err
	}
	if seen != "" && t.Fingerprint == "" {
		_ = NoteFingerprint(name, seen)
	}

	// stdin is closed so the installer takes its own quiet path: with no
	// terminal it prints how to open the menu rather than opening one, and a
	// menu waiting for a keypress over SSH would hang until the timeout.
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/install.sh",
		app.RepoOwner, app.RepoName)
	cmd := "curl -fsSL " + quote(url) + " | bash < /dev/null 2>&1"

	out, err := runLong(c, cmd)
	if err != nil {
		return string(out), fmt.Errorf("installing Backpack on %s failed: %w", name, err)
	}
	return string(out), nil
}

// Upgrade reinstalls Backpack on a server, which is how a node is brought to
// the release the panel is on. It is the same script; the installer replaces
// the binary and restarts what was running.
func (r *SSHRunner) Upgrade(name string) (string, error) { return r.Install(name) }

package node

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Reaching a managed server over its own SSH.
//
// The enrolment channel this replaces asked a lot of the operator: open a port
// on the panel for every server, paste a setup command on the far machine, and
// keep an agent running there to hold the connection open. Three things to get
// right before anything works, and each of them a way for a server to be listed
// in the panel and unreachable anyway.
//
// SSH is already there. It is already open, already authenticated, already the
// way that machine is administered — and it dials outward from the panel, so
// the panel needs no inbound port at all. What crosses it is exactly what
// crossed the old channel: one Request in, one Response out, answered by
// Execute on the far side. The protocol did not change; only what carries it.

// SSHTarget is how to reach one server.
type SSHTarget struct {
	Host     string
	Port     int
	User     string
	Password string

	// Fingerprint is the host key this server presented when it was added.
	//
	// Empty means it has not been seen yet, and the first connection records
	// what it finds — trust on first use, which is the same bargain as typing
	// "yes" at ssh's own prompt. Afterwards it must match: accepting any key
	// would mean anything that can answer on this address gets a root shell
	// and the panel would never say a word about it.
	Fingerprint string
}

func (t SSHTarget) addr() string {
	p := t.Port
	if p == 0 {
		p = 22
	}
	return net.JoinHostPort(t.Host, strconv.Itoa(p))
}

// Fingerprint is the SHA-256 of a host key, in ssh's own display form.
func Fingerprint(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }

// ErrHostKeyChanged is returned when a server presents a different host key
// than the one recorded for it.
type ErrHostKeyChanged struct {
	Name, Had, Got string
}

func (e ErrHostKeyChanged) Error() string {
	return fmt.Sprintf("the host key for %s has changed — it was %s and is now %s. "+
		"Either that server was rebuilt, or something is answering in its place. "+
		"Remove it from the fleet and add it again if the change was expected.",
		e.Name, short(e.Had), short(e.Got))
}

func short(fp string) string {
	if len(fp) > 20 {
		return fp[:20] + "…"
	}
	return fp
}

const (
	sshDialTimeout    = 12 * time.Second
	sshIdle           = 90 * time.Second
	sshOpTimeout      = 60 * time.Second
	sshInstallTimeout = 20 * time.Minute
)

// dialSSH opens one connection and reports the host key it saw.
func dialSSH(ctx context.Context, name string, t SSHTarget) (*ssh.Client, string, error) {
	var seen string
	cfg := &ssh.ClientConfig{
		User:    t.User,
		Auth:    []ssh.AuthMethod{ssh.Password(t.Password)},
		Timeout: sshDialTimeout,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			seen = Fingerprint(key)
			if t.Fingerprint == "" {
				return nil // first sight; the caller records it
			}
			if seen != t.Fingerprint {
				return ErrHostKeyChanged{Name: name, Had: t.Fingerprint, Got: seen}
			}
			return nil
		},
	}

	d := net.Dialer{Timeout: sshDialTimeout}
	conn, err := d.DialContext(ctx, "tcp", t.addr())
	if err != nil {
		return nil, "", fmt.Errorf("could not reach %s over SSH: %w", t.addr(), err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, t.addr(), cfg)
	if err != nil {
		conn.Close()
		// A wrong password is the likeliest cause by a distance, and the
		// library's wording for it is not something an operator can act on.
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, seen, fmt.Errorf("%s refused the login for %q — check the username and password",
				t.Host, t.User)
		}
		return nil, seen, err
	}
	return ssh.NewClient(c, chans, reqs), seen, nil
}

// runOver runs one command and returns what it wrote to stdout.
//
// stderr is kept for the error message and never mixed into stdout: the answer
// is JSON, and a warning from the far machine's shell landing in the middle of
// it would be a parse failure with no explanation.
func runOver(c *ssh.Client, cmd string, stdin []byte) ([]byte, error) {
	s, err := c.NewSession()
	if err != nil {
		return nil, err
	}
	defer s.Close()

	var out, errb bytes.Buffer
	s.Stdout = &out
	s.Stderr = &errb
	if len(stdin) > 0 {
		s.Stdin = bytes.NewReader(stdin)
	}

	done := make(chan error, 1)
	go func() { done <- s.Run(cmd) }()
	select {
	case err = <-done:
	case <-time.After(sshOpTimeout):
		s.Signal(ssh.SIGKILL)
		return nil, fmt.Errorf("%s took longer than %s and was stopped", cmd, sshOpTimeout)
	}
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.Bytes(), fmt.Errorf("%s", msg)
	}
	return out.Bytes(), nil
}

// runLong runs a command that is allowed to take minutes.
//
// Installing Backpack downloads a release, and on a platform with no build for
// it, compiles one. The ordinary op timeout is for an operation the panel is
// waiting on with a page open; this is for a job the operator started knowing
// it would take a while.
func runLong(c *ssh.Client, cmd string) ([]byte, error) {
	s, err := c.NewSession()
	if err != nil {
		return nil, err
	}
	defer s.Close()

	var out bytes.Buffer
	s.Stdout = &out
	s.Stderr = &out

	done := make(chan error, 1)
	go func() { done <- s.Run(cmd) }()
	select {
	case err = <-done:
	case <-time.After(sshInstallTimeout):
		s.Signal(ssh.SIGKILL)
		return out.Bytes(), fmt.Errorf("it was still running after %s and was stopped", sshInstallTimeout)
	}
	if err != nil {
		return out.Bytes(), fmt.Errorf("%s", lastMeaningful(out.String()))
	}
	return out.Bytes(), nil
}

// lastMeaningful is the most recent line that says something, for an error
// message. An installer prints a great deal and the useful part is at the end.
func lastMeaningful(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return "it failed and said nothing"
}

// sshPool keeps one connection per server.
//
// A connection costs a TCP round trip, a key exchange and an authentication —
// a few hundred milliseconds — and the fleet page asks every server for its
// state on every poll. Dialling each time would make the panel slower the more
// servers it manages, which is the wrong way round. Idle connections are closed
// so a server that is only touched occasionally is not held open all day.
type sshPool struct {
	mu    sync.Mutex
	conns map[string]*pooled
}

type pooled struct {
	c    *ssh.Client
	used time.Time
}

func newPool() *sshPool { return &sshPool{conns: map[string]*pooled{}} }

// get returns a live connection, dialling if there is none, and reports the
// host key when it had to dial.
func (p *sshPool) get(ctx context.Context, name string, t SSHTarget) (*ssh.Client, string, error) {
	p.mu.Lock()
	if e := p.conns[name]; e != nil {
		if time.Since(e.used) < sshIdle {
			e.used = time.Now()
			c := e.c
			p.mu.Unlock()
			return c, "", nil
		}
		e.c.Close()
		delete(p.conns, name)
	}
	p.mu.Unlock()

	c, seen, err := dialSSH(ctx, name, t)
	if err != nil {
		return nil, seen, err
	}
	p.mu.Lock()
	if old := p.conns[name]; old != nil {
		old.c.Close()
	}
	p.conns[name] = &pooled{c: c, used: time.Now()}
	p.mu.Unlock()
	return c, seen, nil
}

// drop closes and forgets one server's connection, so the next call dials.
func (p *sshPool) drop(name string) {
	p.mu.Lock()
	if e := p.conns[name]; e != nil {
		e.c.Close()
		delete(p.conns, name)
	}
	p.mu.Unlock()
}

// closeAll drops every connection.
func (p *sshPool) closeAll() {
	p.mu.Lock()
	for name, e := range p.conns {
		e.c.Close()
		delete(p.conns, name)
	}
	p.mu.Unlock()
}

// quote makes one shell argument safe. The values that reach a command line
// here are names the panel validated, but a quoting rule that depends on
// validation somewhere else is a rule that breaks when the validation moves.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// b64 is how a request travels: the far side is given it as an argument rather
// than on stdin, because a shell that is handed both a heredoc and a command
// has more ways to go wrong than one handed a single opaque word.
func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

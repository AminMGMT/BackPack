package webui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/backpack/backpack/internal/node"
)

// What a rollout accepts as a healthy canary.
//
// This is the decision the whole staged rollout rests on. If it is too
// generous the staging proves nothing and the fleet goes down anyway; if it is
// too strict the first node halts every rollout for ever.

// verifyRunner answers OpHello and OpList from a fixed set.
type verifyRunner struct {
	helloErr error
	listErr  error
	tunnels  []node.TunnelState
}

func (r *verifyRunner) Call(name, op string, body, out any) error {
	switch op {
	case node.OpHello:
		if r.helloErr != nil {
			return r.helloErr
		}
		return nil
	case node.OpList:
		if r.listErr != nil {
			return r.listErr
		}
		raw, _ := json.Marshal(r.tunnels)
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (r *verifyRunner) IsOnline(string) bool            { return true }
func (r *verifyRunner) Reachable(string) (bool, string) { return true, "" }
func (r *verifyRunner) Forget(string)                   {}

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

func TestACanaryMustDoMoreThanAnswer(t *testing.T) {
	s := newServer()

	cases := []struct {
		name    string
		tunnels []node.TunnelState
		wantErr string // substring, "" for healthy
	}{
		{
			name: "everything up and paired",
			tunnels: []node.TunnelState{
				{Name: "a", Enabled: true, Active: true, Connected: yes()},
			},
		},
		{
			// The weakest check, and the one a rollout is most tempted to stop
			// at.
			name: "the unit did not come back",
			tunnels: []node.TunnelState{
				{Name: "a", Enabled: true, Active: false},
			},
			wantErr: "did not come back",
		},
		{
			// The process is there and the tunnel is not up. This is exactly
			// the canary that passes a rollout which only asks systemd.
			name: "running without a peer",
			tunnels: []node.TunnelState{
				{Name: "a", Enabled: true, Active: true, Connected: no()},
			},
			wantErr: "without a peer",
		},
		{
			// Up, paired, and every connection dying one step past the end of
			// it. Only the node can see this, which is why it is on the wire.
			name: "delivering into nothing",
			tunnels: []node.TunnelState{
				{Name: "a", Enabled: true, Active: true, Connected: yes(),
					ServiceDown: "127.0.0.1:8080 refused"},
			},
			wantErr: "delivering into nothing",
		},
		{
			// A node still on an older build has no opinion about Connected.
			// Reading absent as "not connected" would halt every rollout on
			// its first node until the fleet had already been upgraded — which
			// is the one thing a rollout cannot do.
			name: "an older build says nothing about its control channel",
			tunnels: []node.TunnelState{
				{Name: "a", Enabled: true, Active: true, Connected: nil},
			},
		},
		{
			// A tunnel that is switched off on purpose is not a failure.
			name: "a disabled tunnel is not a fault",
			tunnels: []node.TunnelState{
				{Name: "a", Enabled: false, Active: false},
			},
		},
		{
			// A spare server with nothing on it must not stop a rollout.
			name:    "a node with no tunnels",
			tunnels: nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.verifyNode(&verifyRunner{tunnels: c.tunnels}, "kharej-1")
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("a healthy node was rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted a node that is %s", c.name)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error %q does not say %q", err, c.wantErr)
			}
			// Whatever it was, it has to name the server — a rollout halts on
			// this message and the operator's next question is "which one".
			if !strings.Contains(err.Error(), "kharej-1") {
				t.Errorf("the failure does not name the server: %v", err)
			}
		})
	}
}

// The failures are reported in order of how much they prove, so an operator
// reading one message is told the most specific thing that is wrong.
func TestTheMostSpecificFailureIsReported(t *testing.T) {
	s := newServer()
	err := s.verifyNode(&verifyRunner{tunnels: []node.TunnelState{
		{Name: "dead", Enabled: true, Active: false},
		{Name: "unpaired", Enabled: true, Active: true, Connected: no()},
		{Name: "empty", Enabled: true, Active: true, Connected: yes(), ServiceDown: "refused"},
	}}, "kharej-1")
	if err == nil {
		t.Fatal("accepted a node with three different failures on it")
	}
	// The unit that is not running is the most basic thing wrong, so it is
	// what gets reported first.
	if !strings.Contains(err.Error(), "did not come back") {
		t.Fatalf("reported %q rather than the unit that is down", err)
	}
}

// A node that cannot be reached at all, and one that will not say what it is
// running, are both failures — a canary that has gone silent is not a canary
// that passed.
func TestANodeThatWillNotAnswerFailsVerification(t *testing.T) {
	s := newServer()
	if err := s.verifyNode(nil, "x"); err == nil {
		t.Error("accepted a node with no runner at all")
	}
	if err := s.verifyNode(&verifyRunner{helloErr: errFake}, "x"); err == nil {
		t.Error("accepted a node that did not answer")
	}
	if err := s.verifyNode(&verifyRunner{listErr: errFake}, "x"); err == nil {
		t.Error("accepted a node that would not say what it is running")
	}
}

var errFake = errorString("the server went away")

type errorString string

func (e errorString) Error() string { return string(e) }

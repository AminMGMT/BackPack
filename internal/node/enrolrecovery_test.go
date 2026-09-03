package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The failure these exist for is silent and permanent.
//
// Answering an enrolment spends the setup token. If the node does not survive
// the moment between that answer being sent and the key being written to disk —
// stopped, restarted, a connection that drops — then the token is gone and the
// key was never stored. Nothing reports it. The node retries, is told its token
// is not valid, and the operator has a server that the panel lists as enrolled
// and that can never connect. The only way out is to notice, remove the node
// and start again, and there is nothing on either machine that says so.
//
// So the token is claimed rather than burned, and stays usable until the node
// confirms the key landed.

func TestARetryAfterALostKeyEnrolsAgain(t *testing.T) {
	isolate(t)
	token, _, err := NewEnrollToken("kharej", 9101)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	first, err := Redeem(token, Info{})
	if err != nil {
		t.Fatalf("first enrolment: %v", err)
	}

	// The node never stored that key. It still holds the token, so it tries
	// again — which is the whole point.
	second, err := Redeem(token, Info{})
	if err != nil {
		t.Fatalf("the node could not enrol again after losing its key: %v", err)
	}
	if second.Name != first.Name {
		t.Errorf("the retry enrolled as %q rather than %q", second.Name, first.Name)
	}
	if second.Key == first.Key {
		t.Error("the retry was handed the key that was already lost; whoever holds " +
			"the old one demonstrably never stored it")
	}
	if second.Port != first.Port {
		t.Errorf("the retry moved to port %d from %d", second.Port, first.Port)
	}
	if got := List(); len(got) != 1 {
		t.Errorf("the retry left %d node records, not one", len(got))
	}
	if _, ok := ByKey(first.Key); ok {
		t.Error("the lost key still authenticates")
	}
	if _, ok := ByKey(second.Key); !ok {
		t.Error("the key from the retry does not authenticate")
	}
}

func TestConfirmingRetiresTheToken(t *testing.T) {
	isolate(t)
	token, _, err := NewEnrollToken("kharej", 9102)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if _, err := Redeem(token, Info{}); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if err := ConfirmEnrolment("kharej"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := Redeem(token, Info{}); err == nil {
		t.Fatal("the setup token still worked after the node said it had the key")
	}
}

// Authenticating settles it too, which is what covers a node old enough not to
// send the confirmation.
func TestUsingTheKeyRetiresTheToken(t *testing.T) {
	isolate(t)
	token, _, err := NewEnrollToken("kharej", 9103)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	n, err := Redeem(token, Info{})
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if _, ok := ByKey(n.Key); !ok {
		t.Fatal("the freshly issued key did not authenticate")
	}
	if _, err := Redeem(token, Info{}); err == nil {
		t.Fatal("the setup token still worked after the node had used its key")
	}
}

// A token that stays live forever is a secret that stays live forever, so the
// window closes on its own whether or not anything confirms.
func TestTheGraceWindowCloses(t *testing.T) {
	isolate(t)
	token, _, err := NewEnrollToken("kharej", 9104)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if _, err := Redeem(token, Info{}); err != nil {
		t.Fatalf("enrol: %v", err)
	}

	// Age the claim past the grace.
	s := LoadStore()
	if len(s.Pending) != 1 {
		t.Fatalf("the claimed token was not kept: %d pending", len(s.Pending))
	}
	s.Pending[0].Claimed = time.Now().Add(-enrollGrace - time.Minute).Unix()
	if err := SaveStore(s); err != nil {
		t.Fatalf("save: %v", err)
	}

	_, err = Redeem(token, Info{})
	if err == nil {
		t.Fatal("a claimed token was still accepted long after the grace window")
	}
	if !strings.Contains(err.Error(), "already been used") {
		t.Errorf("the refusal does not say the token was used: %v", err)
	}
	if len(LoadStore().Pending) != 0 {
		t.Error("the spent token was left in the store")
	}
}

// Retrying must not push the window along in front of itself, or a node stuck
// in a restart loop would hold the token open indefinitely.
func TestRetryingDoesNotExtendTheWindow(t *testing.T) {
	isolate(t)
	token, _, err := NewEnrollToken("kharej", 9105)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if _, err := Redeem(token, Info{}); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	claimed := LoadStore().Pending[0].Claimed

	time.Sleep(1100 * time.Millisecond) // the stamp is unix seconds
	if _, err := Redeem(token, Info{}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := LoadStore().Pending[0].Claimed; got != claimed {
		t.Errorf("the retry moved the claim stamp from %d to %d, which pushes the "+
			"window out every time the node tries", claimed, got)
	}
}

// A claimed token is not a setup the operator still has to do.
func TestAClaimedTokenIsNotListedAsWaiting(t *testing.T) {
	isolate(t)
	token, _, err := NewEnrollToken("kharej", 9106)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if len(PendingList()) != 1 {
		t.Fatalf("a fresh token is not listed as waiting: %d", len(PendingList()))
	}
	if _, err := Redeem(token, Info{}); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if got := PendingList(); len(got) != 0 {
		t.Errorf("a node that has enrolled is still listed as waiting to be set up: %+v", got)
	}
}

// Withdrawing the enrolment has to withdraw the retry as well.
func TestRemovingANodeClosesTheWindow(t *testing.T) {
	isolate(t)
	token, _, err := NewEnrollToken("kharej", 9107)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if _, err := Redeem(token, Info{}); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if err := Remove("kharej"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := Redeem(token, Info{}); err == nil {
		t.Fatal("a removed node's setup token still enrolled it")
	}
}

// End to end: a real node that enrols confirms it, so the token is dead by the
// time the panel reports the node online — the confirmation is read before the
// session is registered, which is what makes that ordering reliable.
func TestARealEnrolmentRetiresTheTokenBeforeItIsOnline(t *testing.T) {
	isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := startHub(t, ctx)
	addr, key := openFor(t, h, "kharej")
	hk, token, _ := ParseSetupKey(key)

	go NewAgent(AgentConfig{Server: addr, HubKey: hk, Enroll: token}, func(string) {}).Run(ctx)
	waitOnline(t, h, "kharej", true)

	if len(LoadStore().Pending) != 0 {
		t.Error("the setup token was still live once the node was online; it should " +
			"be retired the moment the node says it has the key")
	}
	if _, err := Redeem(token, Info{}); err == nil {
		t.Error("the setup command could be pasted a second time after the node was up")
	}
}

// The whole failure, run for real: a node that enrols and cannot write the key
// must be able to enrol again with the command the operator already has.
//
// The disk is what fails here rather than the timing, because it is the same
// loss with a switch on it — the panel has answered and spent the token, and
// the node ends the session holding nothing. Before the token was claimed
// rather than burned, the retry below was refused and that server could never
// join the fleet.
func TestANodeThatCannotStoreItsKeyCanStillEnrol(t *testing.T) {
	isolate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := startHub(t, ctx)
	addr, key := openFor(t, h, "kharej")
	hk, token, _ := ParseSetupKey(key)

	// Somewhere the key cannot be written: the parent is a regular file, so
	// creating the directory under it fails whoever the tests run as.
	good := AgentPath
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("blocker: %v", err)
	}
	AgentPath = filepath.Join(blocker, "node-agent.json")

	first, firstStop := context.WithCancel(ctx)
	go NewAgent(AgentConfig{Server: addr, HubKey: hk, Enroll: token}, func(string) {}).Run(first)

	// Wait for the panel to have answered the enrolment — the token is spent
	// from this moment, and the node still has nothing.
	deadline := time.Now().Add(5 * time.Second)
	for len(List()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the node never enrolled")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := LoadAgent(); err == nil {
		t.Fatal("the node stored a key after all; this test proves nothing")
	}
	firstStop()

	// Same server, same setup command, somewhere it can write this time.
	AgentPath = good
	second, secondStop := context.WithCancel(ctx)
	defer secondStop()
	go NewAgent(AgentConfig{Server: addr, HubKey: hk, Enroll: token}, func(string) {}).Run(second)

	waitOnline(t, h, "kharej", true)

	saved, err := LoadAgent()
	if err != nil {
		t.Fatalf("the node came up without storing its credential: %v", err)
	}
	if saved.NodeKey == "" {
		t.Fatal("the node saved a configuration with no key in it")
	}
	if saved.Enroll != "" {
		t.Error("the node would try to enrol again on its next start")
	}
	if got := List(); len(got) != 1 {
		t.Errorf("the fleet holds %d records for one server", len(got))
	}
}

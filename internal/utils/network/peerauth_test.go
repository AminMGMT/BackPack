package network

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func handshake(t *testing.T, client, server PeerKey, label string) (*PeerSession, *PeerSession, bool) {
	t.Helper()
	h, err := client.NewPeerHello(label)
	if err != nil {
		t.Fatal(err)
	}
	wire, ok := DecodePeerHello(h.Encode())
	if !ok || !server.VerifyHello(wire, label) {
		return nil, nil, false
	}
	r, srv, err := server.Reply(wire, label)
	if err != nil {
		t.Fatal(err)
	}
	back, ok := DecodePeerReply(r.Encode())
	if !ok {
		t.Fatal("reply did not decode")
	}
	cli, ok := client.CheckReply(h, back, label)
	if !ok {
		return nil, nil, false
	}
	if !srv.CheckConfirm(cli.Confirm()) {
		t.Fatal("a genuine confirm was refused")
	}
	return cli, srv, true
}

func TestBothEndsProveTheTokenWithoutSendingIt(t *testing.T) {
	k := PeerKeyFrom("a-long-test-token")
	cli, srv, ok := handshake(t, k, k, "tcp")
	if !ok {
		t.Fatal("two ends with the same token did not complete the handshake")
	}
	// Nothing that crossed the wire contains the token.
	h, _ := k.NewPeerHello("tcp")
	r, _, _ := k.Reply(h, "tcp")
	for _, m := range []string{h.Encode(), r.Encode(), cli.Confirm(), srv.SealTarget("127.0.0.1:443")} {
		if strings.Contains(m, "a-long-test-token") {
			t.Fatalf("the token crossed the wire in %q", m)
		}
	}
	if cli.PoolTag() != srv.PoolTag() {
		t.Fatal("the two ends derived different pool tags")
	}
}

func TestADifferentTokenFailsAtEachStep(t *testing.T) {
	right, wrong := PeerKeyFrom("a-long-test-token"), PeerKeyFrom("not-the-same-token")
	h, _ := wrong.NewPeerHello("tcp")
	if right.VerifyHello(h, "tcp") {
		t.Error("a hello under another token was accepted")
	}
	// Something posing as the server, without the token, cannot answer.
	h, _ = right.NewPeerHello("tcp")
	r, _, _ := wrong.Reply(h, "tcp")
	if _, ok := right.CheckReply(h, r, "tcp"); ok {
		t.Error("a reply from something without the token was accepted")
	}
	// A hello for one transport is not one for another.
	if right.VerifyHello(h, "udp") {
		t.Error("a tcp hello was accepted as a udp one")
	}
}

func TestAReplayedHelloCannotConfirm(t *testing.T) {
	k := PeerKeyFrom("a-long-test-token")
	h, _ := k.NewPeerHello("tcp")
	_, first, _ := k.Reply(h, "tcp")
	cliFirst, _ := k.CheckReply(h, mustReply(t, k, h, first), "tcp")
	_ = cliFirst
	// The attacker replays h and gets a reply with a new Ns; the confirm it
	// saw for the first run does not fit the second.
	_, second, _ := k.Reply(h, "tcp")
	if second.CheckConfirm(first.Confirm()) {
		t.Fatal("a confirm from an earlier run was accepted")
	}
}

func mustReply(t *testing.T, k PeerKey, h PeerHello, _ *PeerSession) PeerReply {
	t.Helper()
	r, _, err := k.Reply(h, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestOnlyTheServersDestinationsAreDialled(t *testing.T) {
	k := PeerKeyFrom("a-long-test-token")
	cli, srv, _ := handshake(t, k, k, "tcpmux")
	got, err := cli.OpenTarget(srv.SealTarget("127.0.0.1:8443"))
	if err != nil || got != "127.0.0.1:8443" {
		t.Fatalf("a sealed destination: %q, %v", got, err)
	}
	for _, forged := range []string{
		"10.0.0.5:22",                           // no seal
		"10.0.0.5:22" + srv.SealTarget("x")[1:], // someone else's seal
		strings.Replace(srv.SealTarget("127.0.0.1:8443"), "8443", "8444", 1), // altered
	} {
		if _, err := cli.OpenTarget(forged); err == nil {
			t.Errorf("forged destination %q was accepted", forged)
		}
	}
	// Another session's seal is not this one's.
	other, _, _ := handshake(t, k, k, "tcpmux")
	if _, err := other.OpenTarget(srv.SealTarget("127.0.0.1:8443")); err == nil {
		t.Error("a destination sealed for one session opened under another")
	}
	// A nil session is an older peer: destinations pass as they are.
	var legacy *PeerSession
	if got, err := legacy.OpenTarget("127.0.0.1:80"); err != nil || got != "127.0.0.1:80" || legacy.SealTarget("a") != "a" {
		t.Error("the previous handshake's destinations were changed")
	}
}

func TestTheGuardRemembersAProvedPeerAcrossRestarts(t *testing.T) {
	pin := PeerPinPath(filepath.Join(t.TempDir(), "iran.toml"))
	g := NewPeerGuard("tok-en-12345678", "", pin)
	if g.Strict() {
		t.Fatal("a fresh guard refuses older peers")
	}
	// Two closed attempts tip a client over to the previous handshake, once.
	if g.Missed() || !g.Missed() || !g.UseLegacy() || g.Missed() {
		t.Fatal("the fallback did not happen exactly at the threshold, once")
	}
	g.Rearm()
	if g.UseLegacy() {
		t.Fatal("rearming kept the fallback")
	}
	g.Proved()
	if !g.Strict() || g.UseLegacy() {
		t.Fatal("a proved peer still allows the previous handshake")
	}
	if !NewPeerGuard("tok-en-12345678", "", pin).Strict() {
		t.Fatal("the proof was not remembered by the next run")
	}
	if NewPeerGuard("another-token-123", "", pin).Strict() {
		t.Fatal("a new token inherited the old token's proof")
	}
	// A proved guard never falls back, however many attempts close.
	g2 := NewPeerGuard("tok-en-12345678", "", pin)
	for i := 0; i < 5; i++ {
		g2.Missed()
	}
	if g2.UseLegacy() {
		t.Fatal("a proved peer fell back")
	}
	if fi, err := os.Stat(pin); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("pin file: %v %v", fi, err)
	}
	if !NewPeerGuard("t", PeerAuthRequired, "").Strict() {
		t.Fatal("peer_auth = required is not strict")
	}
}

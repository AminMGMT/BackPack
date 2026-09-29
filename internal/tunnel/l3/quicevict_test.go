package l3

import (
	"fmt"
	"testing"
)

func peersFor(entries ...struct{ wrote, added int64 }) map[string]*quicPeer {
	m := map[string]*quicPeer{}
	for i, e := range entries {
		p := &quicPeer{added: e.added}
		p.wrote.Store(e.wrote)
		m[fmt.Sprintf("p%d", i)] = p
	}
	return m
}

// A full listener lets a stranger go, never the peer the tunnel is talking to,
// however long ago it last wrote and however new the strangers are.
func TestAFullQUICListenerNeverEvictsTheTrustedPeerForAStranger(t *testing.T) {
	type e = struct{ wrote, added int64 }
	peers := peersFor(e{wrote: 5, added: 1}) // the genuine peer, idle a while
	for i := 0; i < 15; i++ {
		peers[fmt.Sprintf("s%d", i)] = &quicPeer{added: int64(100 + i)} // strangers, never written to
	}
	if v := evictionVictim(peers); v == "p0" {
		t.Fatal("the trusted peer was chosen over a stranger")
	}
	if v := evictionVictim(peers); v != "s0" {
		t.Fatalf("victim %q, want the oldest stranger s0", v)
	}
}

// With no stranger left, the peer written to least recently goes.
func TestWithoutStrangersTheLeastRecentlyWrittenGoes(t *testing.T) {
	type e = struct{ wrote, added int64 }
	peers := peersFor(e{wrote: 30, added: 1}, e{wrote: 10, added: 2}, e{wrote: 20, added: 3})
	if v := evictionVictim(peers); v != "p1" {
		t.Fatalf("victim %q, want p1 (written longest ago)", v)
	}
}

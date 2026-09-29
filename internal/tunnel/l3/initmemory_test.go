package l3

import (
	"context"
	"net"
	"testing"
	"time"
)

// A stranger cannot make the listener forget a handshake it has answered.
//
// The memory of answered handshakes holds initMemory identifiers, and it used
// to record every identifier it was shown before the handshake behind it had
// authenticated. Anyone who could reach the port — no token needed — could
// send initMemory handshakes with made-up identifiers and push the genuine
// ones out, after which a recorded genuine handshake was answered again as if
// it were new. Only a handshake that authenticates is remembered now.
func TestStrangersCannotPushAnsweredHandshakesOutOfMemory(t *testing.T) {
	const token = "a-token-worth-replaying"
	dev := newFakeDevice(1400)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	listener, err := New(Config{
		Mode: ModeListen, Addr: "127.0.0.1:0", Token: token, Encap: "gre",
		LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1400,
	}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	listener.openDevice = func(deviceSpec) (packetDevice, error) { return dev, nil }
	start(t, ctx, cancel, listener, dev)
	bound := awaitBind(t, listener)

	peer, err := net.Dial("udp", bound.String())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	answered := func(d []byte) bool {
		if _, err := peer.Write(d); err != nil {
			t.Fatal(err)
		}
		_ = peer.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, err := peer.Read(make([]byte, 2048))
		return err == nil
	}

	// A genuine handshake, then a second one, so the first is no longer the
	// one a retransmission would be answered from.
	first, err := beginHandshake(token, 0, "gre")
	if err != nil {
		t.Fatal(err)
	}
	if !answered(first.datagram()) {
		t.Fatal("the genuine handshake was not answered")
	}
	second, err := beginHandshake(token, first.id, "gre")
	if err != nil {
		t.Fatal(err)
	}
	if !answered(second.datagram()) {
		t.Fatal("the second genuine handshake was not answered")
	}

	// A stranger's flood: handshakes under a token it does not have.
	for i := 0; i < initMemory+8; i++ {
		junk, err := beginHandshake("not-the-token", 0, "gre")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := peer.Write(junk.datagram()); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(200 * time.Millisecond)

	if answered(first.datagram()) {
		t.Fatal("a replayed handshake was answered again after strangers flooded the memory")
	}
}

package l3

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/snispoof"
)

// a carrier that records what was written and replays what it is given.
type recordingCarrier struct {
	wrote [][]byte
	feed  chan []byte
}

func (c *recordingCarrier) WriteTo(p []byte, _ net.Addr) (int, error) {
	c.wrote = append(c.wrote, append([]byte(nil), p...))
	return len(p), nil
}
func (c *recordingCarrier) ReadFrom(p []byte) (int, net.Addr, error) {
	msg := <-c.feed
	return copy(p, msg), &net.UDPAddr{}, nil
}
func (c *recordingCarrier) Close() error                     { return nil }
func (c *recordingCarrier) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *recordingCarrier) SetDeadline(time.Time) error      { return nil }
func (c *recordingCarrier) SetReadDeadline(time.Time) error  { return nil }
func (c *recordingCarrier) SetWriteDeadline(time.Time) error { return nil }
func (c *recordingCarrier) Overhead() int                    { return 40 }
func (c *recordingCarrier) CarrierName() string              { return "recording" }

// The hello goes out once, ahead of the first datagram, and never again.
//
// Ahead of it because that is when a classifier decides what a flow is; once
// because a hello per datagram would be both a waste and a pattern.
func TestTheHelloLeadsTheFlowAndIsSentOnce(t *testing.T) {
	below := &recordingCarrier{}
	hello, err := snispoof.BuildHello("example.ir")
	if err != nil {
		t.Fatal(err)
	}
	c := &sniCarrier{DatagramCarrier: below, hello: hello}

	for i := 0; i < 3; i++ {
		if _, err := c.WriteTo([]byte{byte(i)}, nil); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if len(below.wrote) != 4 {
		t.Fatalf("%d writes reached the wire, want 4 (one hello and three datagrams)", len(below.wrote))
	}
	if !snispoof.LooksLikeHello(below.wrote[0]) {
		t.Error("the first thing on the wire was not the hello")
	}
	for i, w := range below.wrote[1:] {
		if len(w) != 1 || w[0] != byte(i) {
			t.Errorf("datagram %d came out as %x", i, w)
		}
	}
}

// The far end never sees it. The hello is for the box in between, and handing
// it up would put 517 bytes of unsealed nonsense into the tunnel.
func TestThePeersHelloIsDroppedBeforeTheTunnelSeesIt(t *testing.T) {
	below := &recordingCarrier{feed: make(chan []byte, 3)}
	hello, err := snispoof.BuildHello("example.ir")
	if err != nil {
		t.Fatal(err)
	}
	c := &sniCarrier{DatagramCarrier: below, hello: hello}

	peerHello, err := snispoof.BuildHello("something.else")
	if err != nil {
		t.Fatal(err)
	}
	below.feed <- peerHello
	below.feed <- []byte("a sealed datagram")

	buf := make([]byte, 2048)
	n, _, err := c.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(buf[:n], []byte("a sealed datagram")) {
		t.Fatalf("the tunnel was handed %q", buf[:n])
	}
}

// And the name it announces is the one that was configured.
func TestTheConfiguredDomainIsWhatGoesOut(t *testing.T) {
	below := &recordingCarrier{}
	hello, err := snispoof.BuildHello("www.digikala.com")
	if err != nil {
		t.Fatal(err)
	}
	c := &sniCarrier{DatagramCarrier: below, hello: hello}
	c.WriteTo([]byte("x"), nil)

	got, ok := snispoof.SNIOf(below.wrote[0])
	if !ok || got != "www.digikala.com" {
		t.Errorf("the hello names %q", got)
	}
	if c.CarrierName() != CarrierSNI {
		t.Errorf("CarrierName() = %q", c.CarrierName())
	}
}

package l3

import (
	"net"
	"testing"
	"time"
)

// memCarrier is a carrier that records what it was asked to send, so the
// spreading can be observed without a network.
type memCarrier struct {
	name string
	sent int
	peer net.Addr
	in   chan []byte
}

func newMemCarrier(name string) *memCarrier {
	return &memCarrier{name: name, in: make(chan []byte, 64),
		peer: &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 9000}}
}

func (m *memCarrier) WriteTo(p []byte, _ net.Addr) (int, error) { m.sent++; return len(p), nil }
func (m *memCarrier) ReadFrom(p []byte) (int, net.Addr, error) {
	b, ok := <-m.in
	if !ok {
		return 0, nil, net.ErrClosed
	}
	return copy(p, b), m.peer, nil
}
func (m *memCarrier) Close() error                     { return nil }
func (m *memCarrier) LocalAddr() net.Addr              { return m.peer }
func (m *memCarrier) SetDeadline(time.Time) error      { return nil }
func (m *memCarrier) SetReadDeadline(time.Time) error  { return nil }
func (m *memCarrier) SetWriteDeadline(time.Time) error { return nil }
func (m *memCarrier) Overhead() int                    { return 28 }
func (m *memCarrier) CarrierName() string              { return m.name }

// The point of the layer: the traffic is spread evenly, because an uneven
// spread is an uneven set of flows and a shaper counting them would throttle
// the busy one exactly as it throttled the single socket.
func TestMultipathSpreadsWritesEvenly(t *testing.T) {
	a, b, c := newMemCarrier("udp"), newMemCarrier("udp"), newMemCarrier("udp")
	mp := newMultipathCarrier([]DatagramCarrier{a, b, c}, a.peer)

	const packets = 90
	for i := 0; i < packets; i++ {
		if _, err := mp.WriteTo([]byte("x"), nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []*memCarrier{a, b, c} {
		if p.sent != packets/3 {
			t.Errorf("a path carried %d of %d packets, want an even %d", p.sent, packets, packets/3)
		}
	}
}

// Reads from every path surface on the one carrier, so the tunnel above reads a
// single stream and never has to know there is more than one socket.
func TestMultipathMergesReadsFromEveryPath(t *testing.T) {
	a, b := newMemCarrier("udp"), newMemCarrier("udp")
	mp := newMultipathCarrier([]DatagramCarrier{a, b}, a.peer)
	defer mp.Close()

	a.in <- []byte("from-a")
	b.in <- []byte("from-b")

	seen := map[string]bool{}
	buf := make([]byte, 64)
	for i := 0; i < 2; i++ {
		_ = mp.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := mp.ReadFrom(buf)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		seen[string(buf[:n])] = true
	}
	if !seen["from-a"] || !seen["from-b"] {
		t.Errorf("not every path was merged: %v", seen)
	}
}

// The address handed up must not move between paths. The tunnel above follows a
// peer that changes address, and several ports arriving in turn would read as a
// peer moving on every packet — re-keying the send path continuously.
func TestMultipathReportsOneStableAddress(t *testing.T) {
	a, b := newMemCarrier("udp"), newMemCarrier("udp")
	b.peer = &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 9001} // a different port
	stable := a.peer
	mp := newMultipathCarrier([]DatagramCarrier{a, b}, stable)
	defer mp.Close()

	a.in <- []byte("one")
	b.in <- []byte("two")
	buf := make([]byte, 64)
	for i := 0; i < 2; i++ {
		_ = mp.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, addr, err := mp.ReadFrom(buf)
		if err != nil {
			t.Fatal(err)
		}
		if addr.String() != stable.String() {
			t.Errorf("read %d reported %s, want the stable %s", i, addr, stable)
		}
	}
}

// It adds nothing to the wire, so the MTU is a single socket's.
func TestMultipathAddsNoOverhead(t *testing.T) {
	a, b := newMemCarrier("udp"), newMemCarrier("udp")
	mp := newMultipathCarrier([]DatagramCarrier{a, b}, a.peer)
	defer mp.Close()
	if mp.Overhead() != a.Overhead() {
		t.Errorf("overhead = %d, want a single path's %d", mp.Overhead(), a.Overhead())
	}
	if name := mp.CarrierName(); name != "udp×2" {
		t.Errorf("carrier name = %q, want it to say how many paths", name)
	}
}

// One path is the ordinary tunnel and must not be wrapped at all: the merge
// layer would add a goroutine, a queue and a copy for nothing.
func TestASinglePathIsNotWrapped(t *testing.T) {
	a := newMemCarrier("udp")
	if got := newMultipathCarrier([]DatagramCarrier{a}, a.peer); got != DatagramCarrier(a) {
		t.Error("a single path was wrapped in the merge layer")
	}
}

// Both ends derive the same ports from one configured address, so the set is
// consecutive from the base and a base too near the top is refused rather than
// wrapping into nonsense.
func TestPathPortsAreConsecutiveFromTheBase(t *testing.T) {
	for i, want := range map[int]string{0: "1.2.3.4:9000", 1: "1.2.3.4:9001", 3: "1.2.3.4:9003"} {
		got, err := pathAddr("1.2.3.4:9000", i)
		if err != nil {
			t.Fatalf("path %d: %v", i, err)
		}
		if got != want {
			t.Errorf("path %d = %q, want %q", i, got, want)
		}
	}
	if _, err := pathAddr("1.2.3.4:65535", 1); err == nil {
		t.Error("a port past 65535 was accepted")
	}
	if _, err := pathAddr("no-port", 0); err == nil {
		t.Error("an address with no port was accepted")
	}
}

// A count the carrier cannot serve is refused where it is written.
func TestMultipathValidation(t *testing.T) {
	for _, tc := range []struct {
		n       int
		wantErr bool
	}{{0, false}, {1, false}, {4, false}, {maxPaths, false}, {maxPaths + 1, true}, {-1, true}} {
		err := MultipathConfig{Paths: tc.n}.Validate()
		if (err != nil) != tc.wantErr {
			t.Errorf("paths=%d: err = %v, wantErr %v", tc.n, err, tc.wantErr)
		}
	}
	if (MultipathConfig{Paths: 1}).Enabled() {
		t.Error("one path reported as multipath")
	}
	if !(MultipathConfig{Paths: 2}).Enabled() {
		t.Error("two paths not reported as multipath")
	}
}

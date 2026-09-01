package l3

import (
	"bytes"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// lossyPipe is a carrier pair joined in memory, with a drop rule. It is how the
// FEC layer is exercised against loss without a network: what matters is which
// packets arrive, not how they travelled.
type lossyPipe struct {
	mu    sync.Mutex
	queue [][]byte
	peer  *lossyPipe
	drop  func(n int) bool // called with the packet's ordinal; true drops it
	sent  int
	addr  net.Addr
}

func newLossyPair(drop func(n int) bool) (*lossyPipe, *lossyPipe) {
	a := &lossyPipe{addr: &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 1}}
	b := &lossyPipe{addr: &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 2}, drop: drop}
	a.peer, b.peer = b, a
	return a, b
}

func (p *lossyPipe) WriteTo(b []byte, _ net.Addr) (int, error) {
	p.mu.Lock()
	n := p.sent
	p.sent++
	dropIt := p.drop != nil && p.drop(n)
	p.mu.Unlock()
	if dropIt {
		return len(b), nil // the wire ate it
	}
	p.peer.mu.Lock()
	p.peer.queue = append(p.peer.queue, append([]byte(nil), b...))
	p.peer.mu.Unlock()
	return len(b), nil
}

func (p *lossyPipe) ReadFrom(b []byte) (int, net.Addr, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		return 0, nil, fmt.Errorf("empty")
	}
	pkt := p.queue[0]
	p.queue = p.queue[1:]
	return copy(b, pkt), p.peer.addr, nil
}

func (p *lossyPipe) Close() error                     { return nil }
func (p *lossyPipe) LocalAddr() net.Addr              { return p.addr }
func (p *lossyPipe) SetDeadline(time.Time) error      { return nil }
func (p *lossyPipe) SetReadDeadline(time.Time) error  { return nil }
func (p *lossyPipe) SetWriteDeadline(time.Time) error { return nil }
func (p *lossyPipe) Overhead() int                    { return 28 }
func (p *lossyPipe) CarrierName() string              { return "pipe" }

// drain reads until the carrier runs dry, returning every payload delivered.
func drain(t *testing.T, c DatagramCarrier) [][]byte {
	t.Helper()
	var out [][]byte
	buf := make([]byte, 2048)
	for {
		n, _, err := c.ReadFrom(buf)
		if err != nil {
			return out
		}
		out = append(out, append([]byte(nil), buf[:n]...))
	}
}

// The point of the whole layer: a packet the wire dropped is rebuilt from the
// parity and delivered anyway, with nothing retransmitted.
func TestFECRebuildsADroppedPacket(t *testing.T) {
	// Drop the third data packet of the first group. With 4+2 the group has two
	// parity packets, so one loss is repairable.
	send, recv := newLossyPair(nil)
	send.drop = func(n int) bool { return n == 2 }

	tx, err := newFECCarrier(send, FECConfig{Data: 4, Parity: 2})
	if err != nil {
		t.Fatal(err)
	}
	rx, err := newFECCarrier(recv, FECConfig{Data: 4, Parity: 2})
	if err != nil {
		t.Fatal(err)
	}

	var want [][]byte
	for i := 0; i < 4; i++ {
		msg := []byte(fmt.Sprintf("packet-%d-%s", i, bytes.Repeat([]byte("x"), i*7)))
		want = append(want, msg)
		if _, err := tx.WriteTo(msg, recv.addr); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	got := drain(t, rx)
	if len(got) != len(want) {
		t.Fatalf("delivered %d packets, want %d — the dropped one was not rebuilt", len(got), len(want))
	}
	// Order is not promised: a rebuilt packet arrives after the parity that
	// rebuilt it. What is promised is that every packet arrives, intact.
	for _, w := range want {
		found := false
		for _, g := range got {
			if bytes.Equal(g, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("packet %q never arrived", w)
		}
	}
}

// Two losses in a 4+2 group are still repairable; three are not, and the layer
// must degrade rather than deliver garbage.
func TestFECRepairsUpToItsParityAndNoFurther(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dropped   map[int]bool
		wantCount int
	}{
		{"one loss", map[int]bool{1: true}, 4},
		{"two losses — exactly the parity", map[int]bool{0: true, 3: true}, 4},
		{"three losses — beyond repair", map[int]bool{0: true, 1: true, 2: true}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			send, recv := newLossyPair(nil)
			send.drop = func(n int) bool { return tc.dropped[n] }

			tx, _ := newFECCarrier(send, FECConfig{Data: 4, Parity: 2})
			rx, _ := newFECCarrier(recv, FECConfig{Data: 4, Parity: 2})
			for i := 0; i < 4; i++ {
				if _, err := tx.WriteTo([]byte(fmt.Sprintf("p%d", i)), recv.addr); err != nil {
					t.Fatal(err)
				}
			}
			if got := len(drain(t, rx)); got != tc.wantCount {
				t.Errorf("delivered %d, want %d", got, tc.wantCount)
			}
		})
	}
}

// With nothing lost, every packet arrives once — the layer must not duplicate a
// packet it also rebuilt, and must not hold anything back.
func TestFECDeliversEachPacketOnceOnACleanPath(t *testing.T) {
	send, recv := newLossyPair(nil)
	tx, _ := newFECCarrier(send, FECConfig{Data: 4, Parity: 2})
	rx, _ := newFECCarrier(recv, FECConfig{Data: 4, Parity: 2})

	const groups = 3
	for i := 0; i < 4*groups; i++ {
		if _, err := tx.WriteTo([]byte(fmt.Sprintf("p%02d", i)), recv.addr); err != nil {
			t.Fatal(err)
		}
	}
	got := drain(t, rx)
	if len(got) != 4*groups {
		t.Fatalf("delivered %d packets, want %d — a clean path must not lose or duplicate", len(got), 4*groups)
	}
	seen := map[string]int{}
	for _, g := range got {
		seen[string(g)]++
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("%q was delivered %d times", k, n)
		}
	}
}

// The MTU has to account for this layer, or the tunnel sends packets the path
// cannot carry and stalls on the large ones.
func TestFECAddsItsOverheadToTheCarrierBelow(t *testing.T) {
	send, _ := newLossyPair(nil)
	tx, _ := newFECCarrier(send, FECConfig{Data: 4, Parity: 2})
	if got, want := tx.Overhead(), send.Overhead()+fecOverhead; got != want {
		t.Errorf("overhead = %d, want %d (the carrier below plus this layer)", got, want)
	}
	if !bytes.Contains([]byte(tx.CarrierName()), []byte("fec4/2")) {
		t.Errorf("the carrier does not name its scheme: %q", tx.CarrierName())
	}
}

// An unusable scheme is refused where it is configured. Both ends must agree on
// the numbers, so a half-written pair is a mistake to report, not to repair.
func TestFECSchemeValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     FECConfig
		wantErr bool
	}{
		{"off", FECConfig{}, false},
		{"normal", FECConfig{Data: 10, Parity: 3}, false},
		{"parity with no data", FECConfig{Parity: 3}, true},
		{"data with no parity", FECConfig{Data: 10}, true},
		{"more parity than payload", FECConfig{Data: 4, Parity: 4}, true},
		{"too many shards", FECConfig{Data: 250, Parity: 20}, true},
	} {
		err := tc.cfg.Validate()
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
	// And a disabled scheme leaves the carrier below untouched rather than
	// wrapping it in a layer that does nothing.
	send, _ := newLossyPair(nil)
	got, err := newFECCarrier(send, FECConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != DatagramCarrier(send) {
		t.Error("a disabled scheme still wrapped the carrier")
	}
}

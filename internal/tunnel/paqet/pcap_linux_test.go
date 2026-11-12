//go:build linux && cgo

package paqet

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/backpack/backpack/internal/utils/network"
)

func TestAListenerWithNoGatewayAnswersEachClientAtTheAddressItsSegmentsCameFrom(t *testing.T) {
	j := &pcapInjector{path: &network.PckPath{Ethernet: true}, link: true}
	if !j.WantsNextHops() {
		t.Fatal("an injector with no gateway should want to learn next hops")
	}
	a, b := net.HardwareAddr{2, 0, 0, 0, 0, 1}, net.HardwareAddr{2, 0, 0, 0, 0, 2}
	if j.nextHop(net.ParseIP("203.0.113.5")) != nil {
		t.Fatal("a next hop appeared from nowhere")
	}
	j.LearnNextHop(net.ParseIP("203.0.113.5"), a)
	j.LearnNextHop(net.ParseIP("198.51.100.9"), b)
	if got := j.nextHop(net.ParseIP("203.0.113.5")); got.String() != a.String() {
		t.Errorf("203.0.113.5 -> %v, want %v", got, a)
	}
	if got := j.nextHop(net.ParseIP("198.51.100.9")); got.String() != b.String() {
		t.Errorf("198.51.100.9 -> %v, want %v", got, b)
	}
	// A client that moves behind another router is followed.
	j.LearnNextHop(net.ParseIP("203.0.113.5"), b)
	if got := j.nextHop(net.ParseIP("203.0.113.5")); got.String() != b.String() {
		t.Errorf("after the move: %v, want %v", got, b)
	}
}

func TestAGatewayTheHostFoundOrWasToldAlwaysWinsOverWhatWasLearned(t *testing.T) {
	gw := net.HardwareAddr{2, 0, 0, 0, 0, 9}
	j := &pcapInjector{path: &network.PckPath{Ethernet: true, NextHop: gw}, link: true}
	if j.WantsNextHops() {
		t.Fatal("with a gateway there is nothing to learn")
	}
	j.LearnNextHop(net.ParseIP("203.0.113.5"), net.HardwareAddr{2, 0, 0, 0, 0, 1})
	if got := j.nextHop(net.ParseIP("203.0.113.5")); got.String() != gw.String() {
		t.Errorf("got %v, want the gateway %v", got, gw)
	}
}

func TestACaptureFilterTheLibraryAcceptsIsWhatTheEngineBuilds(t *testing.T) {
	// The expressions are compiled by libpcap, which is the only judge of them.
	for _, f := range []string{
		CaptureFilter(true, 9000, 9000, net.ParseIP("10.0.0.1"), nil, 0),
		CaptureFilter(false, 33000, 33127, net.ParseIP("10.0.0.1"), net.ParseIP("203.0.113.9"), 9000),
	} {
		if err := checkFilter(f); err != nil {
			t.Errorf("%q: %v", f, err)
		}
	}
}

// writeAfterClose stands for a pcap handle: a send after Close is what hands
// libpcap a freed handle and crashes the process.
type writeAfterClose struct {
	closed, late atomic.Bool
	sent         atomic.Int64
}

func (w *writeAfterClose) WritePacketData([]byte) error {
	if w.closed.Load() {
		w.late.Store(true)
	}
	w.sent.Add(1)
	return nil
}
func (w *writeAfterClose) Close() { w.closed.Store(true) }

// KCP flushes from goroutines of its own, so segments are still being sent
// when a generation ends and the wire closes the injector.
func TestNoSegmentReachesTheHandleOnceTheInjectorIsClosed(t *testing.T) {
	gw := net.HardwareAddr{2, 0, 0, 0, 0, 9}
	path := &network.PckPath{Ethernet: true, NextHop: gw, SrcMAC: net.HardwareAddr{2, 0, 0, 0, 0, 1},
		LocalIP: net.IPv4(10, 0, 0, 1).To4()}
	for round := 0; round < 50; round++ {
		hs := []*writeAfterClose{{}, {}}
		j := newPcapInjector([]packetWriter{hs[0], hs[1]}, path, 1 /* Ethernet */, nil)
		var wg sync.WaitGroup
		stop := make(chan struct{})
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				// Two flows per handle.
				dst := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 9443 + i}
				for {
					select {
					case <-stop:
						return
					default:
					}
					if err := j.Inject(40000, []byte("segment"), dst); err != nil && !errors.Is(err, net.ErrClosed) {
						t.Error(err)
						return
					}
				}
			}(i)
		}
		for hs[0].sent.Load() < 10 || hs[1].sent.Load() < 10 {
		}
		j.Close()
		close(stop)
		wg.Wait()
		if hs[0].late.Load() || hs[1].late.Load() {
			t.Fatal("a segment was handed to a pcap handle after it was closed")
		}
	}
}

// Each flow keeps to one send handle, so its segments leave in order, and the
// flows of different sessions spread over the handles.
func TestEachFlowKeepsToOneSendHandleAndFlowsSpreadOverThem(t *testing.T) {
	gw := net.HardwareAddr{2, 0, 0, 0, 0, 9}
	path := &network.PckPath{Ethernet: true, NextHop: gw, SrcMAC: net.HardwareAddr{2, 0, 0, 0, 0, 1},
		LocalIP: net.IPv4(10, 0, 0, 1).To4()}
	hs := []*writeAfterClose{{}, {}, {}, {}}
	j := newPcapInjector([]packetWriter{hs[0], hs[1], hs[2], hs[3]}, path, 1, nil)
	dst := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 9000}
	// A client's sessions: consecutive source ports to one server port.
	for src := uint16(40000); src < 40004; src++ {
		before := make([]int64, 4)
		for i, h := range hs {
			before[i] = h.sent.Load()
		}
		for range 10 {
			j.Inject(src, []byte("x"), dst)
		}
		moved := 0
		for i, h := range hs {
			if h.sent.Load() != before[i] {
				moved++
			}
		}
		if moved != 1 {
			t.Fatalf("source port %d used %d handles, want exactly one", src, moved)
		}
	}
	for i, h := range hs {
		if h.sent.Load() != 10 {
			t.Errorf("handle %d carried %d segments: four sessions should use four handles", i, h.sent.Load())
		}
	}
}

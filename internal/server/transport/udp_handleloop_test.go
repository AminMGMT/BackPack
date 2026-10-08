package transport

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// A failed reply ends the whole flow even while the other worker waits for a
// packet; it must not occupy admission capacity until the 60-second idle timer.
func TestUDPReplyFailureReleasesBothCopyWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	peer := pc.LocalAddr().(*net.UDPAddr)
	pc.Close()
	local := &LocalUDPConn{addr: peer, listener: pc, payload: make(chan []byte, 1)}
	tunnel := &TunnelUDPConn{addr: peer, listener: pc, payload: make(chan []byte, 1)}
	tunnel.payload <- []byte("reply")
	active := map[string]*LocalUDPConn{peer.String(): local}
	mu := &sync.Mutex{}
	lim := newLimiter(Limits{MaxConnections: 1})
	if !lim.acquire() {
		t.Fatal("cannot acquire flow slot")
	}
	s := &UdpTransport{config: &UdpConfig{}, limits: lim, lifecycle: lifecycle{logger: quietLogger()}, activeConnections: map[string]*TunnelUDPConn{peer.String(): tunnel}}
	g := &udpGen{ctx: ctx}
	done := make(chan struct{})
	go func() { defer close(done); s.udpCopy(g, local, tunnel, &active, mu) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("copy workers leaked")
		}
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reply failure waited for the other direction's idle timeout")
	}
	if ctx.Err() != nil {
		t.Fatal("flow failure canceled the whole generation")
	}
	if len(active) != 0 || len(s.activeConnections) != 0 || lim.active.Load() != 0 {
		t.Fatal("failed flow retained its entries or connection slot")
	}
}

// The udp transport gives up on a forwarded flow that has waited more than
// three seconds for a tunnel connection — which happens whenever the pool is
// momentarily empty, and always happens when traffic arrives before the client
// has finished connecting.
//
// Giving up has to mean giving up on the whole flow. It used to stop working on
// it while leaving the source address in the table, and nothing else ever
// removed that entry: every later datagram from that peer was filed against a
// payload channel no goroutine was reading, so the peer went silent for good
// and only a service restart brought it back. That is the "UDP worked, then
// stopped" report. Here the entry has to be gone, so the peer's next datagram
// starts a fresh flow.
func TestUDPHandleLoopReleasesAFlowItGivesUpOn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &UdpTransport{config: &UdpConfig{}, lifecycle: lifecycle{logger: quietLogger()}}
	g := &udpGen{ctx: ctx, tunnelChannel: make(chan *TunnelUDPConn)}

	peer := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 7), Port: 51820}
	key := peer.String()

	stale := &LocalUDPConn{
		// Older than the cutoff: this is a flow that queued behind another one
		// while the pool had nothing to pair it with.
		timeCreated: time.Now().UnixMilli() - 5000,
		payload:     make(chan []byte, 8),
		remoteAddr:  "9000",
		addr:        peer,
	}

	active := map[string]*LocalUDPConn{key: stale}
	mu := &sync.Mutex{}
	udpChan := make(chan *LocalUDPConn, 1)
	udpChan <- stale

	go s.handleLoop(g, udpChan, &active, mu)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		_, stillThere := active[key]
		mu.Unlock()
		if !stillThere {
			// And the flow's channel is closed, so anything still holding it
			// learns the flow is over instead of blocking on it forever.
			select {
			case _, open := <-stale.payload:
				if open {
					t.Error("the abandoned flow's payload channel was left open")
				}
			default:
				t.Error("the abandoned flow's payload channel was left open")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a flow the loop gave up on stayed in the table — every later datagram " +
		"from that source would be filed against a channel nobody reads")
}

// A flow that is still fresh must be paired, not dropped: the cutoff exists to
// shed a backlog, and shedding a flow that just arrived would drop the first
// datagram of every new session.
func TestUDPHandleLoopPairsAFreshFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &UdpTransport{config: &UdpConfig{}, lifecycle: lifecycle{logger: quietLogger()}}
	g := &udpGen{ctx: ctx, tunnelChannel: make(chan *TunnelUDPConn, 1)}

	peer := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 8), Port: 51821}
	key := peer.String()

	// A tunnel socket for the loop to pair with. Nothing is read from it; the
	// point is only that the flow is taken up rather than discarded.
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	fresh := &LocalUDPConn{
		timeCreated: time.Now().UnixMilli(),
		payload:     make(chan []byte, 8),
		remoteAddr:  "9000",
		addr:        peer,
		listener:    pc,
	}
	active := map[string]*LocalUDPConn{key: fresh}
	mu := &sync.Mutex{}
	udpChan := make(chan *LocalUDPConn, 1)
	udpChan <- fresh

	go s.handleLoop(g, udpChan, &active, mu)

	g.tunnelChannel <- &TunnelUDPConn{
		timeCreated: time.Now().UnixNano(),
		payload:     make(chan []byte, 8),
		addr:        pc.LocalAddr().(*net.UDPAddr),
		listener:    pc,
		ping:        make(chan struct{}, 1),
		mu:          &sync.Mutex{},
	}

	// Give the pairing a moment, then confirm the flow was kept.
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	_, stillThere := active[key]
	mu.Unlock()
	if !stillThere {
		t.Error("a flow that had only just arrived was discarded")
	}
}

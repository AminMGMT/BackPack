package transport

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
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

type udpAdmissionEndHook struct {
	cancel   context.CancelFunc
	limits   *limiter
	observed chan struct{}
	once     atomic.Bool
}

func (*udpAdmissionEndHook) Levels() []logrus.Level { return logrus.AllLevels }
func (h *udpAdmissionEndHook) Fire(entry *logrus.Entry) error {
	if strings.HasPrefix(entry.Message, "accepted UDP connection from ") && h.once.CompareAndSwap(false, true) {
		// Pause only at the existing production publication/first-send boundary.
		h.cancel()
		until := time.Now().Add(time.Second)
		for h.limits.active.Load() != 0 && time.Now().Before(until) {
			time.Sleep(time.Millisecond)
		}
		close(h.observed)
	}
	return nil
}

func TestUDPOpeningPacketCanRaceGenerationShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lim := newLimiter(Limits{MaxConnections: 1})
	log := quietLogger()
	log.SetLevel(logrus.DebugLevel)
	hook := &udpAdmissionEndHook{cancel: cancel, limits: lim, observed: make(chan struct{})}
	log.AddHook(hook)
	s := &UdpTransport{config: &UdpConfig{ChannelSize: 1}, limits: lim, lifecycle: lifecycle{logger: log}}
	g := &udpGen{ctx: ctx, tunnelChannel: make(chan *TunnelUDPConn), reqNewConnChan: make(chan struct{}, 1)}
	addr := freeAddr(t)
	done := make(chan struct{})
	go func() { defer close(done); s.localListener(g, addr, "127.0.0.1:9") }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("listener leaked")
		}
	})
	peer, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		peer.Write([]byte("first"))
		select {
		case <-hook.observed:
			if lim.active.Load() != 0 {
				t.Fatal("generation shutdown did not release flow")
			}
			time.Sleep(50 * time.Millisecond)
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("listener did not admit a flow")
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

// A fresh flow must expire while the tunnel pool stays empty, without restarting
// the generation. Its payload channel and admission slot must be released too.
func TestUDPHandleLoopExpiresFreshFlowWithoutTunnel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lim := newLimiter(Limits{MaxConnections: 1})
	s := &UdpTransport{config: &UdpConfig{}, limits: lim, lifecycle: lifecycle{logger: quietLogger()}}
	g := &udpGen{ctx: ctx, tunnelChannel: make(chan *TunnelUDPConn, 1)}
	peer := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
	flow := &LocalUDPConn{timeCreated: nowMillis(), payload: make(chan []byte, 1), remoteAddr: "127.0.0.1:8080", addr: peer}
	active := map[string]*LocalUDPConn{peer.String(): flow}
	mu := &sync.Mutex{}
	queue := make(chan *LocalUDPConn, 1)
	if !lim.acquire() {
		t.Fatal("slot acquisition failed")
	}
	queue <- flow
	done := make(chan struct{})
	go func() { defer close(done); s.handleLoop(g, queue, &active, mu) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("pairing worker leaked")
		}
		until := time.Now().Add(time.Second)
		for lim.active.Load() != 0 && time.Now().Before(until) {
			time.Sleep(time.Millisecond)
		}
		if lim.active.Load() != 0 {
			t.Error("forwarded flow did not release its slot on cancellation")
		}
	})
	until := time.Now().Add(pairingTimeout + 500*time.Millisecond)
	for time.Now().Before(until) {
		mu.Lock()
		remaining := len(active)
		mu.Unlock()
		if remaining == 0 && lim.active.Load() == 0 {
			select {
			case _, open := <-flow.payload:
				if open {
					t.Fatal("expired payload channel left open")
				}
			default:
				t.Fatal("expired payload channel not closed")
			}
			if !lim.acquire() {
				t.Fatal("timed-out flow exhausted limit")
			}
			if ctx.Err() != nil {
				t.Fatal("timeout required canceling generation")
			}
			pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				lim.release()
				t.Fatal(err)
			}
			defer pc.Close()
			next := &LocalUDPConn{timeCreated: nowMillis(), payload: make(chan []byte, 1), remoteAddr: "127.0.0.1:8081", addr: peer, listener: pc}
			mu.Lock()
			active[peer.String()] = next
			mu.Unlock()
			queue <- next
			g.tunnelChannel <- &TunnelUDPConn{payload: make(chan []byte, 1), addr: pc.LocalAddr().(*net.UDPAddr), listener: pc, ping: make(chan struct{}), mu: &sync.Mutex{}}
			if err := pc.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 64)
			n, _, err := pc.ReadFromUDP(buf)
			if err != nil {
				t.Fatalf("pairing worker did not serve the next flow: %v", err)
			}
			if string(buf[:n]) != next.remoteAddr {
				t.Fatalf("next flow target = %q, want %q", buf[:n], next.remoteAddr)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	remaining := len(active)
	mu.Unlock()
	t.Fatalf("fresh flow timeout did not expire: remaining=%d slots=%d", remaining, lim.active.Load())
}

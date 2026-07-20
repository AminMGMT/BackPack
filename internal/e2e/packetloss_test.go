package e2e

import (
	"fmt"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// lossyRelay sits between a client and a server and throws away a share of the
// datagrams passing through it, in both directions.
//
// This is what makes the claim "KCP repairs packet loss" testable. Without it,
// KCP and plain UDP look identical on a clean loopback, and a change that
// silently disabled forward error correction would pass every other test.
type lossyRelay struct {
	// Addr is the address clients should dial instead of the real server.
	Addr string

	conn     *net.UDPConn
	upstream *net.UDPAddr
	// lossPct is changed while the relay is running (the partition test cuts
	// and restores the path), so it is read and written atomically.
	lossPct atomic.Int64

	dropped atomic.Int64
	passed  atomic.Int64

	mu      sync.Mutex
	clients map[string]*net.UDPConn
	closed  atomic.Bool
	rnd     *rand.Rand
	rndMu   sync.Mutex
}

// startLossyRelay forwards UDP to upstreamAddr, dropping lossPct% of datagrams.
func startLossyRelay(t *testing.T, upstreamAddr string, lossPct int) *lossyRelay {
	t.Helper()

	up, err := net.ResolveUDPAddr("udp", upstreamAddr)
	if err != nil {
		t.Fatalf("cannot resolve the upstream address: %v", err)
	}
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("cannot start the lossy relay: %v", err)
	}

	r := &lossyRelay{
		Addr:     pc.LocalAddr().String(),
		conn:     pc,
		upstream: up,
		clients:  make(map[string]*net.UDPConn),
		rnd:      rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	r.lossPct.Store(int64(lossPct))
	go r.run()
	t.Cleanup(r.Stop)
	return r
}

// shouldDrop reports whether this datagram is one of the unlucky ones.
func (r *lossyRelay) shouldDrop() bool {
	r.rndMu.Lock()
	defer r.rndMu.Unlock()
	return int64(r.rnd.Intn(100)) < r.lossPct.Load()
}

func (r *lossyRelay) run() {
	buf := make([]byte, 65535)
	for {
		n, from, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return // relay closed
		}
		if r.shouldDrop() {
			r.dropped.Add(1)
			continue
		}
		r.passed.Add(1)

		// One upstream socket per client address, so replies can be routed back
		// to the right peer.
		key := from.String()
		r.mu.Lock()
		out, ok := r.clients[key]
		if !ok {
			out, err = net.DialUDP("udp", nil, r.upstream)
			if err != nil {
				r.mu.Unlock()
				continue
			}
			r.clients[key] = out
			go r.pumpBack(out, from)
		}
		r.mu.Unlock()

		payload := make([]byte, n)
		copy(payload, buf[:n])
		out.Write(payload)
	}
}

// pumpBack carries the server's replies to one client, dropping its share too:
// real loss is not one-directional.
func (r *lossyRelay) pumpBack(out *net.UDPConn, client *net.UDPAddr) {
	buf := make([]byte, 65535)
	for {
		n, err := out.Read(buf)
		if err != nil {
			return
		}
		if r.shouldDrop() {
			r.dropped.Add(1)
			continue
		}
		r.passed.Add(1)
		if r.closed.Load() {
			return
		}
		payload := make([]byte, n)
		copy(payload, buf[:n])
		r.conn.WriteToUDP(payload, client)
	}
}

// SetLoss changes the share of datagrams thrown away, while the relay runs.
func (r *lossyRelay) SetLoss(pct int) { r.lossPct.Store(int64(pct)) }

func (r *lossyRelay) Stop() {
	if !r.closed.CompareAndSwap(false, true) {
		return
	}
	r.conn.Close()
	r.mu.Lock()
	for _, c := range r.clients {
		c.Close()
	}
	r.mu.Unlock()
}

// Stats returns how many datagrams were dropped and forwarded.
func (r *lossyRelay) Stats() (dropped, passed int64) {
	return r.dropped.Load(), r.passed.Load()
}

// TestKCPSurvivesPacketLoss is the test that justifies the KCP transport
// existing at all: with a tenth of the datagrams thrown away in both
// directions, data must still arrive complete and uncorrupted.
func TestKCPSurvivesPacketLoss(t *testing.T) {
	backend := startEchoBackend(t)

	// Start a server, then point the client at a relay that eats packets.
	// startTunnel would wait for readiness through the healthy path, so the
	// pieces are wired up by hand here.
	tunnelPort := freePort(t)
	entryPort := freePort(t)
	const token = "kcp-loss-test-token-0123456789ab"

	relay := startLossyRelay(t, fmt.Sprintf("127.0.0.1:%d", tunnelPort), 10)

	tun := startTunnelVia(t, "kcp", backend, tunnelPort, entryPort, token, relay.Addr)

	payload := randomPayload(t, 256*1024)
	if err := tun.roundTrip(payload); err != nil {
		t.Fatalf("KCP failed to deliver 256 KiB across a 10%% loss link: %v", err)
	}

	dropped, passed := relay.Stats()
	if dropped == 0 {
		t.Fatalf("the relay dropped nothing, so this test proved nothing "+
			"(passed %d datagrams)", passed)
	}
	t.Logf("delivered intact with %d datagrams dropped and %d forwarded (%.1f%% loss)",
		dropped, passed, float64(dropped)/float64(dropped+passed)*100)
}

// TestKCPSurvivesHeavyPacketLoss pushes it further. At this rate a plain
// retransmit-only protocol slows to a crawl; forward error correction is what
// keeps it usable.
func TestKCPSurvivesHeavyPacketLoss(t *testing.T) {
	if testing.Short() {
		t.Skip("slow by design — skipped under -short")
	}
	backend := startEchoBackend(t)

	tunnelPort := freePort(t)
	entryPort := freePort(t)
	const token = "kcp-heavy-loss-token-0123456789a"

	relay := startLossyRelay(t, fmt.Sprintf("127.0.0.1:%d", tunnelPort), 25)
	tun := startTunnelVia(t, "kcp", backend, tunnelPort, entryPort, token, relay.Addr)

	payload := randomPayload(t, 64*1024)
	if err := tun.roundTrip(payload); err != nil {
		t.Fatalf("KCP failed to deliver 64 KiB across a 25%% loss link: %v", err)
	}

	dropped, passed := relay.Stats()
	t.Logf("delivered intact with %d datagrams dropped and %d forwarded (%.1f%% loss)",
		dropped, passed, float64(dropped)/float64(dropped+passed)*100)
}

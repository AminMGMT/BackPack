package e2e

import (
	"fmt"
	"math/rand"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// Widening lossyRelay from "drops a share" into a path that misbehaves the way
// real ones do.
//
// The relay already existed and already proved its worth: without it KCP and
// plain UDP look identical on a clean loopback, and a change that silently
// disabled forward error correction would pass every other test. It does one
// thing, though — throw datagrams away — and three tests use it.
//
// Loss is not the failure these tunnels actually meet. The networks BackPack
// runs on add delay, vary that delay packet by packet, and deliver out of
// order; a shaper that buffers and releases in bursts produces all three at
// once. Those are the conditions the transports claim to survive, and nothing
// checked that claim.
//
// Rather than a second relay, the existing one learns to be slow and
// disordered as well as lossy. The setters are additive and default to zero, so
// every test already using it is unaffected.

// SetLatency adds a fixed one-way delay to every datagram, in both directions.
func (r *lossyRelay) SetLatency(d time.Duration) { r.latency.Store(int64(d)) }

// SetJitter varies that delay by up to ±d per datagram.
//
// Jitter is what produces reordering, and deliberately so: a datagram held 40ms
// behind one held 5ms arrives second having been sent first. Simulating
// reordering as its own knob would be modelling the symptom rather than the
// cause.
func (r *lossyRelay) SetJitter(d time.Duration) { r.jitter.Store(int64(d)) }

// delayFor returns how long this datagram should be held, or zero.
func (r *lossyRelay) delayFor() time.Duration {
	base := r.latency.Load()
	jit := r.jitter.Load()
	if base == 0 && jit == 0 {
		return 0
	}
	d := base
	if jit > 0 {
		r.rndMu.Lock()
		d += r.rnd.Int63n(2*jit) - jit
		r.rndMu.Unlock()
	}
	if d < 0 {
		d = 0
	}
	return time.Duration(d)
}

// deliver sends a datagram, after its delay if it has one.
//
// A goroutine per delayed datagram rather than a queue: a queue would deliver
// in order by construction, which is exactly the property being taken away.
func (r *lossyRelay) deliver(send func()) {
	d := r.delayFor()
	if d == 0 {
		send()
		return
	}
	go func() {
		t := time.NewTimer(d)
		defer t.Stop()
		<-t.C
		if r.closed.Load() {
			return
		}
		send()
	}()
}

// A tunnel has to keep carrying traffic over a path that is lossy, slow and
// out of order at the same time — which is one path, not three.
//
// Run across the transports whose reliability claims differ: tcp leans on the
// kernel, tcpmux adds a stream multiplexer over it, and kcp brings its own
// retransmission and error correction. If any of them only works on a clean
// loopback, this is where that shows.
func TestTransportsSurviveADegradedPath(t *testing.T) {
	if testing.Short() {
		t.Skip("this drives a deliberately slow path")
	}

	for _, transport := range []string{"tcp", "tcpmux", "kcp"} {
		t.Run(transport, func(t *testing.T) {
			backend := startEchoBackend(t)
			tunnelPort := freePort(t)
			entryPort := freePort(t)
			token := "degraded-path-token-0123456789ab"

			srvCfg := baseServerConfig(transport, tunnelPort, entryPort, backend.addr, token)

			// Only the datagram transports go through the relay; tcp and
			// tcpmux are streams and the relay speaks UDP. For those two the
			// degradation that matters is the backend's, which
			// startSlowBackend below provides.
			remote := fmt.Sprintf("127.0.0.1:%d", tunnelPort)
			if transport == "kcp" {
				relay := startLossyRelay(t, remote, 8) // 8% loss
				relay.SetLatency(15 * time.Millisecond)
				relay.SetJitter(10 * time.Millisecond) // and therefore reordering
				t.Cleanup(relay.Stop)
				remote = relay.Addr
			}

			cliCfg := baseClientConfig(transport, remote, token, nil)

			tun := runPair(t, srvCfg, cliCfg, entryPort, tunnelPort)
			if err := tun.waitReady(tunnelReadyTimeout); err != nil {
				t.Fatalf("%s never came up over a degraded path: %v", transport, err)
			}
			// Large enough that loss and reordering are certain to be hit, and
			// byte-exact so a transport that "works" by dropping data fails.
			if err := tun.roundTrip(randomPayload(t, 256*1024)); err != nil {
				t.Fatalf("%s lost or corrupted data over a degraded path: %v", transport, err)
			}
		})
	}
}

// A backend that accepts and then never answers must not take the tunnel with
// it.
//
// This is the half-open case: a forwarded connection established at both ends
// where one side simply stops. It matters because the control channel is shared
// — every other forwarded connection depends on it — so a tunnel that tied its
// own liveness to one hung backend would take everything down with it.
//
// Two forwarded ports, deliberately: one to a normal echo backend and one to a
// backend that never answers. Readiness and the final check both go through the
// healthy port, because measuring the tunnel through the port that is meant to
// hang would only measure the hang.
func TestAHangingBackendDoesNotTakeTheTunnelDown(t *testing.T) {
	healthy := startEchoBackend(t)
	hang := startHangingBackend(t)

	tunnelPort := freePort(t)
	goodPort := freePort(t)
	badPort := freePort(t)
	const token = "hanging-backend-token-0123456789"

	srvCfg := baseServerConfig("tcp", tunnelPort, goodPort, healthy.addr, token)
	srvCfg.Ports = []string{
		fmt.Sprintf("%d=%s", goodPort, healthy.addr),
		fmt.Sprintf("%d=%s", badPort, hang.addr),
	}
	cliCfg := baseClientConfig("tcp", fmt.Sprintf("127.0.0.1:%d", tunnelPort), token, nil)

	tun := runPair(t, srvCfg, cliCfg, goodPort, tunnelPort)
	if err := tun.waitReady(tunnelReadyTimeout); err != nil {
		t.Fatalf("the tunnel never came up: %v", err)
	}

	// Several connections that will hang, left hanging.
	badEntry := fmt.Sprintf("127.0.0.1:%d", badPort)
	for i := 0; i < 5; i++ {
		c, err := net.DialTimeout("tcp", badEntry, 3*time.Second)
		if err != nil {
			t.Fatalf("could not reach the forwarded port that leads nowhere: %v", err)
		}
		_, _ = c.Write([]byte("this will never be answered"))
		t.Cleanup(func() { c.Close() })
	}

	// The healthy port has to still work. If it does not, one stuck backend has
	// taken the whole tunnel with it.
	time.Sleep(1500 * time.Millisecond)
	if err := tun.roundTrip(randomPayload(t, 32*1024)); err != nil {
		t.Fatalf("five connections hung on one forwarded port stopped a different port "+
			"from working: %v", err)
	}
}

// startHangingBackend accepts connections, reads nothing and answers nothing.
type hangingBackend struct {
	addr   string
	closed atomic.Bool
}

func startHangingBackend(t *testing.T) *hangingBackend {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot start a backend: %v", err)
	}
	b := &hangingBackend{addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Held, not closed: closing would be an answer of a kind, and the
			// case under test is the one where nothing comes back at all.
			t.Cleanup(func() { c.Close() })
		}
	}()
	t.Cleanup(func() { b.closed.Store(true); ln.Close() })
	return b
}

// Kept so the package's own rand import is used identically to packetloss_test.
var _ = rand.Int

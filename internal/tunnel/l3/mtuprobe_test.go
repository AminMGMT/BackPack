package l3

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// A probe must be exactly as big on the wire as the data packet it stands for.
//
// This is the whole correctness of the measurement. A probe even slightly
// smaller arrives on a path where the real packet would not, and the tunnel
// concludes the path is wider than it is — which is the fault the probe exists
// to prevent, reached by a different route.
func TestAProbeIsTheSizeOfTheDataItStandsFor(t *testing.T) {
	for _, encapName := range []string{"ipip", "gre"} {
		encap, err := NewEncap(encapName, 0)
		if err != nil {
			t.Fatalf("NewEncap: %v", err)
		}
		const inner = 1371

		// What a real data message costs: the encapsulated packet, sealed.
		packet := make([]byte, inner)
		packet[0] = 0x45
		wrapped, err := encap.Wrap(nil, packet)
		if err != nil {
			t.Fatalf("Wrap: %v", err)
		}
		dataPlaintext := len(wrapped)

		// What the probe costs.
		probePlaintext := len(buildProbe(1, inner, encap.Overhead()))

		if probePlaintext != dataPlaintext {
			t.Errorf("%s: a probe for %d bytes is %d bytes of plaintext, a data packet is %d — "+
				"the measurement would be wrong by %d bytes",
				encapName, inner, probePlaintext, dataPlaintext, dataPlaintext-probePlaintext)
		}
	}
}

func TestProbeBodyRoundTrips(t *testing.T) {
	body := buildProbe(0xDEADBEEF, 1371, 4)
	id, p, ok := readProbe(body)
	if !ok || id != 0xDEADBEEF || p != 1371 {
		t.Fatalf("round trip: id=%x p=%d ok=%v", id, p, ok)
	}
	if _, _, ok := readProbe([]byte{1, 2}); ok {
		t.Error("a body too short to hold a header was accepted")
	}
}

// The end-to-end behaviour: a path that silently drops anything over a limit
// must be measured, and the interface must end up at that limit.
//
// The carrier here refuses to deliver datagrams above a ceiling, exactly as a
// real path does — no error, no ICMP, the packet simply never arrives.
func TestTheTunnelMeasuresACappedPathAndSetsTheInterface(t *testing.T) {
	const token = "a-probing-token"
	// A wire ceiling that corresponds to an inner packet well below the 1415
	// an unobstructed 1500-byte path would allow.
	const wireCeiling = 1200

	listenDev := newFakeDevice(1400)
	dialDev := newFakeDevice(1400)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	listener, err := New(Config{
		Mode: ModeListen, Addr: "127.0.0.1:0", Token: token, Encap: "gre",
		LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1400, AutoMTU: true,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(listener): %v", err)
	}
	listener.openDevice = func(deviceSpec) (packetDevice, error) {
		return listenDev, nil
	}
	start(t, ctx, cancel, listener, listenDev)

	dialer, err := New(Config{
		Mode: ModeDial, Addr: awaitBind(t, listener).String(), Token: token, Encap: "gre",
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400, AutoMTU: true,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(dialer): %v", err)
	}
	dialer.openDevice = func(deviceSpec) (packetDevice, error) {
		return dialDev, nil
	}
	// Shrunk so a full binary search finishes in a test rather than a minute.
	dialer.probe = probeTiming{
		timeout:  120 * time.Millisecond,
		attempts: 2,
		settle:   50 * time.Millisecond,
		every:    time.Hour,
	}

	// The dialler's carrier is wrapped so anything over the ceiling is
	// swallowed, which is what a too-small path does.
	var dropped atomic.Int64
	dialer.wrapCarrier = func(c DatagramCarrier) DatagramCarrier {
		return &cappedCarrier{DatagramCarrier: c, ceiling: wireCeiling, dropped: &dropped}
	}
	start(t, ctx, cancel, dialer, dialDev)

	awaitSession(t, dialer, 5*time.Second)

	// The prober waits probeSettle before its first round.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if got := dialDev.settledMTU(); got != 1400 {
			// It must have come down to something the capped path carries, and
			// not collapsed to the floor.
			if got >= 1400 {
				t.Fatalf("the interface was raised to %d on a path capped at %d", got, wireCeiling)
			}
			overhead := dialer.carrier.Overhead() + dataOverhead + dialer.encap.Overhead()
			if got+overhead > wireCeiling {
				t.Fatalf("measured %d, which needs %d on the wire — over the %d ceiling",
					got, got+overhead, wireCeiling)
			}
			if dropped.Load() == 0 {
				t.Fatal("nothing was ever dropped, so the cap was not exercised")
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the interface stayed at 1400 on a path that cannot carry it (dropped %d)", dropped.Load())
}

// cappedCarrier is a path with a size limit and no way of telling you about it.
type cappedCarrier struct {
	DatagramCarrier
	ceiling int
	dropped *atomic.Int64
}

func (c *cappedCarrier) WriteTo(p []byte, addr net.Addr) (int, error) {
	if len(p)+c.Overhead() > c.ceiling {
		// Silently. That is the whole point: a real path sends nothing back.
		c.dropped.Add(1)
		return len(p), nil
	}
	return c.DatagramCarrier.WriteTo(p, addr)
}

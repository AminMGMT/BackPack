package l3

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// The carrier has to move datagrams, in both directions, unchanged.
//
// It is the plainest thing that can be asked of it and the thing everything
// above depends on: the tunnel seals a packet, hands it here, and expects the
// same bytes out of the other end.
func TestQuicCarrierMovesDatagramsBothWays(t *testing.T) {
	listener, _, err := openQuic(Config{Mode: ModeListen, Addr: "127.0.0.1:0", Carrier: CarrierQuic})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	addr := listener.LocalAddr().String()
	type read struct {
		b   []byte
		err error
	}
	got := make(chan read, 1)
	go func() {
		buf := make([]byte, 2048)
		n, _, err := listener.ReadFrom(buf)
		got <- read{append([]byte(nil), buf[:n]...), err}
	}()

	dialer, peer, err := openQuic(Config{Mode: ModeDial, Addr: addr, Carrier: CarrierQuic})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer dialer.Close()

	up := []byte("sealed bytes, up")
	if _, err := dialer.WriteTo(up, peer); err != nil {
		t.Fatalf("write up: %v", err)
	}
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("read up: %v", r.err)
		}
		if !bytes.Equal(r.b, up) {
			t.Fatalf("up: got %q want %q", r.b, up)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("nothing arrived at the listener")
	}

	// And back, which is the direction that has to find the peer for itself.
	down := []byte("sealed bytes, down")
	if _, err := listener.WriteTo(down, nil); err != nil {
		t.Fatalf("write down: %v", err)
	}
	buf := make([]byte, 2048)
	dialer.SetReadDeadline(time.Now().Add(10 * time.Second))
	n, _, err := dialer.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read down: %v", err)
	}
	if !bytes.Equal(buf[:n], down) {
		t.Fatalf("down: got %q want %q", buf[:n], down)
	}
}

// The overhead it reports has to be at least what it really costs, or the MTU
// budget above it produces a tunnel that fragments silently.
func TestQuicOverheadIsNotAnUnderestimate(t *testing.T) {
	c, _, err := openQuic(Config{Mode: ModeListen, Addr: "127.0.0.1:0", Carrier: CarrierQuic})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer c.Close()
	// IPv4 + UDP + a short header with a connection id, a packet number, the
	// frame header and the AEAD tag. Anything below this and a full-sized
	// tunnel packet would not fit a 1500-byte path.
	if c.Overhead() < 20+8+16 {
		t.Errorf("Overhead() = %d, which does not even cover IP, UDP and the tag", c.Overhead())
	}
	if c.CarrierName() != CarrierQuic {
		t.Errorf("CarrierName() = %q", c.CarrierName())
	}
}

// The whole tunnel over the QUIC carrier: Noise handshake, GRE framing, real
// IP packets in and out, in both directions.
//
// The carrier test above proves datagrams cross. This proves the tunnel does —
// which is a different claim, because everything between the two has to agree
// about sizes, peers and when a session is established.
func TestTheTunnelCarriesPacketsOverQuic(t *testing.T) {
	const token = "a-quic-carried-token"

	dialDev, listenDev := newFakeDevice(1400), newFakeDevice(1400)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	listener, err := New(Config{
		Mode: ModeListen, Addr: "127.0.0.1:0", Token: token, Carrier: CarrierQuic,
		LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1300,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(listener): %v", err)
	}
	listener.openDevice = func(deviceSpec) (packetDevice, error) { return listenDev, nil }
	start(t, ctx, cancel, listener, listenDev)

	bound := awaitBind(t, listener)
	dialer, err := New(Config{
		Mode: ModeDial, Addr: bound.String(), Token: token, Carrier: CarrierQuic,
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1300,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(dialer): %v", err)
	}
	dialer.openDevice = func(deviceSpec) (packetDevice, error) { return dialDev, nil }
	start(t, ctx, cancel, dialer, dialDev)

	awaitSession(t, dialer, 15*time.Second)

	across(t, dialDev, listenDev, ipv4Packet(1, 2, 3, 4))
	across(t, listenDev, dialDev, ipv4Packet(9, 8, 7, 6))
	// A full-sized one, because that is where an overhead that was guessed too
	// low stops being invisible.
	big := ipv4Packet(bytes.Repeat([]byte{0xAB}, 1100)...)
	across(t, dialDev, listenDev, big)
}

// The same, over the ICMP carrier.
//
// It needs CAP_NET_RAW — the socket is a raw one — so it skips where the test
// runner does not have it rather than failing for a reason that is not the
// tunnel's. Run under `unshare --map-root-user --net` (or as root) it exercises
// the whole path: ping-shaped datagrams, Noise, GRE, real packets both ways.
func TestTheTunnelCarriesPacketsOverXdi(t *testing.T) {
	const token = "an-icmp-carried-token"

	probe, _, err := openXdi(Config{Mode: ModeListen, Addr: "127.0.0.1:0", Token: token})
	if err != nil {
		t.Skipf("no raw ICMP socket here (%v) — needs CAP_NET_RAW", err)
	}
	probe.Close()

	dialDev, listenDev := newFakeDevice(1400), newFakeDevice(1400)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	listener, err := New(Config{
		Mode: ModeListen, Addr: "127.0.0.1:0", Token: token, Carrier: CarrierXdi,
		LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1300,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(listener): %v", err)
	}
	listener.openDevice = func(deviceSpec) (packetDevice, error) { return listenDev, nil }
	start(t, ctx, cancel, listener, listenDev)

	dialer, err := New(Config{
		Mode: ModeDial, Addr: "127.0.0.1:0", Token: token, Carrier: CarrierXdi,
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1300,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(dialer): %v", err)
	}
	dialer.openDevice = func(deviceSpec) (packetDevice, error) { return dialDev, nil }
	start(t, ctx, cancel, dialer, dialDev)

	awaitSession(t, dialer, 20*time.Second)
	across(t, dialDev, listenDev, ipv4Packet(1, 2, 3, 4))
	across(t, listenDev, dialDev, ipv4Packet(9, 8, 7, 6))
}

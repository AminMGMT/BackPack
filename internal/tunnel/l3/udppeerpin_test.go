package l3

import (
	"context"
	"net"
	"runtime"
	"testing"
	"time"
)

// The udp carrier the tunnel is handed must not decide on its own where the
// tunnel's packets go.
//
// The tunnel moves its peer only after a datagram has decrypted under a live
// session (notePeer). A wrapper below it used to remember the source of every
// datagram it read — authenticated or not — and send everything there instead
// of to the address it was given. One junk datagram from anywhere, source
// forged or not, then pointed the listener's whole egress at that address.
func TestAStrayDatagramDoesNotRedirectTheListener(t *testing.T) {
	carrier, _, err := openCarrier(Config{Mode: ModeListen, Addr: "127.0.0.1:0", Carrier: CarrierUDP})
	if err != nil {
		t.Fatalf("opening the carrier: %v", err)
	}
	defer carrier.Close()

	genuine := localUDP(t)
	stray := localUDP(t)
	to := carrier.LocalAddr()

	buf := make([]byte, 64)
	if _, err := genuine.WriteTo([]byte("hello"), to); err != nil {
		t.Fatal(err)
	}
	_ = carrier.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, from, err := carrier.ReadFrom(buf)
	if err != nil {
		t.Fatalf("reading the genuine datagram: %v", err)
	}

	// Anything at all, from anywhere. The tunnel would drop it unread.
	if _, err := stray.WriteTo([]byte("not a tunnel packet"), to); err != nil {
		t.Fatal(err)
	}
	if _, _, err := carrier.ReadFrom(buf); err != nil {
		t.Fatalf("reading the stray datagram: %v", err)
	}

	// The tunnel still sends to the peer it authenticated.
	if _, err := carrier.WriteTo([]byte("reply"), from); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if !arrives(genuine, "reply") {
		t.Fatal("the reply did not reach the address the tunnel sent it to")
	}
	if arrives(stray, "reply") {
		t.Fatal("the reply went to the source of an unauthenticated datagram")
	}
}

// The batched read and write paths exist for the carrier the tunnel actually
// runs on. They were measured on the bare carrier and then hidden from the
// tunnel behind a wrapper that exposes neither.
func TestTheDefaultUDPCarrierReachesTheBatchPaths(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("recvmmsg and sendmmsg are Linux syscalls")
	}
	for _, mode := range []string{ModeListen, ModeDial} {
		addr := "127.0.0.1:0"
		if mode == ModeDial {
			peer := localUDP(t)
			addr = peer.LocalAddr().String()
		}
		carrier, _, err := openCarrier(Config{Mode: mode, Addr: addr, Carrier: CarrierUDP})
		if err != nil {
			t.Fatalf("%s: opening the carrier: %v", mode, err)
		}
		if asBatchReader(carrier) == nil {
			t.Errorf("%s: the udp carrier the tunnel gets cannot read in batches", mode)
		}
		if asBatchWriter(carrier) == nil {
			t.Errorf("%s: the udp carrier the tunnel gets cannot write in batches", mode)
		}
		carrier.Close()
	}
}

func localUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func arrives(c *net.UDPConn, want string) bool {
	buf := make([]byte, 64)
	_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	n, _, err := c.ReadFrom(buf)
	return err == nil && string(buf[:n]) == want
}

// With the single udp path no longer wrapped, the tunnel's batched send (and
// UDP segmentation offload, where the kernel has it) is the production path
// for the default carrier. Runs of full-sized packets with short ones mixed in
// — the shapes GSO splits a batch around — must all arrive intact, both ways.
func TestBatchedUDPCarriesMixedRunsIntactBothWays(t *testing.T) {
	const batch = 32
	aDev, bDev := newBatchDevice(batch), newBatchDevice(batch)
	aDev.emitted = make(chan []byte, 4096)
	bDev.emitted = make(chan []byte, 4096)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	listener, err := New(Config{
		Mode: ModeListen, Addr: "127.0.0.1:0", Token: "gso-token", Encap: "gre",
		LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1400,
	}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	listener.openDevice = func(deviceSpec) (packetDevice, error) { return bDev, nil }
	start(t, ctx, cancel, listener, bDev)

	dialer, err := New(Config{
		Mode: ModeDial, Addr: awaitBind(t, listener).String(), Token: "gso-token", Encap: "gre",
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
	}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	dialer.openDevice = func(deviceSpec) (packetDevice, error) { return aDev, nil }
	start(t, ctx, cancel, dialer, aDev)
	awaitSession(t, dialer, 5*time.Second)

	// The listener learns its peer from the first authenticated datagram.
	aDev.queue(ipv4Packet(0xEE))
	select {
	case <-bDev.emitted:
	case <-time.After(5 * time.Second):
		t.Fatal("the first packet never crossed")
	}

	build := func(i int) []byte {
		size := 1400 // mostly full-sized, so the sender can segment runs of them
		switch i % 11 {
		case 3:
			size = 60
		case 7:
			size = 900
		}
		p := ipv4Packet(byte(i>>8), byte(i))
		for len(p) < size {
			p = append(p, byte(i+len(p)))
		}
		return p[:size]
	}
	const packets = 1500
	for _, dir := range []struct {
		name     string
		from, to *batchDevice
	}{{"dial→listen", aDev, bDev}, {"listen→dial", bDev, aDev}} {
		want := map[string]int{}
		for i := 0; i < packets; i++ {
			p := build(i)
			want[string(p)]++
			dir.from.queue(p)
		}
		deadline := time.After(20 * time.Second)
		for got := 0; got < packets; got++ {
			select {
			case p := <-dir.to.emitted:
				if want[string(p)] == 0 {
					t.Fatalf("%s: a packet arrived changed or twice (%d bytes)", dir.name, len(p))
				}
				want[string(p)]--
			case <-deadline:
				t.Fatalf("%s: only %d of %d packets crossed", dir.name, got, packets)
			}
		}
	}
}

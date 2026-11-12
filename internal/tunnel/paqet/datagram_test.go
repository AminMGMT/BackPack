package paqet

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"testing"
	"time"
)

func TestTheLaneSealsAndOpensADatagramAndRefusesAnythingElse(t *testing.T) {
	key := DeriveKey("a-long-test-token")
	cli, err := newDGCodec(key, false)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := newDGCodec(key, true)
	scratch := make([]byte, frameBufLen)

	data := make([]byte, 700)
	rand.Read(data)
	pkt := cli.seal(0x01020304, data)
	if len(pkt) != len(data)+dgOverhead {
		t.Fatalf("a datagram of %d bytes became %d on the wire, want %d", len(data), len(pkt), len(data)+dgOverhead)
	}
	flow, got, ok := srv.open(pkt, scratch)
	if !ok || flow != 0x01020304 || !bytes.Equal(got, data) {
		t.Fatalf("open: ok=%v flow=%#x equal=%v", ok, flow, bytes.Equal(got, data))
	}
	// The packet is left as it came: a failed open must hand it to KCP intact.
	if !bytes.Equal(pkt, cli.seal(0x01020304, data)[:0:0]) && len(pkt) != len(data)+dgOverhead {
		t.Fatal("open changed the packet")
	}

	// Another token's packet, a flipped bit and plain noise are not the lane's.
	other, _ := newDGCodec(DeriveKey("some-other-token-entirely"), false)
	if _, _, ok := srv.open(other.seal(1, data), scratch); ok {
		t.Error("a packet sealed under another token was opened")
	}
	bad := append([]byte(nil), pkt...)
	bad[len(bad)-1] ^= 1
	if _, _, ok := srv.open(bad, scratch); ok {
		t.Error("a packet with a flipped bit was opened")
	}
	noise := make([]byte, 1350)
	passed := 0
	for i := 0; i < 20000; i++ {
		rand.Read(noise)
		if _, _, ok := srv.open(noise, scratch); ok {
			passed++
		}
	}
	if passed != 0 {
		t.Errorf("%d of 20000 random packets were taken for datagrams", passed)
	}
	// The marker is not constant: two datagrams do not begin alike.
	a, b := cli.seal(1, data), cli.seal(1, data)
	if bytes.Equal(a[:dgGCMNonce], b[:dgGCMNonce]) {
		t.Error("two packets carry the same nonce")
	}
}

func TestTheTwoEndsNeverSealUnderTheSameNonce(t *testing.T) {
	key := DeriveKey("a-long-test-token")
	cli, _ := newDGCodec(key, false)
	srv, _ := newDGCodec(key, true)
	seen := map[[dgGCMNonce]byte]bool{}
	for i := 0; i < 5000; i++ {
		for _, c := range []*dgCodec{cli, srv} {
			var n [dgGCMNonce]byte
			copy(n[:], c.seal(1, []byte("x"))[:dgGCMNonce])
			if seen[n] {
				t.Fatalf("nonce %x used twice", n)
			}
			seen[n] = true
		}
	}
}

func laneCounters(p *pair) (cliOut, cliIn, srvOut, srvIn uint64) {
	return p.client.dgOut.Load(), p.client.dgIn.Load(), p.server.dgOut.Load(), p.server.dgIn.Load()
}

func TestSmallDatagramsTravelTheLaneAndLargeOnesTheStream(t *testing.T) {
	p := newPair(t, nil)
	p.start(t)
	p.waitUp(t)
	echo := udpEcho(t)

	conn, err := p.client.Dial(context.Background(), "udp", echo.LocalAddr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	buf := make([]byte, 65535)
	roundTrip := func(n int) {
		t.Helper()
		d := make([]byte, n)
		rand.Read(d)
		if _, err := conn.Write(d); err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		got, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("datagram of %d: %v", n, err)
		}
		if !bytes.Equal(buf[:got], d) {
			t.Fatalf("a datagram of %d bytes came back as %d different bytes", n, got)
		}
	}
	// The first goes before the server's verdict is in, so by the stream.
	roundTrip(100)
	const small = 50
	for i := 0; i < small; i++ {
		roundTrip(300)
	}
	cOut, cIn, sOut, sIn := laneCounters(p)
	if cOut < small || sIn < small || sOut < small || cIn < small {
		t.Fatalf("%d small datagrams each way, but the lane carried client out %d in %d, server out %d in %d", small, cOut, cIn, sOut, sIn)
	}
	// One too large for a segment goes by the stream, as before.
	roundTrip(9000)
	if o, _, _, _ := laneCounters(p); o != cOut {
		t.Fatalf("a 9000-byte datagram went on the lane (%d sent there before, %d after)", cOut, o)
	}
}

// A datagram sent while the path is down is lost, as UDP's is. It was once
// queued in the stream, retransmitted and delivered seconds late, which is what
// the soak harness measured (11.7 s at the median).
func TestADatagramSentWhileThePathIsDownIsLostNotDeliveredLate(t *testing.T) {
	p := newPair(t, nil)
	p.start(t)
	p.waitUp(t)
	echo := udpEcho(t)

	conn, err := p.client.Dial(context.Background(), "udp", echo.LocalAddr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	buf := make([]byte, 2048)
	exchange := func(tag uint32) {
		t.Helper()
		d := make([]byte, 200)
		binary.BigEndian.PutUint32(d, tag)
		conn.Write(d)
	}
	// Past the verdict, so the lane is in use.
	exchange(0)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Read(buf); err != nil {
		t.Fatal(err)
	}

	p.cEnd.setLoss(1)
	for i := uint32(1); i <= 20; i++ {
		exchange(i)
	}
	time.Sleep(300 * time.Millisecond)
	p.cEnd.setLoss(0)
	exchange(100)
	for {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("the datagram sent after the path came back never arrived: %v", err)
		}
		if n < 4 {
			continue
		}
		tag := binary.BigEndian.Uint32(buf)
		if tag == 100 {
			return
		}
		t.Fatalf("a datagram sent while the path was down (%d) arrived after it was back", tag)
	}
}

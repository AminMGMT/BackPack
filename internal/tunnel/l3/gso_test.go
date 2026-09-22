package l3

import (
	"bytes"
	"net"
	"runtime"
	"testing"
	"time"
)

// The rules, without a socket.
//
// Getting these wrong does not produce an error. It produces a kernel cutting
// somebody's packets in the wrong places, which arrives at the far end as
// corruption and nowhere as a message — so they are held here, exactly, rather
// than inferred from whether a write happened to succeed.
func TestWhereARunEnds(t *testing.T) {
	buf := func(n int) []byte { return make([]byte, n) }

	for _, tc := range []struct {
		name    string
		bufs    [][]byte
		n       int
		segment int
		ok      bool
	}{
		{"a uniform batch is one run", [][]byte{buf(1200), buf(1200), buf(1200)}, 3, 1200, true},
		{"two is a run", [][]byte{buf(1200), buf(1200)}, 2, 1200, true},

		// A short packet does not merely end a run: it is that run's last
		// segment, because the kernel's remainder is the final datagram. This
		// is what makes the path useful on traffic that is not uniform.
		{"a short one closes the run it is in", [][]byte{buf(1200), buf(1200), buf(400)}, 3, 1200, true},
		{"and the rest is left for the next run", [][]byte{buf(1200), buf(400), buf(1200), buf(1200)}, 2, 1200, true},

		{"one is just a write", [][]byte{buf(1200)}, 0, 0, false},
		{"nothing at all", nil, 0, 0, false},
		{"a longer one cannot be a segment", [][]byte{buf(1200), buf(1201)}, 0, 0, false},
		{"an empty one ends it", [][]byte{buf(1200), buf(0)}, 0, 0, false},
		{"an empty first one", [][]byte{buf(0), buf(0)}, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, segment, ok := gsoRun(tc.bufs)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if n != tc.n {
				t.Fatalf("run of %d, want %d", n, tc.n)
			}
			if segment != tc.segment {
				t.Fatalf("segment = %d, want %d", segment, tc.segment)
			}
		})
	}
}

// The two ceilings end a run rather than refusing the batch, so a long batch
// goes out as several segmented writes instead of falling back wholesale.
func TestTheCeilingsEndARunRatherThanRefusingIt(t *testing.T) {
	// More datagrams than the kernel segments in one go.
	many := make([][]byte, gsoMaxSegments+10)
	for i := range many {
		many[i] = make([]byte, 100)
	}
	n, _, ok := gsoRun(many)
	if !ok || n != gsoMaxSegments {
		t.Fatalf("run of %d (ok=%v), want %d", n, ok, gsoMaxSegments)
	}

	// More bytes than one UDP payload describes.
	big := make([][]byte, 64)
	for i := range big {
		big[i] = make([]byte, 1200)
	}
	n, _, ok = gsoRun(big)
	if !ok {
		t.Fatal("a long run of full-sized packets was refused outright")
	}
	if n*1200 > gsoMaxPayload {
		t.Fatalf("a run of %d × 1200 is %d bytes, over the %d a UDP payload carries",
			n, n*1200, gsoMaxPayload)
	}
	if n < 50 {
		t.Fatalf("run of %d, and %d would fit", n, gsoMaxPayload/1200)
	}
}

// A run that does not fit in one UDP payload is refused before it is attempted,
// because the kernel refuses it outright and a refusal turns the feature off
// for the life of the socket.
func TestARunTooBigForOnePayloadIsRefusedFirst(t *testing.T) {
	fits := make([][]byte, 54)
	for i := range fits {
		fits[i] = make([]byte, 1200)
	}
	n, _, ok := gsoRun(fits)
	if !ok {
		t.Fatal("a run that fits in one UDP payload was refused")
	}
	if n*1200 > gsoMaxPayload {
		t.Fatalf("the run is %d bytes, over the %d limit", n*1200, gsoMaxPayload)
	}
}

// The whole point, end to end: the kernel has to deliver the run as separate
// datagrams, each one byte for byte what was handed over.
//
// This is the assertion that a misdeclared segment size cannot pass. Sending
// succeeds either way; only reading it back at the far end says whether it was
// cut in the right places.
func TestASegmentedWriteArrivesAsSeparateDatagrams(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("UDP_SEGMENT is a Linux socket option")
	}

	sink, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding a sink: %v", err)
	}
	defer sink.Close()

	c := newLocalUDPCarrier(t)

	const (
		segment = 1200
		count   = 8
	)
	bufs := make([][]byte, count)
	for i := range bufs {
		b := make([]byte, segment)
		for j := range b {
			b[j] = byte(i)
		}
		bufs[i] = b
	}

	n, err := c.writeGSO(bufs, sink.LocalAddr())
	if err != nil {
		t.Skipf("this kernel will not segment a write (%v); the carrier falls back to sendmmsg", err)
	}
	if n != count {
		t.Fatalf("writeGSO reported %d datagrams, want %d", n, count)
	}

	if err := sink.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	got := make([]byte, 65536)
	for i := 0; i < count; i++ {
		read, _, err := sink.ReadFrom(got)
		if err != nil {
			t.Fatalf("datagram %d never arrived: %v — the run was not cut into %d", i, err, count)
		}
		if read != segment {
			t.Fatalf("datagram %d is %d bytes, want %d — the segment size was declared wrong",
				i, read, segment)
		}
		if !bytes.Equal(got[:read], bufs[i]) {
			t.Fatalf("datagram %d came back with the wrong contents", i)
		}
	}
}

// A ragged batch takes the old path and still arrives. This is the ordinary
// case for interactive traffic and it must be untouched by any of the above.
func TestARaggedBatchStillGoesOutInFull(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the batch path is a Linux syscall")
	}

	sink, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding a sink: %v", err)
	}
	defer sink.Close()

	c := newLocalUDPCarrier(t)

	sizes := []int{1200, 64, 900, 40}
	bufs := make([][]byte, len(sizes))
	for i, n := range sizes {
		b := make([]byte, n)
		for j := range b {
			b[j] = byte(i + 1)
		}
		bufs[i] = b
	}

	sent, err := c.WriteBatch(bufs, sink.LocalAddr())
	if err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	if sent != len(bufs) {
		t.Fatalf("WriteBatch sent %d of %d", sent, len(bufs))
	}

	if err := sink.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	got := make([]byte, 65536)
	for i := range bufs {
		read, _, err := sink.ReadFrom(got)
		if err != nil {
			t.Fatalf("datagram %d never arrived: %v", i, err)
		}
		if !bytes.Equal(got[:read], bufs[i]) {
			t.Fatalf("datagram %d came back wrong: %d bytes, want %d", i, read, len(bufs[i]))
		}
	}
}

// A uniform batch through the public path arrives whole, whichever of the two
// ways it went. The carrier chooses; the caller must not be able to tell.
func TestAUniformBatchArrivesWhicheverPathItTook(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the batch path is a Linux syscall")
	}

	sink, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding a sink: %v", err)
	}
	defer sink.Close()

	c := newLocalUDPCarrier(t)

	const segment = 1000
	bufs := make([][]byte, 6)
	for i := range bufs {
		b := make([]byte, segment)
		for j := range b {
			b[j] = byte(0xA0 + i)
		}
		bufs[i] = b
	}

	sent, err := c.WriteBatch(bufs, sink.LocalAddr())
	if err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	if sent != len(bufs) {
		t.Fatalf("WriteBatch sent %d of %d", sent, len(bufs))
	}

	if err := sink.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	got := make([]byte, 65536)
	for i := range bufs {
		read, _, err := sink.ReadFrom(got)
		if err != nil {
			t.Fatalf("datagram %d never arrived: %v", i, err)
		}
		if read != segment || !bytes.Equal(got[:read], bufs[i]) {
			t.Fatalf("datagram %d came back wrong", i)
		}
	}
}

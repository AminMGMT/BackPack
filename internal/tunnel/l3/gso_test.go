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
func TestOnlyAUniformRunIsSegmented(t *testing.T) {
	buf := func(n int) []byte { return make([]byte, n) }

	for _, tc := range []struct {
		name    string
		bufs    [][]byte
		segment int
		ok      bool
	}{
		{"a uniform run", [][]byte{buf(1200), buf(1200), buf(1200)}, 1200, true},
		{"a short last one is allowed", [][]byte{buf(1200), buf(1200), buf(400)}, 1200, true},
		{"two is a run", [][]byte{buf(1200), buf(1200)}, 1200, true},

		{"one is just a write", [][]byte{buf(1200)}, 0, false},
		{"nothing at all", nil, 0, false},
		{"a short one in the middle", [][]byte{buf(1200), buf(400), buf(1200)}, 0, false},
		{"a long last one", [][]byte{buf(1200), buf(1201)}, 0, false},
		{"an empty last one", [][]byte{buf(1200), buf(0)}, 0, false},
		{"an empty first one", [][]byte{buf(0), buf(0)}, 0, false},
		{"more than the kernel takes", make([][]byte, gsoMaxSegments+1), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			segment, ok := gsoEligible(tc.bufs)
			if ok != tc.ok {
				t.Fatalf("eligible = %v, want %v", ok, tc.ok)
			}
			if ok && segment != tc.segment {
				t.Fatalf("segment = %d, want %d", segment, tc.segment)
			}
		})
	}
}

// A run that does not fit in one UDP payload is refused before it is attempted,
// because the kernel refuses it outright and a refusal turns the feature off
// for the life of the socket.
func TestARunTooBigForOnePayloadIsRefusedFirst(t *testing.T) {
	// 64 × 1200 is 76,800 — over the 65,507 a UDP payload can describe.
	bufs := make([][]byte, 64)
	for i := range bufs {
		bufs[i] = make([]byte, 1200)
	}
	if _, ok := gsoEligible(bufs); ok {
		t.Fatal("a run larger than one UDP payload was accepted")
	}

	// And the largest run that does fit is accepted.
	fits := make([][]byte, 54)
	for i := range fits {
		fits[i] = make([]byte, 1200)
	}
	if _, ok := gsoEligible(fits); !ok {
		t.Fatalf("a %d-byte run was refused and it fits", 54*1200)
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

	if _, ok := gsoEligible(bufs); ok {
		t.Fatal("a ragged batch was judged eligible for segmenting")
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

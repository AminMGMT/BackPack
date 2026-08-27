package l3

import (
	"context"
	"encoding/binary"
	"io"
	"sync"
	"testing"
	"time"
)

// A device that writes segments the way the real one does.
//
// batchDevice next door copies into the buffer it is given, so it truncates
// quietly when the buffer is short and proves nothing about how big the buffers
// have to be. The library behind the real device does not copy: it slices the
// buffer to the length of the segment and writes into that, with no check that
// it fits. A buffer one byte short is therefore not a short read — it is a
// write past the end of a slice, and it takes the process down.
//
// So this fake slices exactly as the library does, which is what makes it able
// to reproduce the crash.
type gsoDevice struct {
	mu      sync.Mutex
	pending [][]byte

	// The shortest buffer the pump has offered, so a test can assert on the
	// contract rather than only on the crash.
	minBuf int

	emitted chan []byte
	closed  chan struct{}
	once    sync.Once
}

func newGSODevice() *gsoDevice {
	return &gsoDevice{
		minBuf:  -1,
		emitted: make(chan []byte, 64),
		closed:  make(chan struct{}),
	}
}

func (d *gsoDevice) queue(pkts ...[]byte) {
	d.mu.Lock()
	d.pending = append(d.pending, pkts...)
	d.mu.Unlock()
}

func (d *gsoDevice) Read(bufs [][]byte, sizes []int) (int, error) {
	for {
		d.mu.Lock()
		if d.minBuf < 0 {
			for _, b := range bufs {
				if d.minBuf < 0 || len(b) < d.minBuf {
					d.minBuf = len(b)
				}
			}
		}
		n := 0
		for n < len(bufs) && n < len(d.pending) {
			seg := d.pending[n]
			// The library's write, verbatim in shape: slice to the segment
			// length and fill it. Panics when the buffer is shorter.
			out := bufs[n][:len(seg)]
			copy(out, seg)
			sizes[n] = len(seg)
			n++
		}
		if n > 0 {
			d.pending = d.pending[n:]
			d.mu.Unlock()
			return n, nil
		}
		d.mu.Unlock()

		select {
		case <-d.closed:
			return 0, io.EOF
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (d *gsoDevice) Write(bufs [][]byte) (int, error) {
	for _, p := range bufs {
		select {
		case d.emitted <- append([]byte(nil), p...):
		case <-d.closed:
			return 0, io.EOF
		}
	}
	return len(bufs), nil
}

func (d *gsoDevice) BatchSize() int   { return 8 }
func (d *gsoDevice) Name() string     { return "gso0" }
func (d *gsoDevice) MTU() int         { return 1400 }
func (d *gsoDevice) SetMTU(int) error { return nil }
func (d *gsoDevice) Close() error {
	d.once.Do(func() { close(d.closed) })
	return nil
}

func (d *gsoDevice) smallestBuffer() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.minBuf
}

// The pump must offer buffers that fit any segment, not buffers that fit the
// MTU.
//
// This is the crash reported from the field, as an assertion on the contract
// rather than on the fault it causes. The buffers used to be MTU-sized, on the
// assumption that a read returns packets built for this interface. It does not:
// with segmentation offload on it returns whatever the sender's path produced,
// and the split writes each one in whole.
func TestPumpOffersBuffersBigEnoughForAnySegment(t *testing.T) {
	dev := newGSODevice()
	tunnel, err := New(Config{
		Mode: ModeListen, Addr: "127.0.0.1:0", Token: "t", Encap: "gre",
		LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1400,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tunnel.openDevice = func(deviceSpec) (packetDevice, error) { return dev, nil }

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	start(t, ctx, cancel, tunnel, dev)

	deadline := time.After(5 * time.Second)
	for dev.smallestBuffer() < 0 {
		select {
		case <-deadline:
			t.Fatal("the pump never read from the device")
		case <-time.After(5 * time.Millisecond):
		}
	}

	if got := dev.smallestBuffer(); got < tunReadBuf {
		t.Fatalf("the pump offered a %d-byte buffer, want at least %d.\n"+
			"A segment longer than the buffer is written past its end, which is a "+
			"memory fault and not a short read — see tunReadBuf.", got, tunReadBuf)
	}
}

// And end to end: a segment larger than the tunnel's MTU crosses intact.
//
// The buffer assertion above says the contract is met; this says the fault it
// was hiding is gone. Without the fix the pump does not return an error here —
// it dies inside the device read and takes the process with it.
func TestASegmentLargerThanTheMTUCrossesIntact(t *testing.T) {
	sendDev := newGSODevice()
	recvDev := newGSODevice()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	listener, err := New(Config{
		Mode: ModeListen, Addr: "127.0.0.1:0", Token: "gso-token", Encap: "gre",
		LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1400,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(listener): %v", err)
	}
	listener.openDevice = func(deviceSpec) (packetDevice, error) { return recvDev, nil }
	start(t, ctx, cancel, listener, recvDev)

	dialer, err := New(Config{
		Mode: ModeDial, Addr: awaitBind(t, listener).String(), Token: "gso-token", Encap: "gre",
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(dialer): %v", err)
	}
	dialer.openDevice = func(deviceSpec) (packetDevice, error) { return sendDev, nil }
	start(t, ctx, cancel, dialer, sendDev)

	awaitSession(t, dialer, 5*time.Second)

	// 1500 is the everyday case: a sender on an ordinary 1500-byte path,
	// forwarded into a tunnel whose MTU is 1400.
	big := ipv4Packet(9, 1, 2, 3)
	for len(big) < 1500 {
		big = append(big, byte(len(big)))
	}
	binary.BigEndian.PutUint16(big[2:4], uint16(len(big)))
	sendDev.queue(big)

	select {
	case got := <-recvDev.emitted:
		if len(got) != len(big) {
			t.Fatalf("a %d-byte segment arrived as %d bytes", len(big), len(got))
		}
		for i := range big {
			if got[i] != big[i] {
				t.Fatalf("the segment was corrupted at byte %d", i)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a segment larger than the MTU never crossed")
	}
}

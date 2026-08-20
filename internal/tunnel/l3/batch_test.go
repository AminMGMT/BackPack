package l3

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// A device that hands over several packets per read, as the real one does when
// the kernel's segmentation offload is on.
//
// The ordinary fake returns one packet per Read, which is enough for every
// other test and proves nothing about batching: a pump that quietly dropped
// everything after the first packet of a batch would pass all of them.
type batchDevice struct {
	mu      sync.Mutex
	pending [][]byte
	emitted chan []byte
	closed  chan struct{}
	once    sync.Once
	batch   int
	reads   int
}

func newBatchDevice(batch int) *batchDevice {
	return &batchDevice{
		emitted: make(chan []byte, 256),
		closed:  make(chan struct{}),
		batch:   batch,
	}
}

func (d *batchDevice) queue(pkts ...[]byte) {
	d.mu.Lock()
	d.pending = append(d.pending, pkts...)
	d.mu.Unlock()
}

func (d *batchDevice) Read(bufs [][]byte, sizes []int) (int, error) {
	for {
		d.mu.Lock()
		n := 0
		for n < len(bufs) && n < len(d.pending) {
			sizes[n] = copy(bufs[n], d.pending[n])
			n++
		}
		if n > 0 {
			d.pending = d.pending[n:]
			d.reads++
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

func (d *batchDevice) Write(bufs [][]byte) (int, error) {
	for _, p := range bufs {
		select {
		case d.emitted <- append([]byte(nil), p...):
		case <-d.closed:
			return 0, io.EOF
		}
	}
	return len(bufs), nil
}

func (d *batchDevice) BatchSize() int   { return d.batch }
func (d *batchDevice) Name() string     { return "batch0" }
func (d *batchDevice) MTU() int         { return 1400 }
func (d *batchDevice) SetMTU(int) error { return nil }
func (d *batchDevice) Close() error {
	d.once.Do(func() { close(d.closed) })
	return nil
}

func (d *batchDevice) readCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reads
}

// Every packet of a batch must cross, not just the first.
//
// The pump used to read one packet per call, so turning it into a batch loop is
// exactly the change that can drop the tail of a batch and look fine: a tunnel
// carrying one packet in thirty-two still pings, still handshakes, and still
// reports healthy.
func TestPumpSendsEveryPacketOfABatch(t *testing.T) {
	const batch = 16
	sendDev := newBatchDevice(batch)
	recvDev := newBatchDevice(batch)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	listener, err := New(Config{
		Mode: ModeListen, Addr: "127.0.0.1:0", Token: "batch-token", Encap: "gre",
		LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1400,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(listener): %v", err)
	}
	listener.openDevice = func(deviceSpec) (packetDevice, error) { return recvDev, nil }
	start(t, ctx, cancel, listener, recvDev)

	dialer, err := New(Config{
		Mode: ModeDial, Addr: awaitBind(t, listener).String(), Token: "batch-token", Encap: "gre",
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(dialer): %v", err)
	}
	dialer.openDevice = func(deviceSpec) (packetDevice, error) { return sendDev, nil }
	start(t, ctx, cancel, dialer, sendDev)

	awaitSession(t, dialer, 5*time.Second)

	// One batch, queued at once, so the pump sees them in a single read.
	const packets = batch
	for i := 0; i < packets; i++ {
		sendDev.queue(ipv4Packet(byte(i), 1, 2, 3))
	}

	got := 0
	deadline := time.After(10 * time.Second)
	for got < packets {
		select {
		case <-recvDev.emitted:
			got++
		case <-deadline:
			t.Fatalf("only %d of %d packets crossed — the pump is dropping the tail of a batch", got, packets)
		}
	}

	// And it really was a batch, not sixteen separate reads.
	if r := sendDev.readCount(); r >= packets {
		t.Errorf("took %d reads for %d packets — the batch is not being used", r, packets)
	}
}

// A device that reports a nonsense batch size must not panic the pump.
func TestPumpToleratesAZeroBatchSize(t *testing.T) {
	dev := newBatchDevice(0)
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

	// Getting this far without a panic is the assertion.
	time.Sleep(100 * time.Millisecond)
	_ = logrus.StandardLogger()
}

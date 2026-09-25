package l3

import (
	"bytes"
	"os"
	"testing"

	wgtun "golang.zx2c4.com/wireguard/tun"
)

// growingDevice stands in for the TUN device with segmentation offload on: it
// coalesces by appending to the first packet's buffer in place, as the
// wireguard library's GRO does when the capacity is there, and then reports
// what it was handed.
type growingDevice struct {
	wgtun.Device
	seen [][]byte
}

func (d *growingDevice) Write(bufs [][]byte, offset int) (int, error) {
	for _, b := range bufs {
		d.seen = append(d.seen, append([]byte(nil), b[offset:]...))
	}
	if len(bufs) > 1 && cap(bufs[0]) >= len(bufs[0])+len(bufs[1]) {
		// Grow the first packet by the second's length, in place.
		bufs[0] = append(bufs[0], bytes.Repeat([]byte{0xEE}, len(bufs[1]))...)
	}
	return len(bufs), nil
}

func (d *growingDevice) BatchSize() int    { return 128 }
func (d *growingDevice) File() *os.File    { return nil }
func (d *growingDevice) Close() error      { return nil }
func (d *growingDevice) MTU() (int, error) { return 1500, nil }

// A batch written to the device must reach it intact even though the device
// grows packets in place. Staged back to back, the first packet's growth ran
// over the second.
func TestTheStagedBatchSurvivesInPlaceCoalescing(t *testing.T) {
	dev := &growingDevice{}
	td := &tunDevice{dev: dev, wbuf: make([]byte, tunStageSize), wbufs: make([][]byte, 0, batchSize)}

	a := bytes.Repeat([]byte{0xAA}, 1400)
	b := bytes.Repeat([]byte{0xBB}, 1400)
	c := bytes.Repeat([]byte{0xCC}, 1400)
	if n, err := td.Write([][]byte{a, b, c}); err != nil || n != 3 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	// What the device saw first is what was staged; now check the staging
	// itself was not trampled by the in-place growth.
	for i, want := range [][]byte{a, b, c} {
		got := td.wbufs[i][virtioOffset:]
		if i > 0 && !bytes.Equal(got[:len(want)], want) {
			t.Fatalf("packet %d was overwritten by packet 0 growing in place", i)
		}
	}
	// And every packet has room to be coalesced into.
	for i, w := range td.wbufs {
		if cap(w) < 1<<16 {
			t.Fatalf("packet %d has only %d bytes of capacity", i, cap(w))
		}
	}
}

// More packets than the stage holds are written in part, and the count says so.
func TestTheStageReportsAShortWrite(t *testing.T) {
	dev := &growingDevice{}
	td := &tunDevice{dev: dev, wbuf: make([]byte, tunStageSize), wbufs: make([][]byte, 0, batchSize)}
	bufs := make([][]byte, batchSize+5)
	for i := range bufs {
		bufs[i] = []byte{byte(i)}
	}
	if n, _ := td.Write(bufs); n != batchSize {
		t.Fatalf("wrote %d of %d, want the %d the stage holds", n, len(bufs), batchSize)
	}
}

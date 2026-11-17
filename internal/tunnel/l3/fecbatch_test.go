package l3

import (
	"bytes"
	"fmt"
	"net"
	"testing"
)

// batchPipe is a lossyPipe with the raw carriers' batch read and write, so
// FEC over pck or xdi is exercised as the tunnel uses it.
type batchPipe struct {
	*lossyPipe
	writes, reads int // batch calls made
	noBatch       bool
}

func (p *batchPipe) WriteBatch(bufs [][]byte, to net.Addr) (int, error) {
	if p.noBatch {
		return 0, errNoBatch
	}
	p.writes++
	for _, b := range bufs {
		_, _ = p.WriteTo(b, to)
	}
	return len(bufs), nil
}

func (p *batchPipe) ReadBatch(bufs [][]byte, sizes []int, froms []net.Addr) (int, error) {
	if p.noBatch {
		return 0, errNoBatch
	}
	p.reads++
	n := 0
	for n < len(bufs) {
		k, from, err := p.ReadFrom(bufs[n])
		if err != nil {
			break
		}
		sizes[n], froms[n] = k, from
		n++
	}
	if n == 0 {
		return 0, fmt.Errorf("empty")
	}
	return n, nil
}

func newBatchFECPair(t *testing.T, cfg FECConfig, drop func(int) bool) (tx, rx *fecBatchCarrier, send, recv *batchPipe) {
	t.Helper()
	a, b := newLossyPair(nil)
	a.drop = drop
	send, recv = &batchPipe{lossyPipe: a}, &batchPipe{lossyPipe: b}
	txc, err := newFECCarrier(send, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rxc, err := newFECCarrier(recv, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var ok bool
	if tx, ok = txc.(*fecBatchCarrier); !ok {
		t.Fatalf("FEC over a batch carrier is %T, not batched", txc)
	}
	if rx, ok = rxc.(*fecBatchCarrier); !ok {
		t.Fatalf("FEC over a batch carrier is %T, not batched", rxc)
	}
	return tx, rx, send, recv
}

func readAllBatches(rx *fecBatchCarrier, width int) [][]byte {
	bufs := make([][]byte, width)
	for i := range bufs {
		bufs[i] = make([]byte, 2048)
	}
	sizes, froms := make([]int, width), make([]net.Addr, width)
	var got [][]byte
	for {
		n, err := rx.ReadBatch(bufs, sizes, froms)
		if err != nil {
			return got
		}
		for i := 0; i < n; i++ {
			got = append(got, append([]byte(nil), bufs[i][:sizes[i]]...))
		}
	}
}

// A batch goes out in one write with its groups' parity among it, and comes
// back in batch reads, the dropped packets rebuilt and each one right where
// it was lost rather than behind the rest of the batch.
func TestFECOverABatchCarrierRebuildsInPlace(t *testing.T) {
	// 4+2: the wire order is d0 d1 d2 d3 p p | d0 ... Drop d1 of the first
	// group and d2 of the second.
	tx, rx, send, recv := newBatchFECPair(t, FECConfig{Data: 4, Parity: 2},
		func(n int) bool { return n == 1 || n == 8 })
	var want [][]byte
	for i := 0; i < 10; i++ {
		want = append(want, []byte(fmt.Sprintf("packet-%02d-%s", i, bytes.Repeat([]byte("y"), i*3))))
	}
	if n, err := tx.WriteBatch(want, recv.addr); err != nil || n != len(want) {
		t.Fatalf("WriteBatch = %d, %v", n, err)
	}
	if send.writes != 1 {
		t.Errorf("%d batch writes for one batch", send.writes)
	}
	got := readAllBatches(rx, 32)
	// The last group is incomplete (8, 9), so it has no parity yet; both
	// arrived anyway. Rebuilt ones sit right after the parity that rebuilt
	// them: d0 d2 d3 [d1] d4 d5 d7 [d6] d8 d9.
	order := []int{0, 2, 3, 1, 4, 5, 7, 6, 8, 9}
	if len(got) != len(order) {
		t.Fatalf("delivered %d packets, want %d", len(got), len(order))
	}
	for i, w := range order {
		if !bytes.Equal(got[i], want[w]) {
			t.Errorf("slot %d is %q, want %q", i, got[i], want[w])
		}
	}
	if recv.reads == 0 {
		t.Error("the receiver never read in batches")
	}
}

// A batch read narrower than what arrived keeps the rest for the next read,
// in order, losing nothing.
func TestFECBatchReadKeepsWhatDoesNotFit(t *testing.T) {
	tx, rx, _, recv := newBatchFECPair(t, FECConfig{Data: 4, Parity: 1},
		func(n int) bool { return n == 0 })
	var want [][]byte
	for i := 0; i < 8; i++ {
		want = append(want, []byte(fmt.Sprintf("p%d", i)))
	}
	if _, err := tx.WriteBatch(want, recv.addr); err != nil {
		t.Fatal(err)
	}
	got := readAllBatches(rx, 2)
	if len(got) != len(want) {
		t.Fatalf("delivered %d of %d", len(got), len(want))
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[string(g)] = true
	}
	for _, w := range want {
		if !seen[string(w)] {
			t.Errorf("%q never arrived", w)
		}
	}
}

// When the carrier turns out not to batch after all, the frames already
// filed in their groups go out one at a time, each exactly once.
func TestFECBatchWriteFallsBackWithoutFilingTwice(t *testing.T) {
	tx, rx, send, recv := newBatchFECPair(t, FECConfig{Data: 4, Parity: 2}, nil)
	send.noBatch = true
	var want [][]byte
	for i := 0; i < 4; i++ {
		want = append(want, []byte(fmt.Sprintf("q%d", i)))
	}
	if n, err := tx.WriteBatch(want, recv.addr); err != nil || n != len(want) {
		t.Fatalf("WriteBatch = %d, %v", n, err)
	}
	if send.sent != 6 {
		t.Errorf("%d frames on the wire, want 4 data and 2 parity", send.sent)
	}
	got := drain(t, rx.fecCarrier)
	if len(got) != len(want) {
		t.Fatalf("delivered %d, want %d", len(got), len(want))
	}
}

// Groups are reused once evicted: a long run on a clean path keeps working
// and does not allocate a shard copy per packet.
func TestFECReusesEvictedGroups(t *testing.T) {
	tx, rx, _, recv := newBatchFECPair(t, FECConfig{Data: 4, Parity: 2},
		func(n int) bool { return n%13 == 5 })
	bufs := make([][]byte, 16)
	for round := 0; round < 50; round++ {
		for i := range bufs {
			bufs[i] = []byte(fmt.Sprintf("r%03d-%02d-%s", round, i, bytes.Repeat([]byte("z"), 100+i)))
		}
		if _, err := tx.WriteBatch(bufs, recv.addr); err != nil {
			t.Fatal(err)
		}
		got := readAllBatches(rx, 32)
		for _, g := range got {
			if !bytes.HasPrefix(g, []byte(fmt.Sprintf("r%03d-", round))) {
				t.Fatalf("round %d delivered %q", round, g)
			}
		}
	}
	if len(rx.spare) == 0 {
		t.Error("no evicted group was kept for reuse")
	}
}

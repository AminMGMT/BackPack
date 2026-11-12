package transport

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func frameOf(payload []byte) []byte {
	f := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(f, uint16(len(payload)))
	copy(f[2:], payload)
	return f
}

// Everything queued while the writer was busy comes out as one piece, in
// order, and never beyond the limit except for a first frame that is itself
// larger.
func TestDatagramBatchTakesWhatQueued(t *testing.T) {
	b := newDatagramBatch(100)
	var want []byte
	for i := 0; i < 10; i++ {
		p := bytes.Repeat([]byte{byte(i)}, 50)
		b.put(p)
		want = append(want, frameOf(p)...)
	}
	got, ok := b.take(nil, 4096)
	if !ok || !bytes.Equal(got, want) {
		t.Fatalf("take returned %d bytes ok=%v, want %d", len(got), ok, len(want))
	}

	for i := 0; i < 10; i++ {
		b.put(bytes.Repeat([]byte{byte(i)}, 50)) // 52 bytes a frame
	}
	got, _ = b.take(nil, 160)
	if len(got) != 3*52 {
		t.Fatalf("a 160-byte limit took %d bytes, want 3 whole frames", len(got))
	}
	got, _ = b.take(got, 10) // a frame larger than the limit still goes
	if len(got) != 52 {
		t.Fatalf("an oversize first frame took %d bytes, want 52", len(got))
	}
}

// A full queue drops the oldest datagram, not the newest.
func TestDatagramBatchDropsOldest(t *testing.T) {
	b := newDatagramBatch(3)
	for i := 1; i <= 5; i++ {
		b.put([]byte{byte(i)})
	}
	got, _ := b.take(nil, 100)
	want := append(append(frameOf([]byte{3}), frameOf([]byte{4})...), frameOf([]byte{5})...)
	if !bytes.Equal(got, want) {
		t.Fatalf("kept %v, want the newest three", got)
	}
	if b.dropped != 2 {
		t.Fatalf("dropped %d, want 2", b.dropped)
	}
}

// take blocks while the queue is empty, wakes for a put, and ends on close.
func TestDatagramBatchBlocksAndCloses(t *testing.T) {
	b := newDatagramBatch(4)
	type res struct {
		n  int
		ok bool
	}
	out := make(chan res, 2)
	go func() {
		got, ok := b.take(nil, 100)
		out <- res{len(got), ok}
		got, ok = b.take(nil, 100)
		out <- res{len(got), ok}
	}()
	select {
	case <-out:
		t.Fatal("take returned on an empty queue")
	case <-time.After(100 * time.Millisecond):
	}
	b.put([]byte("hi"))
	if r := <-out; !r.ok || r.n != 4 {
		t.Fatalf("take after put: %+v", r)
	}
	time.Sleep(50 * time.Millisecond)
	b.close()
	select {
	case r := <-out:
		if r.ok {
			t.Fatal("take succeeded after close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not wake take")
	}
	b.put([]byte("late")) // after close: ignored, no panic
}

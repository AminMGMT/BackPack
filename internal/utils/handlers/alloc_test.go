package handlers

import "testing"

// The relay buffer pool must hand back a buffer without allocating.
//
// That is the entire reason it exists. Before it, every forwarded connection
// allocated two 16 KB buffers — nothing for a long download, and thirty times
// two garbage-collected allocations for a browser opening thirty short
// connections to load one page, on a machine where the collector is competing
// with the thing the user is waiting for.
//
// It is also a property that is easy to lose by accident. The pool stores
// *[]byte rather than []byte specifically because putting a slice into a
// sync.Pool converts it to an interface, and that conversion allocates — which
// would put back a good part of what pooling is here to save. Somebody
// simplifying the types back to []byte would undo it silently, and nothing
// would say so. This does.
func TestTheRelayBufferPoolDoesNotAllocate(t *testing.T) {
	// Prime it, so the first Get is not the one being measured — a cold pool
	// allocates once by construction and that is not the question.
	putRelayBuffer(getRelayBuffer())

	got := testing.AllocsPerRun(500, func() {
		b := getRelayBuffer()
		putRelayBuffer(b)
	})
	if got != 0 {
		t.Errorf("a get/put round trip allocates %.0f times; the pool exists to make it "+
			"zero. The usual cause is storing []byte instead of *[]byte — putting a "+
			"slice into a sync.Pool boxes it, and the box is an allocation.", got)
	}
}

// And the buffer handed out is the size the copy loop expects. A pool that
// quietly returned a short buffer would turn one syscall per 64 KB into
// several, which is the cost pooling was meant to avoid in the first place.
func TestTheRelayBufferIsTheExpectedSize(t *testing.T) {
	b := getRelayBuffer()
	defer putRelayBuffer(b)
	if len(*b) != relayBufferSize {
		t.Errorf("pooled buffer is %d bytes, want %d", len(*b), relayBufferSize)
	}
}

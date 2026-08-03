package handlers

import "sync"

// The buffer every forwarded connection copies through.
//
// Two things about the old arrangement cost throughput, and both are in the one
// line it took to allocate it.
//
// It was 16 KB. A relay's work is syscalls, not arithmetic: every buffer's
// worth of data costs one read and one write, so the buffer size sets how many
// syscalls a gigabyte takes. At 16 KB that is about 131,000 of them per
// gigabyte per direction, and on the small VPS this tunnel usually runs on,
// that is the thing the CPU is actually busy with. 64 KB cuts it to a quarter
// of that. It is also what gost uses, and twice what Go's own io.Copy defaults
// to; past that the returns fall off, because the cost stops being the syscall
// count and starts being the copy itself.
//
// And it was allocated per direction per connection — two 16 KB allocations for
// every connection the tunnel forwards. For a long download that is nothing.
// For a browser opening thirty short connections to load one page, it is thirty
// times two garbage-collected allocations that did not have to exist, on a
// machine where the garbage collector is competing with the thing the user is
// waiting for. Pooling them makes a connection's buffer free after the first
// one.

// relayBufferSize is how much a forwarded connection copies at a time.
const relayBufferSize = 64 * 1024

// relayBuffers hands out reusable copy buffers.
//
// It stores *[]byte rather than []byte: putting a slice into a sync.Pool
// converts it to an interface, and that conversion allocates — which would put
// back a good part of what pooling is here to save.
var relayBuffers = sync.Pool{
	New: func() any {
		b := make([]byte, relayBufferSize)
		return &b
	},
}

// getRelayBuffer takes a buffer from the pool.
func getRelayBuffer() *[]byte { return relayBuffers.Get().(*[]byte) }

// putRelayBuffer returns a buffer to the pool. The contents are not cleared:
// the buffer is only ever read back over the length that was just written into
// it, so what is left underneath is never seen.
func putRelayBuffer(b *[]byte) { relayBuffers.Put(b) }

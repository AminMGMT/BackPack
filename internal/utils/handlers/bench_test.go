package handlers

import (
	"testing"
)

// The relay buffer pool, per connection.
//
// Every forwarded connection on every reverse transport takes two of these and
// gives them back, so the pool is on the hottest path in the product that is
// not the packet path itself. It is asserted allocation-free in alloc_test.go;
// this says what it costs.

func BenchmarkRelayBufferPool(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := getRelayBuffer()
		putRelayBuffer(buf)
	}
}

// Both halves at once, which is what a connection actually does: one buffer per
// direction, held for the life of the transfer.
func BenchmarkRelayBufferPair(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		up := getRelayBuffer()
		down := getRelayBuffer()
		putRelayBuffer(up)
		putRelayBuffer(down)
	}
}

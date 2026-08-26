// Package testport hands out loopback ports for tests to bind.
//
// It exists because the obvious way to do this is wrong, and the same wrong
// version was written three times in this repository — in the end-to-end
// harness, in the direct tunnel's tests and in the layer-3 forwarder's — with
// two of the three going on to fail on CI in exactly the same way:
//
//	the udp tunnel never came up
//	no reply ever came back through the udp forwarder
//
// Both read as a timeout and were diagnosed as flakiness. Neither was. A port
// that was never bound answers nothing, for as long as anybody cares to wait,
// and raising the timeout only makes the failure take longer to arrive.
//
// The obvious version binds a port, reads its number and closes it:
//
//	l, _ := net.Listen("tcp", "127.0.0.1:0")
//	port := l.Addr().(*net.TCPAddr).Port
//	l.Close()
//	return port
//
// which is wrong twice.
//
// The first is the visible race: the socket is gone before the caller binds, so
// anything on the host may take the number in between. The second is the one
// that actually bites, and it is not obvious at all — the kernel draws the
// SOURCE port of every outgoing connection from the ephemeral range, and these
// suites open a great many outgoing connections. A listen port picked from
// inside that range can be handed to one of the suite's own dials before the
// thing under test gets to bind it. Linux's ephemeral range starts at 32768 and
// macOS's at 49152, which is why this fails on CI and passes on a laptop.
//
// And a port free for TCP says nothing about the same number on UDP, which
// matters because the tests that failed forward both.
package testport

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// The range ports are drawn from: below the lowest ephemeral range of any
// platform this is tested on, so the kernel never hands one of these out as the
// source port of somebody else's connection.
const (
	low  = 12000
	high = 30000
)

var (
	mu     sync.Mutex
	next   = low + (int(time.Now().UnixNano()/1e6) % (high - low))
	issued = map[int]bool{}
)

// Free returns a loopback port that is free for both TCP and UDP and has not
// been issued before in this run.
//
// The race cannot be closed completely without binding on the caller's behalf,
// which would defeat the purpose. What it does is make a collision rare enough
// to be a real event rather than a routine one — and when the caller's bind does
// fail, it fails at the bind, where the error says so, rather than as silence
// somewhere downstream.
func Free(t *testing.T) int {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()

	for attempts := 0; attempts < 4000; attempts++ {
		port := next
		next++
		if next > high {
			next = low
		}
		if issued[port] || !IsFree(port) {
			continue
		}
		issued[port] = true
		return port
	}
	t.Fatal("no free loopback port available for the test")
	return 0
}

// IsFree reports whether a port can be bound on loopback for both TCP and UDP.
func IsFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	l.Close()
	pc, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	pc.Close()
	return true
}

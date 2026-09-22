//go:build linux

package l3

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Is eight the right number of datagrams to gather per read?
//
// batchSize has been 8 since recvmmsg was added, on the stated grounds that the
// syscall saving has flattened out by then. That was an argument rather than a
// measurement, and the send side has just shown how wrong an argument about
// syscalls can be — `sendmmsg` removed seven eighths of them and bought
// nothing, while UDP segmentation at the *same* syscall count doubled the rate.
//
// So this asks the receive side the same question with numbers: how many
// datagrams a second the carrier takes off the socket at four widths. The
// caller's arrays decide the width, so nothing has to be recompiled to find
// out — which is also why this can stay as a measurement rather than becoming
// a knob.
//
//	go test ./internal/tunnel/l3/ -run TestBatchWidth -v -count=1
func TestBatchWidth(t *testing.T) {
	if testing.Short() {
		t.Skip("a measurement, not a check")
	}

	const datagrams = 40000
	payload := make([]byte, 1200)

	measure := func(width, senders, rcvbuf int) (kpps, perCall, received float64) {
		c := newLocalUDPCarrier(t)
		if rcvbuf > 0 {
			// The real carrier sizes this from the config — several megabytes
			// on every preset. Without it the question "is the reader too
			// slow" cannot be told apart from "is the queue too small".
			if err := c.SetReadBuffer(rcvbuf); err != nil {
				t.Fatalf("SO_RCVBUF: %v", err)
			}
		}
		sender, err := net.Dial("udp", c.LocalAddr().String())
		if err != nil {
			t.Fatalf("dialling: %v", err)
		}
		defer sender.Close()

		bufs := make([][]byte, width)
		for i := range bufs {
			bufs[i] = make([]byte, maxMTU+256)
		}
		sizes := make([]int, width)
		froms := make([]net.Addr, width)

		tun := &Tunnel{carrier: c}
		reader := asBatchReader(c)

		// One sender keeps the reader comfortable, which is the ordinary case
		// and is also why the batch rarely fills. Several senders is the
		// question underneath: whether one reading goroutine is the ceiling at
		// all, or whether the socket is.
		conns := []net.Conn{sender}
		for i := 1; i < senders; i++ {
			extra, err := net.Dial("udp", c.LocalAddr().String())
			if err != nil {
				t.Fatalf("dialling: %v", err)
			}
			defer extra.Close()
			conns = append(conns, extra)
		}
		each := datagrams / len(conns)
		for _, w := range conns {
			go func(w net.Conn) {
				for i := 0; i < each; i++ {
					w.Write(payload)
				}
			}(w)
		}

		got, calls := 0, 0
		start := time.Now()
		for got < datagrams {
			c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
			n, err := tun.receive(reader, bufs, sizes, froms)
			if err != nil {
				break
			}
			got += n
			calls++
		}
		elapsed := time.Since(start)
		return float64(got) / elapsed.Seconds() / 1000,
			float64(got) / float64(max(calls, 1)),
			100 * float64(got) / float64(datagrams)
	}

	for _, width := range []int{1, 8, batchSize, 128} {
		kpps, per, got := measure(width, 1, 0)
		t.Logf("width %3d, 1 sender : %7.0f kpps, %5.1f per call, %5.1f%% arrived", width, kpps, per, got)
	}

	// The question §5.5 asks — is one reading goroutine the ceiling? If it is,
	// the batch fills and the rate stops climbing with the senders. If it is
	// not, more senders means more datagrams a second and the batch still does
	// not fill, and SO_REUSEPORT with N readers would be solving a problem
	// nobody has.
	for _, senders := range []int{1, 2, 4, 8} {
		kpps, per, got := measure(batchSize, senders, 0)
		t.Logf("width %3d, %d senders: %7.0f kpps, %5.1f per call, %5.1f%% arrived", batchSize, senders, kpps, per, got)
	}

	// The same, with the receive buffer the real carrier is given. Losses that
	// disappear here were the queue, not the reader.
	for _, senders := range []int{4, 8} {
		kpps, per, got := measure(batchSize, senders, 8<<20)
		t.Logf("width %3d, %d senders, 8MB rcvbuf: %7.0f kpps, %5.1f per call, %5.1f%% arrived",
			batchSize, senders, kpps, per, got)
	}
}

// Would more than one reading goroutine raise the ceiling?
//
// This is the question §5.5 asks, and the mechanism it proposes — SO_REUSEPORT
// with N sockets — cannot answer it here. SO_REUSEPORT distributes incoming
// datagrams across the listening sockets **by flow**, and a layer-3 tunnel has
// exactly one: one peer, one source port. Every datagram hashes the same way,
// so N sockets would leave N-1 of them idle.
//
// What is left is N goroutines on the *same* socket, which the kernel allows
// and serialises. That is what this measures: the same load, read by one, two
// and four goroutines.
//
//	go test ./internal/tunnel/l3/ -run TestReadersPerSocket -v -count=1
func TestReadersPerSocket(t *testing.T) {
	if testing.Short() {
		t.Skip("a measurement, not a check")
	}

	const (
		datagrams = 60000
		senders   = 4
	)
	payload := make([]byte, 1200)

	measure := func(readers int) (kpps, received float64) {
		c := newLocalUDPCarrier(t)
		if err := c.SetReadBuffer(8 << 20); err != nil {
			t.Fatalf("SO_RCVBUF: %v", err)
		}

		var conns []net.Conn
		for i := 0; i < senders; i++ {
			w, err := net.Dial("udp", c.LocalAddr().String())
			if err != nil {
				t.Fatalf("dialling: %v", err)
			}
			defer w.Close()
			conns = append(conns, w)
		}

		var got atomic.Int64
		var wg sync.WaitGroup
		start := time.Now()
		for r := 0; r < readers; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				bufs := make([][]byte, batchSize)
				for i := range bufs {
					bufs[i] = make([]byte, maxMTU+256)
				}
				sizes := make([]int, batchSize)
				froms := make([]net.Addr, batchSize)
				tun := &Tunnel{carrier: c}
				reader := asBatchReader(c)
				for got.Load() < datagrams {
					c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
					n, err := tun.receive(reader, bufs, sizes, froms)
					if err != nil {
						return
					}
					got.Add(int64(n))
				}
			}()
		}

		each := datagrams / senders
		for _, w := range conns {
			go func(w net.Conn) {
				for i := 0; i < each; i++ {
					w.Write(payload)
				}
			}(w)
		}
		wg.Wait()
		elapsed := time.Since(start)
		n := float64(got.Load())
		return n / elapsed.Seconds() / 1000, 100 * n / datagrams
	}

	for _, readers := range []int{1, 2, 4} {
		kpps, arrived := measure(readers)
		t.Logf("%d reader(s): %7.0f kpps, %5.1f%% arrived", readers, kpps, arrived)
	}
}

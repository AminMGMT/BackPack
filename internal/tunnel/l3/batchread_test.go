package l3

import (
	"errors"
	"math"
	"net"
	"runtime"
	"testing"
	"time"
)

// newLocalUDPCarrier binds a plain UDP carrier on loopback.
func newLocalUDPCarrier(t *testing.T) *udpCarrier {
	t.Helper()
	c, err := listenUDP("127.0.0.1:0", 0)
	if err != nil {
		t.Fatalf("binding a carrier: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	uc, ok := c.(*udpCarrier)
	if !ok {
		t.Fatalf("listenUDP returned %T, not the plain UDP carrier", c)
	}
	return uc
}

// The whole point: several datagrams already waiting come back from one call.
func TestReadBatchGathersWhatHasArrived(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("recvmmsg is a Linux syscall")
	}
	c := newLocalUDPCarrier(t)

	sender, err := net.Dial("udp", c.LocalAddr().String())
	if err != nil {
		t.Fatalf("dialling the carrier: %v", err)
	}
	defer sender.Close()

	const sent = 5
	for i := 0; i < sent; i++ {
		if _, err := sender.Write([]byte{byte(i)}); err != nil {
			t.Fatalf("sending datagram %d: %v", i, err)
		}
	}
	// Give the kernel a moment to have them all queued, or the first call
	// legitimately returns fewer and the test measures scheduling rather than
	// batching.
	time.Sleep(50 * time.Millisecond)

	bufs := make([][]byte, batchSize)
	for i := range bufs {
		bufs[i] = make([]byte, 64)
	}
	sizes := make([]int, batchSize)
	froms := make([]net.Addr, batchSize)

	got, err := c.ReadBatch(bufs, sizes, froms)
	if err != nil {
		t.Fatalf("ReadBatch: %v", err)
	}
	if got < 2 {
		t.Fatalf("ReadBatch returned %d datagrams with %d queued; it is not batching at all", got, sent)
	}
	for i := 0; i < got; i++ {
		if sizes[i] != 1 {
			t.Fatalf("datagram %d has size %d, want 1", i, sizes[i])
		}
		if bufs[i][0] != byte(i) {
			t.Fatalf("datagram %d carried %d, want %d — the batch is out of order", i, bufs[i][0], i)
		}
		if froms[i] == nil {
			t.Fatalf("datagram %d has no source address", i)
		}
	}
}

// One datagram must come back as one datagram, immediately. If recvmmsg waited
// for the array to fill, an idle tunnel would stall on every packet — which is
// the failure that would make this change worse than not making it.
func TestReadBatchDoesNotWaitForASecondDatagram(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("recvmmsg is a Linux syscall")
	}
	c := newLocalUDPCarrier(t)

	sender, err := net.Dial("udp", c.LocalAddr().String())
	if err != nil {
		t.Fatalf("dialling the carrier: %v", err)
	}
	defer sender.Close()
	if _, err := sender.Write([]byte("one")); err != nil {
		t.Fatalf("sending: %v", err)
	}

	bufs := make([][]byte, batchSize)
	for i := range bufs {
		bufs[i] = make([]byte, 64)
	}
	sizes := make([]int, batchSize)
	froms := make([]net.Addr, batchSize)

	done := make(chan int, 1)
	go func() {
		n, err := c.ReadBatch(bufs, sizes, froms)
		if err != nil {
			done <- -1
			return
		}
		done <- n
	}()

	select {
	case n := <-done:
		if n != 1 {
			t.Fatalf("ReadBatch returned %d for one queued datagram", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadBatch blocked waiting for a second datagram that was never coming")
	}
}

// The pump has to keep working on a carrier that cannot batch — which is every
// carrier except plain UDP, so this is the common path, not the exotic one.
func TestReceiveFallsBackToTheSingleRead(t *testing.T) {
	c := newLocalUDPCarrier(t)
	tun := &Tunnel{carrier: c}

	sender, err := net.Dial("udp", c.LocalAddr().String())
	if err != nil {
		t.Fatalf("dialling the carrier: %v", err)
	}
	defer sender.Close()
	if _, err := sender.Write([]byte("hello")); err != nil {
		t.Fatalf("sending: %v", err)
	}

	bufs := [][]byte{make([]byte, 64)}
	sizes := make([]int, 1)
	froms := make([]net.Addr, 1)

	// nil reader is what a carrier with no batch capability produces.
	got, err := tun.receive(nil, bufs, sizes, froms)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if got != 1 || string(bufs[0][:sizes[0]]) != "hello" {
		t.Fatalf("got %d datagrams, %q", got, bufs[0][:sizes[0]])
	}
}

// refusingReader is a carrier that reports it cannot batch after all.
type refusingReader struct{}

func (refusingReader) ReadBatch([][]byte, []int, []net.Addr) (int, error) { return 0, errNoBatch }

// A batch read that says it cannot batch must fall through to the single read,
// not take the pump down. Losing the optimisation has to cost throughput, not
// the tunnel.
func TestABatchRefusalFallsThroughRatherThanFailing(t *testing.T) {
	c := newLocalUDPCarrier(t)
	tun := &Tunnel{carrier: c}

	sender, err := net.Dial("udp", c.LocalAddr().String())
	if err != nil {
		t.Fatalf("dialling the carrier: %v", err)
	}
	defer sender.Close()
	if _, err := sender.Write([]byte("hello")); err != nil {
		t.Fatalf("sending: %v", err)
	}

	bufs := [][]byte{make([]byte, 64)}
	sizes := make([]int, 1)
	froms := make([]net.Addr, 1)

	got, err := tun.receive(refusingReader{}, bufs, sizes, froms)
	if err != nil {
		t.Fatalf("a refused batch killed the pump: %v", err)
	}
	if got != 1 {
		t.Fatalf("got %d datagrams after falling through", got)
	}
}

// breakingReader is a real socket error, which must not be swallowed.
type breakingReader struct{ err error }

func (b breakingReader) ReadBatch([][]byte, []int, []net.Addr) (int, error) { return 0, b.err }

func TestARealBatchErrorIsReported(t *testing.T) {
	c := newLocalUDPCarrier(t)
	tun := &Tunnel{carrier: c}
	want := errors.New("socket went away")

	bufs := [][]byte{make([]byte, 64)}
	if _, err := tun.receive(breakingReader{want}, bufs, make([]int, 1), make([]net.Addr, 1)); !errors.Is(err, want) {
		t.Fatalf("error = %v, want it to carry the socket error", err)
	}
}

// Only the plain UDP carrier gets the capability. A wrapper that does work per
// datagram must not be handed a batch it will then have to take apart.
func TestOnlyThePlainUDPCarrierBatches(t *testing.T) {
	c := newLocalUDPCarrier(t)
	if runtime.GOOS == "linux" && asBatchReader(c) == nil {
		t.Fatal("the plain UDP carrier has no batch capability on Linux")
	}
	// pinnedCarrier is what multipath wraps each path in.
	if asBatchReader(&pinnedCarrier{DatagramCarrier: c}) != nil {
		t.Fatal("a wrapping carrier claimed a batch capability it does not implement")
	}
}

// ReadBatch must not allocate per call — the whole point is saving work on the
// data path, and a per-call allocation would give it straight back.
func TestReadBatchDoesNotAllocatePerCall(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("recvmmsg is a Linux syscall")
	}
	c := newLocalUDPCarrier(t)
	sender, err := net.Dial("udp", c.LocalAddr().String())
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	defer sender.Close()

	bufs := make([][]byte, batchSize)
	for i := range bufs {
		bufs[i] = make([]byte, 2048)
	}
	sizes := make([]int, batchSize)
	froms := make([]net.Addr, batchSize)

	// Prime, so the message array is already sized.
	sender.Write([]byte("x"))
	time.Sleep(20 * time.Millisecond)
	c.ReadBatch(bufs, sizes, froms)

	got := minAllocsPerRun(5, 20, func() {
		sender.Write([]byte("x"))
		c.ReadBatch(bufs, sizes, froms)
	})
	// The addresses the kernel reports are themselves allocated, one per
	// datagram, which is the floor here and is not something this code chooses.
	if budget := 6.0; got > budget {
		t.Fatalf("ReadBatch allocates %.1f objects per call, budget %.0f", got, budget)
	}
}

// The evidence for the change: how many datagrams a second the receive side can
// take off the socket, with and without recvmmsg.
//
// Run with:
//
//	go test ./internal/tunnel/l3/ -run TestBatchReceiveRate -v -count=1
//
// It is a rate, not a syscall count, because the rate is what an operator
// feels. The saving is one syscall per batch instead of one per datagram, and
// at a tunnel's packet rates that is the difference the number below shows.
func TestBatchReceiveRate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("recvmmsg is a Linux syscall")
	}
	if testing.Short() {
		t.Skip("a measurement, not a check")
	}

	const datagrams = 40000
	payload := make([]byte, 1200) // a realistic inner packet

	measure := func(batched bool) (time.Duration, int) {
		c := newLocalUDPCarrier(t)
		if !batched {
			c.v4, c.v6 = nil, nil // read it one at a time
		}
		sender, err := net.Dial("udp", c.LocalAddr().String())
		if err != nil {
			t.Fatalf("dialling: %v", err)
		}
		defer sender.Close()

		bufs := make([][]byte, batchSize)
		for i := range bufs {
			bufs[i] = make([]byte, maxMTU+256)
		}
		sizes := make([]int, batchSize)
		froms := make([]net.Addr, batchSize)

		tun := &Tunnel{carrier: c}
		var reader batchReader
		if batched {
			reader = asBatchReader(c)
		}

		go func() {
			for i := 0; i < datagrams; i++ {
				sender.Write(payload)
			}
		}()

		// Stop on a quiet socket rather than on a count: UDP on loopback can
		// still drop under a full send buffer, and waiting for a datagram that
		// was dropped would hang rather than fail.
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
		t.Logf("%-10s %6d datagrams in %5d calls (%.1f per call), %.0f kpps",
			map[bool]string{true: "recvmmsg", false: "one by one"}[batched],
			got, calls, float64(got)/float64(max(calls, 1)),
			float64(got)/elapsed.Seconds()/1000)
		return elapsed, got
	}

	plain, gotPlain := measure(false)
	batched, gotBatched := measure(true)

	if gotPlain == 0 || gotBatched == 0 {
		t.Skip("the socket dropped everything; nothing to compare")
	}
	t.Logf("one by one %v, recvmmsg %v", plain.Round(time.Millisecond), batched.Round(time.Millisecond))
}

// The transmit half.
//
// The receive path gathers several datagrams per syscall; the send path did
// not, and it is the side that already has a batch in hand — tun.Read returns
// several packets at once, and each one was then written with its own syscall.
// The gather is free: the packets are there, sealed, and going to the same
// peer.

func TestWriteBatchSendsEveryDatagram(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sendmmsg is a Linux syscall")
	}
	c := newLocalUDPCarrier(t)

	sink, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding a sink: %v", err)
	}
	defer sink.Close()

	const n = 5
	bufs := make([][]byte, n)
	for i := range bufs {
		bufs[i] = []byte{byte(i), byte(i), byte(i)}
	}

	sent, err := c.WriteBatch(bufs, sink.LocalAddr())
	if err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	if sent != n {
		t.Fatalf("sent %d of %d datagrams", sent, n)
	}

	seen := map[byte]bool{}
	buf := make([]byte, 64)
	for i := 0; i < n; i++ {
		sink.SetReadDeadline(time.Now().Add(2 * time.Second))
		got, _, err := sink.ReadFrom(buf)
		if err != nil {
			t.Fatalf("datagram %d never arrived: %v", i, err)
		}
		if got != 3 {
			t.Fatalf("datagram %d is %d bytes, want 3", i, got)
		}
		seen[buf[0]] = true
	}
	for i := 0; i < n; i++ {
		if !seen[byte(i)] {
			t.Errorf("datagram %d was not delivered", i)
		}
	}
}

// A carrier that cannot batch has to say so rather than silently sending
// nothing — the send path falls back to one write per packet, and a batch
// writer that quietly did nothing would be a tunnel that stops carrying.
func TestACarrierThatCannotBatchWriteSaysSo(t *testing.T) {
	c := newLocalUDPCarrier(t)
	c.v4, c.v6 = nil, nil

	_, err := c.WriteBatch([][]byte{{1}}, c.LocalAddr())
	if !errors.Is(err, errNoBatch) {
		t.Fatalf("err = %v, want errNoBatch — a batch writer that cannot batch "+
			"must not report success having sent nothing", err)
	}
}

func TestWritingAnEmptyBatchIsRefused(t *testing.T) {
	c := newLocalUDPCarrier(t)
	if _, err := c.WriteBatch(nil, c.LocalAddr()); err == nil {
		t.Fatal("an empty batch reported success")
	}
}

// minAllocsPerRun is testing.AllocsPerRun sampled a few times, keeping the
// lowest figure.
//
// The two batch tests below were flaky — roughly one run in five reported two
// or three times the steady-state count — and the cause is not the code under
// test. Both drive a real socket, and when the far side falls behind the send
// or receive path takes an error return, which allocates: an *net.OpError, a
// wrapped syscall error, a string. That has nothing to do with whether
// ReadBatch and WriteBatch allocate per call, which is the whole question.
//
// The minimum is the right statistic for an allocation budget and not a way of
// hiding a failure. Allocations cannot be under-counted: interference can only
// push the figure up, never down, so the lowest of several samples is the
// steady-state cost and a budget that the lowest sample still exceeds is a
// budget genuinely exceeded.
func minAllocsPerRun(samples, runs int, f func()) float64 {
	best := math.Inf(1)
	for i := 0; i < samples; i++ {
		if got := testing.AllocsPerRun(runs, f); got < best {
			best = got
		}
	}
	return best
}

// Same budget argument as the receive side: the point is saving work on the
// data path, and a per-call allocation gives it straight back.
func TestWriteBatchDoesNotAllocatePerCall(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sendmmsg is a Linux syscall")
	}
	c := newLocalUDPCarrier(t)
	sink, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding a sink: %v", err)
	}
	defer sink.Close()
	go func() {
		b := make([]byte, 2048)
		for {
			if _, _, err := sink.ReadFrom(b); err != nil {
				return
			}
		}
	}()

	bufs := make([][]byte, batchSize)
	for i := range bufs {
		bufs[i] = make([]byte, 1200)
	}
	to := sink.LocalAddr()
	c.WriteBatch(bufs, to) // prime

	got := minAllocsPerRun(5, 20, func() { c.WriteBatch(bufs, to) })
	if budget := 4.0; got > budget {
		t.Fatalf("WriteBatch allocates %.1f objects per call, budget %.0f", got, budget)
	}
}

// The evidence for the send side, next to the receive side's.
//
//	go test ./internal/tunnel/l3/ -run TestBatchSendRate -v -count=1
func TestBatchSendRate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sendmmsg is a Linux syscall")
	}
	if testing.Short() {
		t.Skip("a measurement, not a check")
	}

	sink, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding a sink: %v", err)
	}
	defer sink.Close()
	// Drained, or the socket buffer fills and the measurement becomes one of
	// how fast the kernel drops things.
	go func() {
		b := make([]byte, 2048)
		for {
			if _, _, err := sink.ReadFrom(b); err != nil {
				return
			}
		}
	}()
	to := sink.LocalAddr()

	const datagrams = 40000
	payload := make([]byte, 1200)

	measure := func(batched bool) (time.Duration, int) {
		c := newLocalUDPCarrier(t)
		if !batched {
			c.v4, c.v6 = nil, nil
		}
		bufs := make([][]byte, batchSize)
		for i := range bufs {
			bufs[i] = payload
		}

		// syscalls, not loop iterations. The one-at-a-time path does one write
		// per datagram inside the loop, so counting iterations would report it
		// as making the same number of calls as the batched one — which is the
		// entire difference being measured.
		sent, syscalls := 0, 0
		start := time.Now()
		for sent < datagrams {
			n := batchSize
			if rest := datagrams - sent; rest < n {
				n = rest
			}
			if batched {
				got, err := c.WriteBatch(bufs[:n], to)
				if err != nil {
					break
				}
				sent += got
				syscalls++
			} else {
				for i := 0; i < n; i++ {
					if _, err := c.WriteTo(payload, to); err != nil {
						break
					}
					sent++
					syscalls++
				}
			}
		}
		elapsed := time.Since(start)
		t.Logf("%-11s %6d datagrams in %6d syscalls (%.1f per call), %.0f kpps",
			map[bool]string{true: "sendmmsg", false: "one by one"}[batched],
			sent, syscalls, float64(sent)/float64(max(syscalls, 1)),
			float64(sent)/elapsed.Seconds()/1000)
		return elapsed, sent
	}

	plain, gotPlain := measure(false)
	batched, gotBatched := measure(true)
	if gotPlain == 0 || gotBatched == 0 {
		t.Skip("nothing was sent; nothing to compare")
	}
	t.Logf("one by one %v, sendmmsg %v",
		plain.Round(time.Millisecond), batched.Round(time.Millisecond))
}

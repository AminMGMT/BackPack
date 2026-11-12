package l3

import (
	"context"
	"errors"
	"net"
	"testing"
)

// The pump's side of batching: what it does with a carrier that has no batch
// path, one that refuses, and one that fails. The carriers that really batch —
// pck and xdi — need raw sockets; see network's own tests for theirs.

// newLoopbackCarrier binds the tests' loopback carrier, which has no batch
// path, on an ephemeral port.
func newLoopbackCarrier(t *testing.T) DatagramCarrier {
	t.Helper()
	c, _, err := openLoopback(context.Background(), Config{Mode: ModeListen, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("binding the loopback carrier: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// The pump has to keep working on a carrier that cannot batch — spoof and sni
// cannot, so this is a common path, not the exotic one.
func TestReceiveFallsBackToTheSingleRead(t *testing.T) {
	c := newLoopbackCarrier(t)
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
	c := newLoopbackCarrier(t)
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
	c := newLoopbackCarrier(t)
	tun := &Tunnel{carrier: c}
	want := errors.New("socket went away")

	bufs := [][]byte{make([]byte, 64)}
	if _, err := tun.receive(breakingReader{want}, bufs, make([]int, 1), make([]net.Addr, 1)); !errors.Is(err, want) {
		t.Fatalf("error = %v, want it to carry the socket error", err)
	}
}

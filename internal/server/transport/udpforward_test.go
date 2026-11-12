package transport

import (
	"bytes"
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/backpack/backpack/internal/utils/network"
)

// A source whose flow could not be started must be able to start one on its
// next datagram.
//
// This is the bug that made forwarded UDP stop for good rather than for a
// moment. The implementation this replaces recorded a new source address before
// offering the flow, and left the record behind when the offer failed — so once
// a channel filled up, every later datagram from that peer was filed against a
// flow nobody was reading, and that peer was silent until the service was
// restarted. Here the failed flow is dropped whole, and the peer recovers.
func TestARefusedFlowDoesNotBlackholeTheSource(t *testing.T) {
	fw := testForwarder(t, "9000")
	local := fw.conn.LocalAddr().String()

	admitted := make(chan net.Conn, 4)
	// Read by the forwarder's own goroutine, so it is atomic rather than a bare
	// bool the race detector would rightly object to.
	var accept atomic.Bool
	go fw.run(func(conn net.Conn, target string) bool {
		if !accept.Load() {
			return false // the transport is busy: refuse this one
		}
		admitted <- conn
		return true
	})

	peer, err := net.Dial("udp", local)
	if err != nil {
		t.Fatalf("cannot reach the forwarded port: %v", err)
	}
	defer peer.Close()

	// Refused.
	if _, err := peer.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	select {
	case <-admitted:
		t.Fatal("a refused flow was admitted anyway")
	default:
	}

	// The same peer, once the transport is ready again. The old code never got
	// here: the source was already in the table, so this datagram went into a
	// queue nobody read.
	accept.Store(true)
	if _, err := peer.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	select {
	case conn := <-admitted:
		defer conn.Close()
		buf := make([]byte, network.MaxDatagram)
		n, err := network.ReadDatagram(conn, buf)
		if err != nil {
			t.Fatalf("the admitted flow carried nothing: %v", err)
		}
		if string(buf[:n]) != "second" {
			t.Errorf("the flow carried %q, want the datagram that started it", buf[:n])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the source was blackholed: a later datagram never started a flow")
	}
}

// The mark on the target is what the far end reads to know this is UDP.
func TestAdmittedFlowsCarryTheUDPMark(t *testing.T) {
	fw := testForwarder(t, "127.0.0.1:9000")
	local := fw.conn.LocalAddr().String()
	targets := make(chan string, 1)

	go fw.run(func(conn net.Conn, target string) bool {
		select {
		case targets <- target:
		default:
		}
		return true
	})

	peer, err := net.Dial("udp", local)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	peer.Write([]byte("hello"))

	select {
	case got := <-targets:
		target, isUDP := network.SplitUDPTarget(got)
		if !isUDP {
			t.Errorf("the flow was announced as %q, which the far end will dial over TCP", got)
		}
		if target != "127.0.0.1:9000" {
			t.Errorf("the target arrived as %q", target)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no flow was ever offered")
	}
}

// The flow is a net.Conn in both directions: datagrams in come out framed, and
// framed bytes written in — split anywhere, because the tunnel decides where —
// go back to the source as whole datagrams.
func TestFlowFramesBothDirections(t *testing.T) {
	fw := testForwarder(t, "9000")
	local := fw.conn.LocalAddr().String()
	flows := make(chan net.Conn, 1)

	go fw.run(func(conn net.Conn, target string) bool {
		select {
		case flows <- conn:
		default:
		}
		return true
	})

	peer, err := net.Dial("udp", local)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	sent := bytes.Repeat([]byte("abc"), 500)
	if _, err := peer.Write(sent); err != nil {
		t.Fatal(err)
	}

	var flow net.Conn
	select {
	case flow = <-flows:
	case <-time.After(2 * time.Second):
		t.Fatal("no flow was offered")
	}
	defer flow.Close()

	buf := make([]byte, network.MaxDatagram)
	n, err := network.ReadDatagram(flow, buf)
	if err != nil {
		t.Fatalf("the flow did not frame the datagram: %v", err)
	}
	if !bytes.Equal(buf[:n], sent) {
		t.Fatal("the datagram came out of the flow altered")
	}

	// Back the other way, written one byte at a time so the reassembly is
	// forced to hold state across calls.
	reply := []byte("reply-payload")
	var framed bytes.Buffer
	if err := network.WriteDatagram(&framed, reply); err != nil {
		t.Fatal(err)
	}
	for _, b := range framed.Bytes() {
		if _, err := flow.Write([]byte{b}); err != nil {
			t.Fatalf("writing a frame byte by byte: %v", err)
		}
	}

	peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, 64)
	rn, err := peer.Read(got)
	if err != nil {
		t.Fatalf("the reply never reached the source: %v", err)
	}
	if !bytes.Equal(got[:rn], reply) {
		t.Errorf("the source received %q, want %q", got[:rn], reply)
	}
}

// A forwarded port whose UDP side cannot be bound must leave the tunnel
// running. It used to be fatal, so one busy datagram port took down the whole
// server — every other forwarded port with it.
func TestABusyUDPPortIsNotFatal(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		startUDPForward(ctx, quietLogger(), pc.LocalAddr().String(), "9000",
			func(net.Conn, string) bool { return true })
	}()

	select {
	case <-done: // returned instead of killing the process
	case <-time.After(2 * time.Second):
		t.Fatal("binding a busy UDP port neither failed nor returned")
	}
}

// testForwarder binds a forwarder on an ephemeral port and cleans it up.
//
// Built here rather than through startUDPForward so the socket is bound before
// the test sends anything: a readiness check that probes the port by binding it
// can win that race and take the port the forwarder was about to use.
func testForwarder(t *testing.T, target string) *udpForwarder {
	t.Helper()
	fw, err := newUDPForwarder(quietLogger(), "127.0.0.1:0", target)
	if err != nil {
		t.Fatalf("cannot bind a UDP forwarder: %v", err)
	}
	t.Cleanup(fw.close)
	return fw
}

// Datagrams that are already queued ride one Read, so a relay that writes each
// Read to a stream and waits for it is not limited to one datagram per write.
// The bytes are the same as ever — length-prefixed frames, in order — however
// the Reads cut them.
func TestFlowReadTakesEverythingQueued(t *testing.T) {
	fw := testForwarder(t, "9000")
	peer := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4000}
	f := newUDPFlow(fw, peer, peer.String())

	var want []byte
	for i := 0; i < 20; i++ {
		payload := bytes.Repeat([]byte{byte(i)}, 10+i*7)
		f.deliver(payload)
		want = append(want, byte(len(payload)>>8), byte(len(payload)))
		want = append(want, payload...)
	}

	buf := make([]byte, 64*1024)
	n, err := f.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:n], want) {
		t.Fatalf("one Read returned %d bytes, want all %d queued", n, len(want))
	}

	// A small buffer splits the same stream at arbitrary points; nothing is
	// lost or reordered, and a frame cut by one Read continues in the next.
	for i := 0; i < 20; i++ {
		payload := bytes.Repeat([]byte{byte(i)}, 10+i*7)
		f.deliver(payload)
	}
	var got []byte
	small := make([]byte, 37)
	for len(got) < len(want) {
		n, err := f.Read(small)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, small[:n]...)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("a small buffer changed the stream")
	}
}

// A Read does not wait for a batch: with one datagram queued it returns it.
func TestFlowReadDoesNotWaitToBatch(t *testing.T) {
	fw := testForwarder(t, "9000")
	peer := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4001}
	f := newUDPFlow(fw, peer, peer.String())
	f.deliver([]byte("one"))
	done := make(chan int, 1)
	go func() {
		n, _ := f.Read(make([]byte, 4096))
		done <- n
	}()
	select {
	case n := <-done:
		if n != 5 {
			t.Fatalf("Read returned %d bytes, want 5", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read held a lone datagram back")
	}
}

// A flow whose queue is full drops the datagram that has waited longest, so
// what the tunnel carries next is the freshest traffic, not a backlog.
func TestAFullFlowQueueDropsTheOldestDatagram(t *testing.T) {
	fw := testForwarder(t, "9000")
	peer := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4002}
	f := newUDPFlow(fw, peer, peer.String())
	total := udpFlowQueue + 40
	for i := 0; i < total; i++ {
		f.deliver([]byte{byte(i >> 8), byte(i)})
	}
	buf := make([]byte, 64*1024)
	n, err := f.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != udpFlowQueue*4 {
		t.Fatalf("Read returned %d bytes, want %d queued datagrams of 4", n, udpFlowQueue)
	}
	first := int(buf[2])<<8 | int(buf[3])
	last := int(buf[n-2])<<8 | int(buf[n-1])
	if first != 40 || last != total-1 {
		t.Fatalf("kept datagrams %d..%d, want 40..%d (the oldest 40 dropped)", first, last, total-1)
	}
}

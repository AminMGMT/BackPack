package paqet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtaci/smux"
)

// slowConn is one end of a pipe whose every write waits first: a session whose
// frames each take a while to get into the tunnel, as on a loaded one.
type slowConn struct {
	net.Conn
	delay time.Duration
}

func (c slowConn) Write(p []byte) (int, error) {
	time.Sleep(c.delay)
	return c.Conn.Write(p)
}

// The soak run found forwarded UDP losing 93% of its datagrams on a loaded pck
// tunnel, because each datagram was written to its stream by the loop reading
// the flow's socket, and waited there for its turn in the tunnel. Writing a
// flow's datagrams must not wait on the tunnel, and what arrives meanwhile has
// to cross together.
func TestAUDPFlowIsNotPacedByTheTunnelsQueue(t *testing.T) {
	a, b := net.Pipe()
	cs, err := smux.Client(slowConn{a, 20 * time.Millisecond}, smux.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	ss, err := smux.Server(b, smux.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); ss.Close() })
	st, err := cs.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	var drops atomic.Uint64
	conn := newDatagramConn(st, nil, nil, newDatagramSender(st, &drops))
	defer conn.Close()

	const n = 300
	start := time.Now()
	for i := 0; i < n; i++ {
		d := make([]byte, 400)
		binary.BigEndian.PutUint32(d, uint32(i))
		if _, err := conn.Write(d); err != nil {
			t.Fatal(err)
		}
	}
	// One write at a time, the 300 would take 300 x 20 ms.
	if took := time.Since(start); took > time.Second {
		t.Fatalf("writing %d datagrams took %s: the writer waited on the tunnel", n, took)
	}

	srv, err := ss.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	srv.SetReadDeadline(time.Now().Add(5 * time.Second))
	var mu sync.Mutex
	var pending []byte
	buf := make([]byte, maxDatagram)
	for i := 0; i < n; i++ {
		k, err := readFramed(&mu, &pending, srv, buf)
		if err != nil {
			t.Fatalf("datagram %d: %v (%d dropped)", i, err, drops.Load())
		}
		if k != 400 || binary.BigEndian.Uint32(buf) != uint32(i) {
			t.Fatalf("datagram %d arrived as %d bytes numbered %d", i, k, binary.BigEndian.Uint32(buf))
		}
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("the %d datagrams took %s to cross: they were not sent together", n, took)
	}
}

// blockedWriter never finishes a write until released: a tunnel that has
// stopped taking anything.
type blockedWriter struct {
	release chan struct{}
	got     bytes.Buffer
	mu      sync.Mutex
}

func (w *blockedWriter) Write(p []byte) (int, error) {
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.got.Write(p)
}

// A flow that offers more than the tunnel takes loses datagrams, counted, and
// its sender is never held up by it. What was queued still goes, in order.
func TestAFullFlowDropsDatagramsInsteadOfBlocking(t *testing.T) {
	w := &blockedWriter{release: make(chan struct{})}
	var drops atomic.Uint64
	s := newDatagramSender(w, &drops)
	defer s.close()
	d := make([]byte, 1000)
	start := time.Now()
	sent := 0
	for i := 0; i < 3000; i++ { // 3 MB, three times the queue
		if err := s.send(d); err != nil {
			t.Fatal(err)
		}
		sent++
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("queueing took %s", took)
	}
	if drops.Load() == 0 {
		t.Fatal("nothing was dropped although the flow offered three times its queue")
	}
	close(w.release)
	deadline := time.Now().Add(5 * time.Second)
	want := (sent - int(drops.Load())) * 1002
	for {
		w.mu.Lock()
		got := w.got.Len()
		w.mu.Unlock()
		if got == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d bytes written, want %d (%d of %d dropped)", got, want, drops.Load(), sent)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("stream gone") }

// Once the stream is gone, sending says so: the forwarder ends the flow on it.
func TestASenderWhoseStreamIsGoneFails(t *testing.T) {
	s := newDatagramSender(failingWriter{}, nil)
	defer s.close()
	s.send([]byte("x"))
	deadline := time.Now().Add(2 * time.Second)
	for s.send([]byte("y")) == nil {
		if time.Now().After(deadline) {
			t.Fatal("sending kept succeeding on a stream whose writes fail")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.send(make([]byte, maxDatagram+1)); !errors.Is(err, errDatagramTooLarge) && err == nil {
		t.Fatal("an oversized datagram was accepted")
	}
}

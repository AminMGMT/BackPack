package paqet

import (
	"encoding/binary"
	"io"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/xtaci/smux"
)

// A window update used to be written by the Read that triggered it, which
// returned only when the frame had been through the session's write queue. On a
// loaded session that wait stopped the reader, and with it the stream's
// consumption, for as long as the queue was.
func TestAReadDoesNotWaitForItsWindowUpdateToBeWritten(t *testing.T) {
	a, b := net.Pipe()
	gate := &gatedConn{Conn: b, open: make(chan struct{})}
	cfg := smux.DefaultConfig()
	cfg.Version = 2
	cs, err := smux.Client(a, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := smux.Server(gate, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); ss.Close() })
	st, err := cs.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	srv, err := ss.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	// From here nothing the server writes goes out: its window update cannot.
	gate.close()
	done := make(chan int, 1)
	go func() {
		buf := make([]byte, 16)
		n, _ := srv.Read(buf)
		done <- n
	}()
	select {
	case n := <-done:
		if n != 5 {
			t.Fatalf("read %d bytes", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the Read waited for its window update to be written")
	}
	gate.release()
}

// Control frames (window updates, stream opens) go ahead of the data of every
// other stream, not behind a round of it.
func TestAControlFrameDoesNotWaitForTheDataOfOtherStreams(t *testing.T) {
	a, b := net.Pipe()
	gate := &gatedConn{Conn: a, open: make(chan struct{})}
	cfg := smux.DefaultConfig()
	cfg.Version = 2
	cs, err := smux.Client(gate, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); b.Close() })

	// The wire, as smux frames it: version, command, length, stream id. A
	// stream's open is command 0 (SYN) and its data command 2 (PSH).
	var mu sync.Mutex
	var cmds []byte
	go func() {
		hdr := make([]byte, 8)
		for {
			if _, err := io.ReadFull(b, hdr); err != nil {
				return
			}
			if n := int(binary.LittleEndian.Uint16(hdr[2:])); n > 0 {
				io.CopyN(io.Discard, b, int64(n))
			}
			mu.Lock()
			cmds = append(cmds, hdr[1])
			mu.Unlock()
		}
	}()

	streams := make([]*smux.Stream, 30)
	for i := range streams {
		if streams[i], err = cs.OpenStream(); err != nil {
			t.Fatal(err)
		}
	}
	// Hold the transport and queue a frame of data on each of the 30.
	gate.close()
	mu.Lock()
	mark := len(cmds)
	mu.Unlock()
	for _, st := range streams {
		go st.Write(make([]byte, 60000))
	}
	time.Sleep(400 * time.Millisecond)
	// Then open one more, and let the transport go.
	go cs.OpenStream()
	time.Sleep(100 * time.Millisecond)
	gate.release()

	var after []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		after = append([]byte(nil), cmds[mark:]...)
		mu.Unlock()
		if slices.Contains(after, 0) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	i := slices.Index(after, 0)
	if i < 0 {
		t.Fatalf("the stream open never reached the wire: %v", after)
	}
	if i > 3 {
		t.Fatalf("the stream open went out after %d frames of the other streams' data", i)
	}
}

// gatedConn holds every write while closed, and lets them through once released.
type gatedConn struct {
	net.Conn
	mu   sync.Mutex
	open chan struct{}
	shut bool
}

func (g *gatedConn) close()   { g.mu.Lock(); g.shut = true; g.open = make(chan struct{}); g.mu.Unlock() }
func (g *gatedConn) release() { g.mu.Lock(); g.shut = false; close(g.open); g.mu.Unlock() }
func (g *gatedConn) Write(p []byte) (int, error) {
	g.mu.Lock()
	shut, ch := g.shut, g.open
	g.mu.Unlock()
	if shut {
		<-ch
	}
	return g.Conn.Write(p)
}

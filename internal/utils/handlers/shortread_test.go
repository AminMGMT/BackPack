package handlers

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// halfConn is a net.Conn whose Read returns bytes and an error in the same
// call, which io.Reader explicitly allows and which the relay used to discard.
type halfConn struct {
	net.Conn
	once sync.Once
	data []byte
}

func (c *halfConn) Read(b []byte) (int, error) {
	n := 0
	c.once.Do(func() { n = copy(b, c.data) })
	if n > 0 {
		return n, io.EOF
	}
	return 0, io.EOF
}

func (c *halfConn) Close() error { return nil }

// A read that returns data and an error together must forward the data.
//
// io.Reader says it plainly: a Read may return what it has along with the error
// that ended the stream, and a caller is obliged to process the bytes before
// the error. The relay returned on the error and dropped them — so a
// connection whose last read carries the tail of a response and EOF in one call
// loses that tail, silently, on a path that otherwise carries everything
// faithfully.
//
// None of the readers wired to this path does it today, which is why nobody has
// seen it. That is a property of the readers, not of this loop, and it holds
// only until somebody puts a different one here.
func TestTheLastBytesOfAStreamAreNotDropped(t *testing.T) {
	const payload = "the end of the response"

	// The source is not a pipe: it is a reader that hands back its bytes and
	// its error in one call. The destination is, so the test can read what the
	// relay put there.
	from := &halfConn{data: []byte(payload)}
	dst, far := net.Pipe()
	defer far.Close()

	got := make(chan string, 1)
	go func() {
		buf := make([]byte, len(payload))
		n, err := io.ReadFull(far, buf)
		if n == 0 && err != nil && !errors.Is(err, io.EOF) {
			got <- ""
			return
		}
		got <- string(buf[:n])
	}()

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)
	done := make(chan struct{})
	go func() {
		defer close(done)
		transferData(from, dst, log, nil, 0, false)
	}()

	select {
	case s := <-got:
		if s != payload {
			t.Errorf("the far side received %q, want %q — the bytes that arrived with "+
				"the error were thrown away", s, payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing reached the far side at all")
	}
	<-done
}

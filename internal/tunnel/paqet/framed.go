package paqet

import (
	"errors"
	"io"
	"sync"

	"github.com/xtaci/smux"
)

// Half-close on a forwarded TCP connection.
//
// A smux stream closes both directions at once: Close sends a FIN, the other
// end reads EOF, and the stream is gone, so a reply still in flight is thrown
// away. paqet forwards TCP that way, and it breaks every protocol that says "I
// am done sending" with shutdown(SHUT_WR) and then waits for the answer — an
// HTTP/1.0 client, a netcat pipe, rsync, a database dump, a TCP health probe
// that sends a line and half-closes. The TUN path kept the end-of-request
// separate from the end-of-response, so this engine must too.
//
// After the server's one-byte verdict, a TCP stream carries frames in each
// direction: a two-byte big-endian length n and then n bytes of data. A frame
// with n = 0 carries no data and means "this direction is finished" — the
// sender's CloseWrite. Zero-length data is never sent as a frame, so the
// value is free to mean that. A stream that ends without the marker (the peer
// closed it whole) reads as EOF as well, as it always did.

// frameChunk is the most data one frame carries: with its two-byte header it
// is exactly smux's largest frame (65535, see smuxConfig), so a frame is one
// smux frame and a write is never split.
const frameChunk = 65535 - 2

var streamFramePool = sync.Pool{New: func() any { b := make([]byte, 2+frameChunk); return &b }}

// framedStream is a smux stream whose data is framed so that each direction
// can end by itself. It is the TCP kind of forwarded connection, on both ends.
type framedStream struct {
	*smux.Stream

	wmu    sync.Mutex
	wclose bool // our CloseWrite has been sent

	rmu    sync.Mutex
	left   int // bytes of the current frame not yet read
	hdr    [2]byte
	hn     int  // bytes of the next frame's header already read
	rdone  bool // the other end's marker, or the stream's end, was read
	rawEnd error
}

func newFramedStream(st *smux.Stream) *framedStream { return &framedStream{Stream: st} }

var errWriteClosed = errors.New("paqet: write on a connection whose write side is closed")

// Read returns data until the other end's CloseWrite, then io.EOF.
func (f *framedStream) Read(p []byte) (int, error) {
	f.rmu.Lock()
	defer f.rmu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	for f.left == 0 {
		if f.rdone {
			return 0, f.rawEnd
		}
		// A header read in two pieces across a read deadline keeps its first
		// byte: losing it would put every frame after it out of step.
		for f.hn < 2 {
			k, err := f.Stream.Read(f.hdr[f.hn:])
			f.hn += k
			if err != nil {
				if f.hn < 2 && err != io.EOF {
					return 0, err // a deadline or a closed stream: not the end
				}
				if f.hn == 2 {
					break // the header arrived with the end behind it
				}
				if f.hn > 0 {
					err = io.ErrUnexpectedEOF
				}
				f.rdone, f.rawEnd = true, err
				return 0, err
			}
		}
		hdr := f.hdr
		f.hn = 0
		n := int(hdr[0])<<8 | int(hdr[1])
		if n == 0 {
			f.rdone, f.rawEnd = true, io.EOF
			return 0, io.EOF
		}
		f.left = n
	}
	if len(p) > f.left {
		p = p[:f.left]
	}
	n, err := f.Stream.Read(p)
	f.left -= n
	if err == io.EOF && f.left > 0 {
		// The stream ended inside a frame.
		err = io.ErrUnexpectedEOF
		f.rdone, f.rawEnd = true, err
	}
	if err == io.EOF {
		// A frame was read to its last byte and then the stream ended: hand
		// over the data now, the end on the next call.
		err = nil
	}
	return n, err
}

// WriteTo copies the connection to w a frame at a time. It exists because the
// stream's own would hand w the frame headers.
func (f *framedStream) WriteTo(w io.Writer) (int64, error) {
	return io.Copy(w, struct{ io.Reader }{f})
}

// Write sends p as frames.
func (f *framedStream) Write(p []byte) (int, error) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	if f.wclose {
		return 0, errWriteClosed
	}
	bp := streamFramePool.Get().(*[]byte)
	defer streamFramePool.Put(bp)
	buf := *bp
	sent := 0
	for len(p) > 0 {
		n := min(len(p), frameChunk)
		buf[0], buf[1] = byte(n>>8), byte(n)
		copy(buf[2:], p[:n])
		if _, err := f.Stream.Write(buf[:2+n]); err != nil {
			return sent, err
		}
		sent += n
		p = p[n:]
	}
	return sent, nil
}

// CloseWrite tells the other end that nothing more will be sent, and leaves the
// read side open for the reply.
func (f *framedStream) CloseWrite() error {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	if f.wclose {
		return nil
	}
	f.wclose = true
	_, err := f.Stream.Write([]byte{0, 0})
	return err
}

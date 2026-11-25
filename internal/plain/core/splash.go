package core

import (
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"sync"
)

// Splash (Backpack): the first records of every connection padded to random
// sizes, the way Xray's Vision pads them.
//
// Encryption hides what a record says, not how long it is, and the first few
// records of a connection are the most telling: whatever runs inside the
// tunnel opens with its own handshake — a TLS ClientHello of a few hundred
// bytes, a server's certificate flight of a few kilobytes — and those lengths
// come through an encrypted tunnel unchanged. That is what "TLS in TLS"
// detection reads. Splash pads the first splashRecords records each way so
// their lengths say nothing, and then gets out of the way: every byte after
// them is passed through as it is, so a long transfer pays nothing.
//
// It sits inside the encryption (under the Noise layer, or under the TLS of
// wss and wssmux); padding in the clear would hide nothing. Both ends count
// records, and both stop framing after the same number, so no marker is sent.

// splashRecords is how many records each way are padded.
const splashRecords = 8

// splashMin and splashMax bound a padded short record: anything shorter than
// splashMin is padded to a random length in [splashMin, splashMax].
const (
	splashMin = 900
	splashMax = 1400
	// splashLongPad is the most filler a record already longer than splashMin
	// gets: its length is already uninformative within a few hundred bytes.
	splashLongPad = 255
)

// splashHeader is a framed record's header: data length, filler length.
const splashHeader = 4

// splashPad draws a record's filler for n bytes of data.
func splashPad(n int) int {
	if n < splashMin {
		lo := splashMin - n
		return lo + rand.IntN(splashMax-splashMin+1)
	}
	return rand.IntN(splashLongPad + 1)
}

// splashConn frames the first splashRecords records each way, then passes
// through.
type splashConn struct {
	net.Conn

	wmu     sync.Mutex
	written int // framed records sent

	rmu    sync.Mutex
	read   int // framed records received
	rleft  int // data bytes of the current framed record not yet read
	rpad   int // filler bytes to skip after them
	rheadr [splashHeader]byte
}

// newSplash wraps c.
func newSplash(c net.Conn) net.Conn { return &splashConn{Conn: c} }

// NetConn is the connection under splash.
func (c *splashConn) NetConn() net.Conn { return c.Conn }

// maxSplashData is the most data one framed record carries.
const maxSplashData = 16 * 1024

func (c *splashConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	total := 0
	for c.written < splashRecords && len(p) > 0 {
		chunk := p
		if len(chunk) > maxSplashData {
			chunk = chunk[:maxSplashData]
		}
		pad := splashPad(len(chunk))
		rec := make([]byte, splashHeader+len(chunk)+pad)
		binary.BigEndian.PutUint16(rec[0:], uint16(len(chunk)))
		binary.BigEndian.PutUint16(rec[2:], uint16(pad))
		copy(rec[splashHeader:], chunk)
		if _, err := c.Conn.Write(rec); err != nil {
			return total, err
		}
		c.written++
		total += len(chunk)
		p = p[len(chunk):]
	}
	if len(p) == 0 {
		return total, nil
	}
	n, err := c.Conn.Write(p)
	return total + n, err
}

var errSplashRecord = errors.New("splash: malformed record")

func (c *splashConn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for {
		if c.rleft > 0 {
			if len(p) > c.rleft {
				p = p[:c.rleft]
			}
			n, err := c.Conn.Read(p)
			c.rleft -= n
			if c.rleft == 0 && err == nil {
				err = c.skipPad()
			}
			return n, err
		}
		if c.rpad > 0 {
			if err := c.skipPad(); err != nil {
				return 0, err
			}
		}
		if c.read >= splashRecords {
			return c.Conn.Read(p)
		}
		if _, err := io.ReadFull(c.Conn, c.rheadr[:]); err != nil {
			return 0, err
		}
		c.read++
		c.rleft = int(binary.BigEndian.Uint16(c.rheadr[0:]))
		c.rpad = int(binary.BigEndian.Uint16(c.rheadr[2:]))
		if c.rleft > maxSplashData {
			return 0, errSplashRecord
		}
		// A record of filler alone is allowed; read on.
	}
}

// skipPad discards the current record's filler.
func (c *splashConn) skipPad() error {
	if c.rpad == 0 {
		return nil
	}
	_, err := io.CopyN(io.Discard, c.Conn, int64(c.rpad))
	if err == nil {
		c.rpad = 0
	}
	return err
}

// splashMessage pads one message: a four-byte data length, the data, and
// filler. A message can be longer than a stream record, so its length takes
// four bytes; the filler is whatever follows the data.
func splashMessage(msg []byte) []byte {
	pad := splashPad(len(msg))
	out := make([]byte, splashHeader+len(msg)+pad)
	binary.BigEndian.PutUint32(out, uint32(len(msg)))
	copy(out[splashHeader:], msg)
	return out
}

// unsplashMessage strips what splashMessage added.
func unsplashMessage(m []byte) ([]byte, error) {
	if len(m) < splashHeader {
		return nil, errSplashRecord
	}
	n := int(binary.BigEndian.Uint32(m))
	if n > len(m)-splashHeader {
		return nil, errSplashRecord
	}
	return m[splashHeader : splashHeader+n], nil
}

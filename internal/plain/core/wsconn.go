package core

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sync"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/utils/network"
	"github.com/gorilla/websocket"
)

// WSConn is a websocket with the tunnel's encryption on its data messages
// (Backpack). Without one it is the plain websocket.
type WSConn struct {
	*websocket.Conn
	box *network.NoiseBox
	// wmu keeps sealing and writing in one order: the peer opens messages in
	// the order they were sealed.
	wmu sync.Mutex
	// splash pads the first splashRecords data messages each way; see
	// splash.go. sent and got count them.
	splash    bool
	sent, got int
	rmu       sync.Mutex
	// sbuf is the sealed message, reused: WriteMessage has written it before
	// it returns. rbuf is the message read, reused by NextReader, whose
	// caller is done with one before asking for the next.
	sbuf, rbuf []byte
}

// PlainWS is c without encryption.
func PlainWS(c *websocket.Conn) *WSConn { return &WSConn{Conn: c} }

// WSClient runs the kharej's half of the handshake over c's messages. On
// failure c is closed.
func (l *Layer) WSClient(c *websocket.Conn) (*WSConn, error) {
	return l.ws(c, true)
}

// WSServer runs the Iran server's half. On failure c is closed.
func (l *Layer) WSServer(c *websocket.Conn) (*WSConn, error) {
	return l.ws(c, false)
}

func (l *Layer) ws(c *websocket.Conn, client bool) (*WSConn, error) {
	if l == nil {
		return PlainWS(c), nil
	}
	var (
		box *network.NoiseBox
		err error
	)
	switch l.mode {
	case config.EncNone:
	case config.EncStealth:
		if client {
			box, err = network.NoiseMessageClient(c, l.token, layerHandshakeTimeout)
		} else {
			box, err = network.NoiseMessageServer(c, l.token, layerHandshakeTimeout)
		}
	case config.EncStealthPlus:
		if client {
			box, err = network.StealthPlusMessageClient(c, l.token, layerHandshakeTimeout)
		} else {
			box, err = network.StealthPlusMessageServer(c, l.token, layerHandshakeTimeout)
		}
	default:
		err = errUnknownMode
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return &WSConn{Conn: c, box: box, splash: l.splash}, nil
}

// ReadMessage returns the next data message, opened.
func (c *WSConn) ReadMessage() (int, []byte, error) {
	t, m, err := c.Conn.ReadMessage()
	if err != nil || (c.box == nil && !c.splash) {
		return t, m, err
	}
	return c.open(t, m)
}

// open strips the encryption and splash from a message read whole.
func (c *WSConn) open(t int, m []byte) (int, []byte, error) {
	var err error
	if c.box != nil {
		if m, err = c.box.Open(m); err != nil {
			return t, nil, err
		}
	}
	if c.splash {
		c.rmu.Lock()
		padded := c.got < splashRecords
		c.got++
		c.rmu.Unlock()
		if padded {
			m, err = unsplashMessage(m)
		}
	}
	return t, m, err
}

// NextReader is ReadMessage as a reader; with encryption the message is
// read whole, since it can only be opened whole.
func (c *WSConn) NextReader() (int, io.Reader, error) {
	if c.box == nil && !c.splash {
		return c.Conn.NextReader()
	}
	t, r, err := c.Conn.NextReader()
	if err != nil {
		return t, nil, err
	}
	buf := bytes.NewBuffer(c.rbuf[:0])
	if _, err := buf.ReadFrom(r); err != nil {
		return t, nil, err
	}
	c.rbuf = buf.Bytes()
	t, m, err := c.open(t, c.rbuf)
	if err != nil {
		return t, nil, err
	}
	return t, &pieces{b: m}, nil
}

// pieces hands a message out at most wsReadBufferSize at a time, the size
// gorilla's own reader hands a streamed one out in. A whole 64 KiB message in
// one Read becomes one 64 KiB write to the far side's socket, and the plain engine
// gives a loopback target a 32 KiB send buffer: on Linux that write stalls
// for a delayed ACK every time, which held a tunnel to 4 MB/s.
type pieces struct{ b []byte }

func (p *pieces) Read(q []byte) (int, error) {
	if len(p.b) == 0 {
		return 0, io.EOF
	}
	if len(q) > wsReadBufferSize {
		q = q[:wsReadBufferSize]
	}
	n := copy(q, p.b)
	p.b = p.b[n:]
	return n, nil
}

// WriteMessage seals data and sends it as one message.
func (c *WSConn) WriteMessage(t int, data []byte) error {
	if c.box == nil && !c.splash {
		return c.Conn.WriteMessage(t, data)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.splash && c.sent < splashRecords {
		c.sent++
		data = splashMessage(data)
	}
	if c.box == nil {
		return c.Conn.WriteMessage(websocket.BinaryMessage, data)
	}
	sealed, err := c.box.SealTo(c.sbuf, data)
	if err != nil {
		return err
	}
	c.sbuf = sealed
	// Sealed bytes are binary whatever the plaintext was.
	return c.Conn.WriteMessage(websocket.BinaryMessage, sealed)
}

// Bearer is what the websocket upgrade carries as its Authorization bearer.
// The plain wire sends the token itself; on a plain ws that is the token in the
// clear, which would hand an observer the key the encryption is made from.
// With an encryption it is a value derived from the token instead: it still
// tells the server the request is the tunnel's, and reveals nothing.
func (l *Layer) Bearer(token string) string {
	if l == nil {
		return token
	}
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte("backpack-encryption-bearer-v1"))
	return hex.EncodeToString(m.Sum(nil))
}

// BearerOK reports whether an Authorization header carries this tunnel's
// bearer.
func (l *Layer) BearerOK(header, token string) bool {
	want := "Bearer " + l.Bearer(token)
	if l == nil {
		return header == want
	}
	return hmac.Equal([]byte(header), []byte(want))
}

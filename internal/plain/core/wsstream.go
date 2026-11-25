package core

import (
	"io"
	"net"
	"time"

	"github.com/gorilla/websocket"
)

// WSStream is c as a byte stream (Backpack): every Write one binary message,
// Read the messages' data in order. A UDP flow over a ws tunnel uses it, so
// the flow's framing is the one it has over tcp.
func WSStream(c *WSConn) net.Conn { return &wsStream{WSConn: c} }

type wsStream struct {
	*WSConn
	r io.Reader
}

func (s *wsStream) Read(p []byte) (int, error) {
	for {
		if s.r != nil {
			n, err := s.r.Read(p)
			if err == io.EOF {
				s.r = nil
				if n == 0 {
					continue
				}
				err = nil
			}
			return n, err
		}
		_, r, err := s.WSConn.NextReader()
		if err != nil {
			return 0, err
		}
		s.r = r
	}
}

func (s *wsStream) Write(p []byte) (int, error) {
	if err := s.WSConn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *wsStream) SetDeadline(t time.Time) error {
	if err := s.WSConn.SetReadDeadline(t); err != nil {
		return err
	}
	return s.WSConn.SetWriteDeadline(t)
}

package transport

import (
	"bufio"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
)

// oneShot upgrades exactly one connection to a websocket, so a test can have a
// real pair without standing up an http.Server for it.
type oneShot struct {
	conn net.Conn
	up   websocket.Upgrader
	out  chan *websocket.Conn
}

func (o *oneShot) serve() {
	req, err := http.ReadRequest(bufio.NewReader(o.conn))
	if err != nil {
		o.out <- nil
		return
	}
	w := &hijackWriter{conn: o.conn, header: http.Header{}}
	c, err := o.up.Upgrade(w, req, nil)
	if err != nil {
		o.out <- nil
		return
	}
	o.out <- c
}

// hijackWriter is the smallest ResponseWriter the upgrader will accept.
type hijackWriter struct {
	conn   net.Conn
	header http.Header
}

func (h *hijackWriter) Header() http.Header         { return h.header }
func (h *hijackWriter) Write(b []byte) (int, error) { return h.conn.Write(b) }
func (h *hijackWriter) WriteHeader(int)             {}
func (h *hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn)), nil
}

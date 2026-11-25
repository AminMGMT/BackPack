package core

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/web"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// TCPConnectionHandler relays between from and to until either side ends.
// With proxyProtocol, a PROXY v2 header naming from's peer goes first.
//
// A 16 KiB relay loop. A 64 KiB pooled buffer was tried and kept off:
// on a plain tcp tunnel it held the upload to 60 Mbit/s where this carries
// 810 (soak, 100 ms path, BBR) — the kharej side's loopback connection to the
// service has the 32 KiB buffers the plain engine pins there, and reads larger than
// that stalled it.
func TCPConnectionHandler(ctx context.Context, proxyProtocol bool, from net.Conn, to net.Conn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) {
	done := make(chan struct{})

	if proxyProtocol {
		err := handlers.WriteProxyProtocol(from, to)
		if err != nil {
			logger.Error(err)
			from.Close()
			to.Close()
			return
		}
	}

	go func() {
		defer close(done)
		transferData(from, to, logger, usage, remotePort, sniffer)
	}()

	transferData(to, from, logger, usage, remotePort, sniffer)

	select {
	case <-ctx.Done():
		from.Close()
		to.Close()
		return
	case <-done:
	}
}

func transferData(from net.Conn, to net.Conn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) {
	buf := make([]byte, 16*1024) // 16K
	for {
		r, err := from.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				logger.Trace("reader stream closed or EOF received")
			} else {
				logger.Trace("unable to read from the connection: ", err)
			}
			from.Close()
			to.Close()
			return
		}

		totalWritten := 0
		for totalWritten < r {
			w, err := to.Write(buf[totalWritten:r])
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					logger.Trace("writer stream closed or EOF received")
				} else {
					logger.Trace("unable to write to the connection: ", err)
				}
				from.Close()
				to.Close()
				return
			}
			totalWritten += w
		}

		logger.Tracef("read data: %d bytes, written data: %d bytes", r, totalWritten)
		if sniffer {
			usage.AddOrUpdatePort(remotePort, uint64(totalWritten))
		}
	}
}

// The websocket buffers. A write buffer is in use only while a message is
// being written, so they come from one pool rather than one per connection —
// plain ws is a websocket per user. 64 KiB is the relay's read size, so a
// relayed chunk leaves as one frame and one system call instead of the four
// the plain server's 16 KiB and gorilla's 4 KiB default (client) took.
const (
	wsReadBufferSize  = 16 * 1024
	wsWriteBufferSize = 64 * 1024
	wsRelayBufferSize = 64 * 1024
)

// WSWriteBuffers is the shared write buffer pool for both ends' websockets.
var WSWriteBuffers = &sync.Pool{}

var wsRelayBuffers = sync.Pool{New: func() any {
	b := make([]byte, wsRelayBufferSize)
	return &b
}}

// WSConnectionHandler relays between a websocket and a TCP connection.
func WSConnectionHandler(ctx context.Context, wsConn *WSConn, tcpConn net.Conn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { wsConn.Close(); tcpConn.Close() })
	defer stop()

	go func() {
		defer close(done)
		transferWebSocketToTCP(wsConn, tcpConn, logger, usage, remotePort, sniffer)
	}()

	transferTCPToWebSocket(tcpConn, wsConn, logger, usage, remotePort, sniffer)

	<-done
}

func transferWebSocketToTCP(wsConn *WSConn, tcpConn net.Conn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) {
	bufp := wsRelayBuffers.Get().(*[]byte)
	defer wsRelayBuffers.Put(bufp)
	// Each message is streamed through the buffer rather than read whole into
	// a fresh allocation, which is what ReadMessage did for every message.
	reader, writer := &readerOnly{}, writerOnly{tcpConn}
	for {
		messageType, message, err := wsConn.NextReader()
		if err != nil {
			if errors.Is(err, websocket.ErrCloseSent) || errors.Is(err, io.EOF) {
				logger.Trace("WebSocket reader stream closed or EOF received")
			} else {
				logger.Trace("unable to read from the WebSocket connection: ", err)
			}
			wsConn.Close()
			tcpConn.Close()
			return
		}

		// Only text or binary messages (control messages like pings are not data).
		if messageType == websocket.TextMessage || messageType == websocket.BinaryMessage {
			reader.r = message
			n, err := io.CopyBuffer(writer, reader, *bufp)
			if n > 0 {
				// Backpack: the bytes arrived over the tunnel.
				countIn(int(n))
				if sniffer {
					usage.AddOrUpdatePort(remotePort, uint64(n))
				}
			}
			if err != nil {
				logger.Trace("unable to write to the TCP connection: ", err)
				wsConn.Close()
				tcpConn.Close()
				return
			}
			logger.Tracef("transferred data from WebSocket to TCP: %d bytes", n)
		}
	}
}

func transferTCPToWebSocket(tcpConn net.Conn, wsConn *WSConn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) {
	bufp := wsRelayBuffers.Get().(*[]byte)
	defer wsRelayBuffers.Put(bufp)
	buf := *bufp
	for {
		n, readErr := tcpConn.Read(buf)
		if n > 0 {
			if err := wsConn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				if errors.Is(err, websocket.ErrCloseSent) || errors.Is(err, io.EOF) {
					logger.Trace("WebSocket writer stream closed or EOF received")
				} else {
					logger.Trace("unable to write to the WebSocket connection: ", err)
				}
				tcpConn.Close()
				wsConn.Close()
				return
			}
			// Backpack: the bytes left over the tunnel.
			countOut(n)
			logger.Tracef("transferred data from TCP to WebSocket: %d bytes", n)
			if sniffer {
				usage.AddOrUpdatePort(remotePort, uint64(n))
			}
		}
		// The bytes read with an error still went out first.
		if readErr != nil {
			if errors.Is(readErr, io.EOF) || errors.Is(readErr, net.ErrClosed) {
				logger.Trace("TCP reader stream closed or EOF received")
			} else {
				logger.Trace("unable to read from the TCP connection: ", readErr)
			}
			tcpConn.Close()
			wsConn.Close()
			return
		}
	}
}

// readerOnly and writerOnly hide ReadFrom/WriteTo, so io.CopyBuffer uses the
// pooled buffer instead of allocating one of its own.
type readerOnly struct{ r io.Reader }

func (r *readerOnly) Read(p []byte) (int, error) { return r.r.Read(p) }

type writerOnly struct{ w io.Writer }

func (w writerOnly) Write(p []byte) (int, error) { return w.w.Write(p) }

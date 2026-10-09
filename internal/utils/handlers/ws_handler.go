package handlers

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/web"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// WSConnectionHandler relays between a websocket tunnel connection and a TCP
// connection to the backend, until either side ends.
func WSConnectionHandler(ctx context.Context, wsConn *websocket.Conn, tcpConn net.Conn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) {
	// Two independent copies, for the reason given in TCPConnectionHandler: a
	// ReadMessage on an idle websocket blocks exactly as long as an idle TCP
	// Read does, and cancellation has to be able to interrupt it.
	done := make(chan struct{}, 2)
	go func() {
		transferWebSocketToTCP(wsConn, tcpConn, logger, usage, remotePort, sniffer)
		done <- struct{}{}
	}()
	go func() {
		transferTCPToWebSocket(tcpConn, wsConn, logger, usage, remotePort, sniffer)
		done <- struct{}{}
	}()

	awaitRelay(ctx, done, wsConn, tcpConn)
}

// transferWebSocketToTCP transfers data from a WebSocket connection to a TCP connection
func transferWebSocketToTCP(wsConn *websocket.Conn, tcpConn net.Conn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) {
	// Each message is copied through the pooled relay buffer rather than read
	// whole: ReadMessage allocated a buffer the size of every message, which
	// on a busy tunnel was a fifth of the relay's CPU in the collector.
	// Keep the capability-hiding adapters for the whole relay: boxing new
	// adapters for every message allocates twice on the receive hot path.
	reader, writer := &readerOnly{}, &writerOnly{tcpConn}
	bufp := getRelayBuffer()
	defer putRelayBuffer(bufp)
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

		// Only handle text or binary messages (ignore control messages like pings)
		if messageType == websocket.TextMessage || messageType == websocket.BinaryMessage {
			reader.r = message
			n, err := io.CopyBuffer(writer, reader, *bufp)
			w := int(n)
			if err != nil {
				logger.Trace("unable to relay a WebSocket message to the TCP connection: ", err)
				wsConn.Close()
				tcpConn.Close()
				return
			}
			// Arrived over the tunnel.
			metrics.AddBytes(uint64(w), 0)
			if logger.IsLevelEnabled(logrus.TraceLevel) {
				logger.Tracef("transferred data from WebSocket to TCP: %d bytes", w)
			}
			if sniffer {
				usage.AddOrUpdatePort(remotePort, uint64(w))
			}
		}
	}
}

// transferTCPToWebSocket transfers data from a TCP connection to a WebSocket connection
func transferTCPToWebSocket(tcpConn net.Conn, wsConn *websocket.Conn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) {
	// The same pooled 64 KiB buffer the TCP relay copies through, for the same
	// two reasons (see relaybuf.go): a relay's cost is syscalls, and 16 KiB took
	// four times as many of them per gigabyte as 64 KiB does; and allocating it
	// per connection put a 16 KiB slice through the garbage collector for every
	// connection forwarded. This path was simply missed when the TCP one was
	// fixed.
	bufp := getRelayBuffer()
	defer putRelayBuffer(bufp)
	buf := *bufp
	for {
		// Reader bytes come before its error, including a final read with EOF.
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

			// Leaving over the tunnel, after the message was written successfully.
			metrics.AddBytes(0, uint64(n))
			logger.Tracef("transferred data from TCP to WebSocket: %d bytes", n)
			if sniffer {
				usage.AddOrUpdatePort(remotePort, uint64(n))
			}
		}
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

// readerOnly and writerOnly hide everything but Read and Write, so
// io.CopyBuffer uses the buffer it is given. Left visible, a *net.TCPConn's
// ReadFrom takes over the copy and, for a source that is not a socket or a
// file, allocates a fresh buffer of its own on every message.
type readerOnly struct{ r io.Reader }

func (r readerOnly) Read(p []byte) (int, error) { return r.r.Read(p) }

type writerOnly struct{ w io.Writer }

func (w writerOnly) Write(p []byte) (int, error) { return w.w.Write(p) }

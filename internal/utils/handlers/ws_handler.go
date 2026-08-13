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

// WebSocketToTCPConnectionHandler handles data transfer between a WebSocket and a TCP connection
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
	for {
		// Read message from the WebSocket connection
		messageType, message, err := wsConn.ReadMessage()
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
			// Write the message to the TCP connection
			w, err := tcpConn.Write(message)
			if err != nil {
				logger.Trace("unable to write to the TCP connection: ", err)
				wsConn.Close()
				tcpConn.Close()
				return
			}
			// Arrived over the tunnel.
			metrics.AddBytes(uint64(w), 0)
			logger.Tracef("transferred data from WebSocket to TCP: %d bytes", w)
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
		// Read data from the TCP connection
		n, err := tcpConn.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				logger.Trace("TCP reader stream closed or EOF received")
			} else {
				logger.Trace("unable to read from the TCP connection: ", err)
			}
			tcpConn.Close()
			wsConn.Close()
			return
		}

		// Write the data to the WebSocket connection as a binary message
		err = wsConn.WriteMessage(websocket.BinaryMessage, buf[:n])
		if err != nil {
			if errors.Is(err, websocket.ErrCloseSent) || errors.Is(err, io.EOF) {
				logger.Trace("WebSocket writer stream closed or EOF received")
			} else {
				logger.Trace("unable to write to the WebSocket connection: ", err)
			}
			tcpConn.Close()
			wsConn.Close()
			return
		}

		// Leaving over the tunnel.
		metrics.AddBytes(0, uint64(n))
		logger.Tracef("transferred data from TCP to WebSocket: %d bytes", n)
		if sniffer {
			usage.AddOrUpdatePort(remotePort, uint64(n))
		}
	}
}

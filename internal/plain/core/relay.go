package core

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/backpack/backpack/internal/utils/handlers"
	"github.com/backpack/backpack/internal/web"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// TCPConnectionHandler relays between from and to until either side ends.
// With proxyProtocol, a PROXY v2 header naming from's peer goes first.
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

// WSConnectionHandler relays between a websocket and a TCP connection.
func WSConnectionHandler(ctx context.Context, wsConn *websocket.Conn, tcpConn net.Conn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) {
	done := make(chan struct{})

	go func() {
		defer close(done)
		transferWebSocketToTCP(wsConn, tcpConn, logger, usage, remotePort, sniffer)
	}()

	transferTCPToWebSocket(tcpConn, wsConn, logger, usage, remotePort, sniffer)

	select {
	case <-ctx.Done():
		wsConn.Close()
		tcpConn.Close()
		return
	case <-done:
	}
}

func transferWebSocketToTCP(wsConn *websocket.Conn, tcpConn net.Conn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) {
	for {
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

		// Only text or binary messages (control messages like pings are not data).
		if messageType == websocket.TextMessage || messageType == websocket.BinaryMessage {
			w, err := tcpConn.Write(message)
			if err != nil {
				logger.Trace("unable to write to the TCP connection: ", err)
				wsConn.Close()
				tcpConn.Close()
				return
			}
			// Backpack: the bytes arrived over the tunnel.
			countIn(w)
			logger.Tracef("transferred data from WebSocket to TCP: %d bytes", w)
			if sniffer {
				usage.AddOrUpdatePort(remotePort, uint64(w))
			}
		}
	}
}

func transferTCPToWebSocket(tcpConn net.Conn, wsConn *websocket.Conn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) {
	buf := make([]byte, 16*1024) // 16K buffer size
	for {
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

		// Backpack: the bytes left over the tunnel.
		countOut(n)
		logger.Tracef("transferred data from TCP to WebSocket: %d bytes", n)
		if sniffer {
			usage.AddOrUpdatePort(remotePort, uint64(n))
		}
	}
}

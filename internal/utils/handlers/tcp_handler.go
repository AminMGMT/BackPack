package handlers

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/web"
	"github.com/sirupsen/logrus"
)

func TCPConnectionHandler(ctx context.Context, proxyProtocol bool, from net.Conn, to net.Conn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) {
	done := make(chan struct{})

	// Write Proxy Protocol V2 Header
	if proxyProtocol {
		err := WriteProxyProtocol(from, to)
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

// canSplice reports whether both sides of a relay are plain TCP sockets, which
// is the only case where the kernel can move the bytes by itself.
//
// The traffic counter is looked through rather than rejected: it wraps the
// tunnel side of every relay, so refusing it would mean the fast path never
// applied anywhere. Every other wrapper — a bandwidth cap, a mux stream, an
// encrypted session — is a real reason to stay on the buffered loop, and fails
// this check by simply not being a TCP socket.
func canSplice(from, to net.Conn) bool {
	_, fromTCP := metrics.Unwrap(from).(*net.TCPConn)
	_, toTCP := metrics.Unwrap(to).(*net.TCPConn)
	return fromTCP && toTCP
}

// transferData moves everything from one side of a forwarded connection to the
// other and closes both when it stops.
func transferData(from net.Conn, to net.Conn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) {
	// Zero-copy path.
	//
	// The loop below reads each chunk into a buffer in this process only to
	// write it straight back out — two crossings of the kernel boundary for
	// bytes nothing here ever looks at. Between two TCP sockets on Linux the
	// kernel can do it alone with splice(2), moving the bytes without ever
	// mapping them into user memory, and io.Copy asks for that automatically.
	//
	// Nothing on the wire changes: same bytes, same order, same socket, same
	// options. It is a shortcut inside this machine, not a protocol change,
	// and nothing about how the tunnel looks from outside depends on it.
	//
	// Two things deliberately stay on the old path:
	//
	//   - The sniffer, which has to see each chunk to attribute it to a port.
	//   - Anything that is not a plain pair of TCP sockets. A mux stream, a
	//     Noise or KCP session, a WebSocket, or a bandwidth-capped connection
	//     cannot be spliced, and a rate-limited one must not be — its cap is
	//     enforced by pacing reads and writes that splice would skip straight
	//     past. Checking first means every one of those transports keeps
	//     running exactly the code it runs today.
	if !sniffer && canSplice(from, to) {
		if _, err := io.Copy(to, from); err != nil {
			logger.Trace("unable to relay the connection: ", err)
		}
		from.Close()
		to.Close()
		return
	}

	buf := make([]byte, 16*1024) // 16K
	for {
		// Read data from the source connection
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
			// Write data to the destination connection
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

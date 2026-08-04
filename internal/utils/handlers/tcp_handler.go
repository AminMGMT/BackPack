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

// transferData moves one direction of a forwarded connection.
//
// It prefers the zero-copy path, which needs a real socket at each end: the
// counting wrapper is unwrapped for it (the loop below counts instead), but a
// bandwidth-limited connection is not, because pacing bytes means looking at
// them. Anything that is not two plain TCP sockets — a mux stream, a KCP
// session, a websocket, a rate-limited conn, or any platform that is not
// Linux — falls through to the buffered copy, which is also where the fast
// path lands if the kernel declines it.
func transferData(from net.Conn, to net.Conn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) {
	if spliceTransfer(from, to, logger, usage, remotePort, sniffer) {
		countSpliced()
		return
	}
	countBuffered()

	bufp := getRelayBuffer()
	defer putRelayBuffer(bufp)
	buf := *bufp
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

// spliceTransfer runs one direction through the kernel when both ends are plain
// TCP sockets, and reports whether it did. A false return means nothing was
// moved and the caller must copy the stream itself.
func spliceTransfer(from net.Conn, to net.Conn, logger *logrus.Logger, usage *web.Usage, remotePort int, sniffer bool) bool {
	// Off unless the operator asked for it. See zerocopy.go for why the
	// default is the slower path.
	if !ZeroCopy() {
		return false
	}

	// Unwrapping the counter is safe because onChunk below counts exactly what
	// it would have. Unwrapping anything else would not be — a bandwidth cap
	// disappears if the bytes stop passing through it — so nothing else is
	// unwrapped, and a limited connection simply is not a *net.TCPConn here.
	fromConn, fromCounted := metrics.Uncount(from)
	toConn, toCounted := metrics.Uncount(to)

	src, ok := fromConn.(*net.TCPConn)
	if !ok {
		return false
	}
	dst, ok := toConn.(*net.TCPConn)
	if !ok {
		return false
	}

	// CountedConn is applied to the tunnel side only, so at most one of these
	// is true and the same bytes are never counted twice.
	onChunk := func(n int) {
		switch {
		case fromCounted:
			metrics.AddBytes(uint64(n), 0)
		case toCounted:
			metrics.AddBytes(0, uint64(n))
		}
		if sniffer {
			usage.AddOrUpdatePort(remotePort, uint64(n))
		}
	}

	handled, err := spliceRelay(dst, src, onChunk)
	if !handled {
		return false
	}
	if err != nil && !errors.Is(err, net.ErrClosed) {
		logger.Trace("zero-copy transfer ended: ", err)
	}

	from.Close()
	to.Close()
	return true
}

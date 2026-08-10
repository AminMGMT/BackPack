package transport

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/utils/network"
	"github.com/backpack/backpack/internal/web"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// The client end of a forwarded UDP flow: read framed datagrams off the tunnel,
// send them to the backend, and send its replies back the same way.
//
// One socket per flow, and the flow is the tunnel connection — when that ends,
// this ends. The implementation this replaces never managed that: it read from
// the backend socket with no deadline and nothing ever closed it, so every
// forwarded UDP flow left a goroutine and a file descriptor behind for the life
// of the process. A client that had carried a few thousand flows ran out of
// descriptors and stopped accepting anything at all, which looks exactly like
// "UDP stops working after a while".

// udpBackendIdle bounds a flow whose tunnel side has gone quiet without
// closing. It matches the server's mapping lifetime, so the two ends give up on
// a silent flow at about the same time.
const udpBackendIdle = 60 * time.Second

// dialForwardedUDP takes a flow whose target is marked as UDP, reporting
// whether it was one. Every transport calls it before resolving the target the
// ordinary way, so recognising a datagram flow is one thing in one place.
func dialForwardedUDP(stream net.Conn, remoteAddr string, logger *logrus.Logger, usage *web.Usage, sniffer bool) bool {
	target, isUDP := network.SplitUDPTarget(remoteAddr)
	if !isUDP {
		return false
	}
	port, resolved, err := network.ResolveRemoteAddr(target)
	if err != nil {
		logger.Infof("failed to resolve the UDP target %q: %v", target, err)
		stream.Close()
		return true
	}
	UDPForward(stream, resolved, logger, usage, port, sniffer)
	return true
}

// UDPForward pipes a tunnel stream carrying framed datagrams to a UDP backend.
// It returns when either side ends, having closed both.
func UDPForward(stream net.Conn, target string, logger *logrus.Logger, usage *web.Usage, port int, sniffer bool) {
	// A UDP backend cannot be health-checked the way a TCP one is — there is
	// nothing to connect to — so a multi-backend list takes its first entry
	// rather than being handed to the resolver as one nonsense address.
	target = firstUDPBackend(target)

	addr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		logger.Errorf("failed to resolve UDP target %q: %v", target, err)
		stream.Close()
		return
	}
	backend, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		logger.Errorf("failed to dial UDP backend %q: %v", target, err)
		stream.Close()
		return
	}

	logger.Debugf("forwarding UDP to %s", target)

	// Both directions end together. Whichever finishes first closes the pair,
	// which unblocks the other out of its read.
	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			stream.Close()
			backend.Close()
		})
	}
	defer shutdown()

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer shutdown()
		backendToTunnel(stream, backend, logger, usage, port, sniffer)
	}()

	tunnelToBackend(stream, backend, logger, usage, port, sniffer)
	shutdown()
	<-done
}

// tunnelToBackend unpacks datagrams from the tunnel and sends them on.
func tunnelToBackend(stream net.Conn, backend *net.UDPConn, logger *logrus.Logger, usage *web.Usage, port int, sniffer bool) {
	buf := make([]byte, network.MaxDatagram)
	for {
		n, err := network.ReadDatagram(stream, buf)
		if err != nil {
			logger.Tracef("UDP flow to %s ended: %v", backend.RemoteAddr(), err)
			return
		}
		if _, err := backend.Write(buf[:n]); err != nil {
			logger.Debugf("failed to write to UDP backend %s: %v", backend.RemoteAddr(), err)
			return
		}
		if sniffer {
			usage.AddOrUpdatePort(port, uint64(n))
		}
	}
}

// backendToTunnel frames the backend's replies and writes them to the tunnel.
func backendToTunnel(stream net.Conn, backend *net.UDPConn, logger *logrus.Logger, usage *web.Usage, port int, sniffer bool) {
	buf := make([]byte, network.MaxDatagram)
	for {
		// The deadline is what ends a flow the far side abandoned without
		// closing the stream — otherwise this read would block forever on a
		// socket nobody will ever send to again.
		_ = backend.SetReadDeadline(time.Now().Add(udpBackendIdle))
		n, err := backend.Read(buf)
		if err != nil {
			logger.Tracef("UDP backend %s ended: %v", backend.RemoteAddr(), err)
			return
		}
		if err := network.WriteDatagram(stream, buf[:n]); err != nil {
			logger.Debugf("failed to write a datagram to the tunnel: %v", err)
			return
		}
		if sniffer {
			usage.AddOrUpdatePort(port, uint64(n))
		}
	}
}

// wsStream presents a websocket connection as a net.Conn.
//
// The plain websocket transport is the one that does not already hand its
// forwarded connections over as a stream: it speaks in messages. Datagram
// framing has to survive that anyway — the server writes frames into a relay
// that chops them into messages wherever it likes — so the messages are read
// back as one continuous stream and the frames are found in it, rather than
// each message being trusted to hold exactly one datagram.
type wsStream struct {
	conn *websocket.Conn
	r    io.Reader
}

func (w *wsStream) Read(p []byte) (int, error) {
	for {
		if w.r == nil {
			typ, r, err := w.conn.NextReader()
			if err != nil {
				return 0, err
			}
			if typ != websocket.BinaryMessage && typ != websocket.TextMessage {
				continue // a control frame carries no payload of ours
			}
			w.r = r
		}
		n, err := w.r.Read(p)
		if err == io.EOF {
			w.r = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (w *wsStream) Write(p []byte) (int, error) {
	if err := w.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *wsStream) Close() error                       { return w.conn.Close() }
func (w *wsStream) LocalAddr() net.Addr                { return w.conn.LocalAddr() }
func (w *wsStream) RemoteAddr() net.Addr               { return w.conn.RemoteAddr() }
func (w *wsStream) SetDeadline(t time.Time) error      { return w.conn.SetReadDeadline(t) }
func (w *wsStream) SetReadDeadline(t time.Time) error  { return w.conn.SetReadDeadline(t) }
func (w *wsStream) SetWriteDeadline(t time.Time) error { return w.conn.SetWriteDeadline(t) }

// firstUDPBackend takes the first entry of a "a|b" backend list.
func firstUDPBackend(addr string) string {
	for i := 0; i < len(addr); i++ {
		if addr[i] == '|' {
			return addr[:i]
		}
	}
	return addr
}

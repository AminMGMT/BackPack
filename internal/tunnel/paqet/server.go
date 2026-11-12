package paqet

// Derived from paqet's internal/server (MIT); see doc.go.

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/backpack/backpack/internal/tunnel/bridge"
	"github.com/xtaci/smux"
)

func isUnreachable(err error) bool {
	return errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETDOWN) || errors.Is(err, syscall.EHOSTDOWN)
}

// serveTCP dials the target the stream names and joins the two.
func (g *generation) serveTCP(st *smux.Stream, target Addr) {
	e := g.e
	d := net.Dialer{Timeout: dialTimeout}
	ctx, cancel := context.WithCancel(g.ctx)
	defer cancel()
	conn, err := d.DialContext(ctx, "tcp", target.String())
	if err != nil {
		e.noteRefused(&session{remote: st.RemoteAddr()}, target, err)
		st.Write([]byte{statusFor(err)})
		return
	}
	if _, err := st.Write([]byte{statusOK}); err != nil {
		conn.Close()
		return
	}
	e.log.Debugf("pck: stream %d from %s -> %s", st.ID(), st.RemoteAddr(), target)
	bridge.Join(ctx, conn, newFramedStream(st))
}

// serveUDP relays one UDP flow: datagrams from the client on a connected UDP
// socket to the target, and what the target answers back to the client, on the
// datagram lane when it fits a segment and in the stream when it does not.
func (g *generation) serveUDP(s *session, st *smux.Stream, target Addr) {
	e := g.e
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(g.ctx, "udp", target.String())
	if err != nil {
		e.noteRefused(&session{remote: st.RemoteAddr()}, target, err)
		st.Write([]byte{statusFor(err)})
		return
	}
	defer conn.Close()
	// The flow is known to the lane before the client is told the target is
	// reachable, so the first datagram it sends on the lane has somewhere to go.
	flow := newDGFlow()
	if s.lane != nil {
		s.lane.mux.add(st.ID(), flow)
		defer s.lane.mux.remove(st.ID())
	}
	if _, err := st.Write([]byte{statusOK}); err != nil {
		return
	}
	e.log.Debugf("pck: udp stream %d from %s -> %s", st.ID(), st.RemoteAddr(), target)

	// When a datagram last crossed, either way. A flow ends after udpRelayIdle of
	// silence in both directions, not in one: a flow whose target never answers
	// (a log or metrics sink) is still a flow while the client sends.
	var heard atomic.Int64
	heard.Store(time.Now().UnixNano())
	done := make(chan struct{}, 2)
	// The stream's own datagrams (the ones too large for the lane, and the
	// first, sent before the client had the verdict) join the lane's.
	go pumpFrames(st, flow)
	// client -> target
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, maxDatagram)
		for {
			n, err := flow.read(buf, time.Time{})
			if err != nil {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(udpRelayIdle))
			if _, err := conn.Write(buf[:n]); err != nil {
				return
			}
			heard.Store(time.Now().UnixNano())
		}
	}()
	// target -> client. The read of the target's socket never waits on the
	// tunnel: the lane's send is a segment on the wire, and the stream's is
	// queued (see datagramsend.go).
	out := newDatagramSender(st, &e.udpDrops)
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, maxDatagram)
		for {
			conn.SetReadDeadline(time.Now().Add(udpRelayIdle))
			n, err := conn.Read(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() &&
					time.Since(time.Unix(0, heard.Load())) < udpRelayIdle {
					continue // quiet this way, not the other
				}
				return
			}
			heard.Store(time.Now().UnixNano())
			if s.lane.send(st.ID(), buf[:n]) {
				continue
			}
			if err := out.send(buf[:n]); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-g.ctx.Done():
	}
	out.close()
	flow.fail(net.ErrClosed)
	st.Close()
	conn.Close()
	<-done
}

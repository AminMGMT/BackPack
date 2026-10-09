package transport

import (
	"context"
	"net"
	"time"

	"github.com/backpack/backpack/internal/controlwire"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/utils"
)

// The udp transport's control port: TCP, even though its data is not. Claims
// are judged side by side under the pre-authentication gate; see ADR 0004.

func (s *UdpTransport) channelHandshake(g *udpGen) {
	// This transport's control channel is TCP even though its data is not, so
	// this goroutine holds a listener too and Start has to wait for it. Counted
	// here rather than in tunnelListener, which owns the UDP half.
	s.listeners.hold()
	defer s.listeners.release()

	// The tunnel's own control port: retried rather than fatal. See bindfail.go.
	// Named apart from the acceptBackoff further down — that one paces a
	// spinning accept loop in milliseconds, this one waits seconds for another
	// process to let go of a port.
	listener, ok := bindTunnelPort(g.ctx, s.logger, s.config.BindAddr, func() (net.Listener, error) {
		return net.Listen("tcp", s.config.BindAddr)
	})
	if !ok {
		return
	}

	s.logger.Infof("server started successfully, listening on address: %s", listener.Addr().String())

	defer listener.Close()

	// Close the listener when the run ends so the blocked Accept below returns
	// instead of holding this goroutine open past the restart that replaced it.
	go func() {
		<-g.ctx.Done()
		listener.Close()
	}()

	// This listener keeps accepting for the whole run. The first valid claim
	// becomes the client and starts the data side; a later one is that client
	// re-dialing — often before this side noticed its old connection was dead —
	// and takes the seat in place, so the data side and its users stay up. It
	// used to rebuild the whole run. See clientSeat.
	//
	// Claims are judged side by side, each in its own goroutine under the
	// pre-authentication budget, and only the verdicts come back here in
	// order. Judged on the accept loop, one silent connection held every
	// claim behind it for the whole of its deadline — fifteen seconds of the
	// genuine client waiting for each connection a stranger cared to open.
	claims := make(chan net.Conn)
	go s.acceptControlClaims(g, listener, claims)

	for {
		var conn net.Conn
		select {
		case <-g.ctx.Done():
			return
		case conn = <-claims:
		}

		if g.seat.serving() {
			s.logger.Warn("a new control channel claim arrived; adopting the new client in place")
			s.seatClient(g, conn)
			continue
		}
		s.seatClient(g, conn)
		go s.tunnelListener(g)
		go s.parsePortMappings(g)
	}
}

// seatClient makes conn this generation's client, in place of whoever was.
func (s *UdpTransport) seatClient(g *udpGen, conn net.Conn) {
	g.seat.sit(g.ctx,
		func() { s.vacate(g) },
		func() {
			// A dead client that never sends FIN/RST — a hard kill, or a path
			// that blackholes under load — would otherwise sit here as a
			// zombie. Keepalive probes turn that into a read error the control
			// loop can act on.
			enableKeepAlive(conn, 30*time.Second)
			s.controlChannel.Set(conn)
			// The engine says whether it holds a control channel; the watchdog
			// reads it rather than the socket table. See
			// metrics.Snapshot.Connected.
			s.seated(conn.RemoteAddr().String())
			s.status.set("Connected (UDP)")
			s.logger.Info("control channel successfully established.")
		},
		func(ctx context.Context, lost func()) {
			loop := s.control(g, ctx, lost)
			// Bind to this claim even if its goroutine starts after replacement.
			loop.link = controlwire.Net(conn)
			loop.run()
		})
}

// vacate empties the seat: the client's channel is closed and forgotten, and
// the tunnel flows it opened that wait in the pool are dropped. The data
// socket and the forwarded ports stay up for the next client.
func (s *UdpTransport) vacate(g *udpGen) {
	s.controlChannel.Close()
	s.controlChannel.Clear()
	// Flows on the shared data socket: nothing to close, only to stop handing
	// out.
	for drained := false; !drained; {
		select {
		case <-g.tunnelChannel:
		default:
			drained = true
		}
	}
	s.status.set("Disconnected (UDP)")
	metrics.ClearPeer()
}

// acceptControlClaims accepts on the control port for the whole run and judges
// each claim in its own goroutine, handing the valid ones to claims.
func (s *UdpTransport) acceptControlClaims(g *udpGen, listener net.Listener, claims chan<- net.Conn) {
	var backoff acceptBackoff
	for {
		conn, err := listener.Accept()
		if err != nil {
			if g.ctx.Err() != nil {
				return
			}
			s.logger.Debugf("failed to accept control channel connection on %s: %v", listener.Addr(), err)
			// The context check above catches a shutdown, but a listener broken
			// for any other reason fails instantly and forever; without a pause
			// this loop would spin on a core. See acceptBackoff.
			if !backoff.Fail(g.ctx) {
				return
			}
			continue
		}
		backoff.OK()

		leave, ok := s.preauth.Admit(conn, s.logger.Debugf)
		if !ok {
			continue
		}
		go func() {
			valid := s.validControlClaim(g, conn)
			leave()
			if !valid {
				conn.Close()
				return
			}
			select {
			case claims <- conn:
			case <-g.ctx.Done():
				conn.Close()
			}
		}()
	}
}

// validControlClaim reads the token handshake a control-channel claimant must
// pass and answers it. It leaves the connection open on success; the caller
// closes it when this returns false.
func (s *UdpTransport) validControlClaim(g *udpGen, conn net.Conn) bool {
	// Set a read deadline for the token response
	if err := conn.SetReadDeadline(time.Now().Add(controlClaimTimeout)); err != nil {
		s.logger.Errorf("failed to set read deadline: %v", err)
		return false
	}

	msg, transport, err := utils.ReceiveBinaryTransportString(conn)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			s.logger.Warn("timeout while waiting for control channel signal")
		} else {
			s.logger.Errorf("failed to receive control channel signal: %v", err)
		}
		return false
	}

	if transport != utils.SG_Chan {
		s.logger.Errorf("invalid signal received for channel, discarding connection")
		return false
	}

	// Resetting the deadline (removes any existing deadline)
	conn.SetReadDeadline(time.Time{})

	if !tokenMatches(msg, s.config.Token) {
		s.logger.Warnf("invalid security token received")
		return false
	}
	s.preauth.Prove(conn.RemoteAddr())
	// Judged just before the answer: the read above can take the whole claim
	// timeout, and a run that ended during it must not answer. See endedClaim.
	if endedClaim(g.ctx, conn) {
		return false
	}

	if err := utils.SendBinaryTransportStringWithin(conn, s.config.Token, utils.SG_Chan, 10*time.Second); err != nil {
		s.logger.Errorf("failed to send security token: %v", err)
		return false
	}

	return true
}

// control is this generation's control loop, bound to the control channel
// the generation was started with. See controlLoop.
func (s *UdpTransport) control(g *udpGen, ctx context.Context, lost func()) controlLoop {
	return controlLoop{
		ctx:      ctx,
		link:     controlwire.Net(s.controlChannel.Get()),
		beat:     s.config.Heartbeat,
		requests: g.reqNewConnChan,
		log:      s.logger,
		restart:  lost,
		probeRTT: true,
	}
}

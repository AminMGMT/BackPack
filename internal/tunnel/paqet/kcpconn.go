package paqet

// Derived from paqet's internal/tnet/kcp (MIT); see doc.go.

import (
	"github.com/xtaci/kcp-go/v5"
	"github.com/xtaci/smux"
)

// kcpMode is one row of the mode table: kcp-go's NoDelay arguments and the two
// batching switches.
type kcpMode struct {
	noDelay, interval, resend, noCongestion int
	wDelay, ackNoDelay                      bool
}

// modeOf returns the mode table's row for p.
func modeOf(p Params) kcpMode {
	switch p.Mode {
	case ModeNormal:
		return kcpMode{0, 40, 2, 1, true, false}
	case ModeFast:
		return kcpMode{0, 30, 2, 1, true, false}
	case ModeFast2:
		return kcpMode{1, 20, 2, 1, true, false}
	case ModeFast3:
		return kcpMode{1, 10, 2, 1, true, false}
	case ModeManual:
		return kcpMode{p.NoDelay, p.Interval, p.Resend, p.NoCongestion, p.WDelay, p.AckNoDelay}
	}
	return kcpMode{}
}

// applyKCP tunes one KCP session. The mode table is paqet's.
func applyKCP(c *kcp.UDPSession, p Params) {
	m := modeOf(p)
	c.SetNoDelay(m.noDelay, m.interval, m.resend, m.noCongestion)
	c.SetWindowSize(p.SndWnd, p.RcvWnd)
	c.SetMtu(p.MTU)
	c.SetWriteDelay(m.wDelay)
	c.SetACKNoDelay(m.ackNoDelay)
	c.SetStreamMode(true)
	// A fast retransmission that is lost is sent again after about a round
	// trip, not after the timer (and its back-off): under loss the streams
	// behind it wait for it. See internal/thirdparty/kcp-go/kcp.go.
	c.SetFastRetransmitAgain(true)
	c.SetDSCP(46)
}

// smuxConfig is paqet's: protocol version 2 (per-stream flow control), 64 KiB
// frames, and the keepalive that is this engine's liveness signal — a session
// whose peer goes silent for KeepAliveTimeout closes itself, which is what
// Up() watches.
func smuxConfig(p Params) *smux.Config {
	c := smux.DefaultConfig()
	c.Version = 2
	c.KeepAliveInterval = p.KeepAlive
	c.KeepAliveTimeout = p.KeepAliveTimeout
	c.MaxFrameSize = 65535
	c.MaxReceiveBuffer = p.SmuxBuf
	c.MaxStreamBuffer = p.StreamBuf
	return c
}

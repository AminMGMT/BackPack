// Package controlwire is how a reverse tunnel's control channel carries its
// signals, for both ends.
//
// Every signal is one byte — a heartbeat, a request for a pool connection, a
// round-trip probe, a goodbye (see utils.SG_*). The stream transports send the
// byte as it is; the websocket transports send it as a one-byte binary message.
// Each end of the tunnel used to carry its own copy of both, with its own copy
// of the write bound, and a test that read the other side's source to check the
// two bounds still agreed. They are here once, so they cannot disagree.
package controlwire

import (
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/backpack/backpack/internal/utils"
	"github.com/gorilla/websocket"
)

// WriteTimeout bounds a write on the control channel.
//
// None of the signals is worth waiting on, and waiting on one is what took a
// tunnel down for a quarter of an hour at a time. A write goes into the
// kernel's send buffer and returns; when the peer stops absorbing anything the
// buffer fills and the write blocks until the kernel stops retransmitting,
// which is around fifteen minutes on Linux defaults. For that whole window the
// server believed it had a control channel — refusing the client's attempts to
// make a new one, asking nobody for pool connections, dropping every user with
// the queue full — and the client could hang a restart on a goodbye nobody was
// going to read.
//
// Ten seconds is far longer than a healthy path needs for one byte and far
// shorter than a broken one takes to admit it. Past that the channel is treated
// as gone, which is what it is.
const WriteTimeout = 10 * time.Second

// Link is one control channel, as the loop serving it sees it.
type Link interface {
	// Send writes one signal, bounded by WriteTimeout.
	Send(signal byte) error
	// Receive blocks for the next signal. A frame that carried none is
	// reported as ErrNoSignal, and the caller reads on.
	Receive() (byte, error)
	// SetReadDeadline bounds the next Receive.
	SetReadDeadline(t time.Time) error
	Close()
}

var (
	// ErrNoSignal is a frame that arrived but was not a signal.
	ErrNoSignal = errors.New("not a control signal")
	// ErrNoChannel is a link with no connection behind it.
	ErrNoChannel = errors.New("no control channel")
)

// Net is a control channel on a stream: signals travel as single bytes.
func Net(conn net.Conn) Link { return netLink{conn} }

type netLink struct{ conn net.Conn }

func (l netLink) Send(signal byte) error {
	if l.conn == nil {
		return ErrNoChannel
	}
	return utils.SendBinaryByteWithin(l.conn, signal, WriteTimeout)
}

func (l netLink) Receive() (byte, error) {
	if l.conn == nil {
		return 0, ErrNoChannel
	}
	return utils.ReceiveBinaryByte(l.conn)
}

func (l netLink) SetReadDeadline(t time.Time) error {
	if l.conn == nil {
		return ErrNoChannel
	}
	return l.conn.SetReadDeadline(t)
}

func (l netLink) Close() {
	if l.conn != nil {
		_ = l.conn.Close()
	}
}

// PeerClosed reports whether a failed read means the other end closed the
// channel — it stopped, restarted or was killed — rather than that something
// went wrong with it: an EOF, a reset, a websocket closed without its close
// frame, or a QUIC peer whose generation ended.
//
// Both ends logged that at error level, and in a long-running tunnel it is the
// ordinary way the far end goes away for an update or a reboot: a soak's log
// was dozens of "failed to read from the control channel: EOF" errors for a
// tunnel that had nothing wrong with it, which buries the errors that matter.
// It is still said, as a warning that names what happened; the reconnect that
// follows is unchanged.
func PeerClosed(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	if websocket.IsCloseError(err, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return true
	}
	return strings.Contains(err.Error(), "generation ended")
}

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
	"fmt"
	"net"
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

// WS is a control channel on a websocket: signals travel as one-byte binary
// messages.
func WS(conn *websocket.Conn) Link { return wsLink{conn} }

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

type wsLink struct{ conn *websocket.Conn }

// Send goes through gorilla's WriteMessage, which takes its deadline from the
// connection and would otherwise have none.
func (l wsLink) Send(signal byte) error {
	if l.conn == nil {
		return ErrNoChannel
	}
	if err := l.conn.SetWriteDeadline(time.Now().Add(WriteTimeout)); err == nil {
		defer func() { _ = l.conn.SetWriteDeadline(time.Time{}) }()
	}
	return l.conn.WriteMessage(websocket.BinaryMessage, []byte{signal})
}

func (l wsLink) Receive() (byte, error) {
	if l.conn == nil {
		return 0, ErrNoChannel
	}
	messageType, msg, err := l.conn.ReadMessage()
	if err != nil {
		return 0, err
	}
	signal, ok := utils.WebSocketSignal(messageType, msg)
	if !ok {
		return 0, fmt.Errorf("%w: frame type %d, %d bytes", ErrNoSignal, messageType, len(msg))
	}
	return signal, nil
}

func (l wsLink) SetReadDeadline(t time.Time) error {
	if l.conn == nil {
		return ErrNoChannel
	}
	return l.conn.SetReadDeadline(t)
}

func (l wsLink) Close() {
	if l.conn != nil {
		_ = l.conn.Close()
	}
}

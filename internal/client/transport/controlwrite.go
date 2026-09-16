package transport

import (
	"errors"
	"time"

	"github.com/gorilla/websocket"
)

// controlWriteTimeout bounds a write on the client's control channel.
//
// The server's transports have carried this since the failure that produced it,
// and the reasoning is written out over the constant of the same name in
// internal/server/transport/control.go. It applies word for word on this side,
// and this side did not have it.
//
// A write goes into the kernel's send buffer and returns; when the peer stops
// absorbing anything the buffer fills and the write blocks until the kernel
// gives up retransmitting — around fifteen minutes on Linux defaults. The two
// places that mattered here are the ones least able to afford it. The shutdown
// notice is sent while the transport is being torn down, so a restart could hang
// for a quarter of an hour on a byte nobody was ever going to read. The RTT
// probe runs on a timer forever, so a stalled path leaves a goroutine parked in
// a write for the same window and the measurement the panel shows simply stops
// moving, with no error anywhere to say why.
//
// Ten seconds is far longer than a healthy path needs for one byte and far
// shorter than a broken one takes to admit it.
const controlWriteTimeout = 10 * time.Second

// writeControl sends one control byte on a websocket control channel, bounded
// the same way and for the same reason as SendBinaryByteWithin.
//
// The websocket transports carry the identical unbounded write — a heartbeat
// into a peer that has stopped reading blocks until the kernel gives up — and
// they reach it through gorilla's WriteMessage rather than the utils helper, so
// bounding the helper left them exactly as they were. The server grew this
// function for the same reason; see internal/server/transport/control.go.
func writeControl(conn *websocket.Conn, payload []byte) error {
	if conn == nil {
		return errors.New("no control channel")
	}
	if err := conn.SetWriteDeadline(time.Now().Add(controlWriteTimeout)); err == nil {
		defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
	}
	return conn.WriteMessage(websocket.BinaryMessage, payload)
}

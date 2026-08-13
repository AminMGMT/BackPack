package utils

import "github.com/gorilla/websocket"

const (
	SG_HB     byte = iota // for heartbeat
	SG_Chan               // for channel, req a new conn
	SG_Ping               // for ping
	SG_Closed             // for closed channel
	SG_TCP                // TCP Transport ID
	SG_UDP                // TCP Transport ID
	SG_RTT                // For RTT measurment

	// SG_ChanV2 opens a control channel that authorises pool connections by a
	// per-run nonce instead of by source address. It is a separate signal
	// rather than a flag inside the old one so that a server which predates it
	// simply does not recognise it, and the client can fall back — see the
	// handshake in the client transports.
	SG_ChanV2
	// SG_Pool announces a pool connection, carrying the nonce the server handed
	// out when the control channel was established.
	SG_Pool
)

// WebSocketSignal reads one control-channel signal out of a WebSocket frame.
//
// Every signal above goes on the wire the same way: a binary frame holding
// exactly one byte. The read loops took that on trust and went straight to
// msg[0], which is fine for every frame either end of this tunnel has ever
// sent and fatal for one it has not — an empty binary frame indexes past the
// end of the slice and takes the whole process down with it. A control channel
// is reachable by whoever holds the token, and a tunnel that can be stopped by
// one zero-length frame is not one that should be.
//
// ok is false for anything that is not a single-byte binary frame. The callers
// log it and read on: dropping a frame that carries no signal leaves them
// exactly where ignoring it always left them, and is the one response that
// cannot be provoked into disrupting a tunnel that is working.
func WebSocketSignal(messageType int, payload []byte) (signal byte, ok bool) {
	if messageType != websocket.BinaryMessage || len(payload) != 1 {
		return 0, false
	}
	return payload[0], true
}

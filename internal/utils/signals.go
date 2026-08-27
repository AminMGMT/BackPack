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

	// SG_Refused answers a control handshake the server will not accept,
	// carrying a short reason.
	//
	// It exists because the alternative was silence. The server used to close
	// the connection on a token it did not recognise and on a claim for a
	// control channel it had already given away, and both reach the client as
	// "failed to read message length from net.Conn: EOF" — the same thing an
	// old server produces by not understanding the signal at all. Three
	// different faults, one symptom, and the client guessed the least likely of
	// them: it reported the server as out of date and told the operator to
	// upgrade a server whose only problem was a mistyped token.
	//
	// A client too old to know this signal is no worse off than before. It
	// compares the answer against its own token, fails to match, and reports an
	// invalid token — which is wrong in wording but points at the right half of
	// the configuration, where EOF pointed at nothing.
	SG_Refused
)

// Why a control handshake was refused. Short, because it crosses the wire on
// every rejected attempt, and free of anything an unauthenticated peer should
// not be told — a refusal says which side is wrong, never what the right answer
// would have been.
const (
	// RefusedBadToken is a token that does not match the server's.
	RefusedBadToken = "token"

	// RefusedInUse is a control channel this server has already given to
	// somebody else — two clients dialling one server with the same token, or
	// an old service left running beside its replacement.
	RefusedInUse = "in-use"
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

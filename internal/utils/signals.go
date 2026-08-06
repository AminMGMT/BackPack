package utils

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

	// SG_ForwardTCP announces a data connection opened by the dialling Iran
	// edge. The payload is the current control-channel nonce; the next framed
	// string is the backend target on the Kharej origin. Older binaries reject
	// the unknown signal without changing legacy reverse behaviour.
	SG_ForwardTCP
	// SG_ForwardUDP is the datagram equivalent. It is reserved separately so a
	// receiver can never interpret UDP framing as a TCP byte stream.
	SG_ForwardUDP
	// The origin answers a forward-open only after its backend dial succeeds.
	// This keeps an accepted Iran-side user socket from hanging against a dead
	// or invalid backend.
	SG_ForwardOK
	SG_ForwardError
)

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
)

package network

import "net"

// SizeUDPBuffers sizes a datagram socket to the configured receive and send
// buffers. A zero leaves the kernel default in place. Best effort: a socket
// that refuses the size — usually because net.core.rmem_max is lower — still
// works, it just has less headroom against a burst, so a refusal is only
// reported.
func SizeUDPBuffers(conn *net.UDPConn, rcvBuf, sndBuf int, warnf func(string, ...any)) {
	if rcvBuf > 0 {
		if err := conn.SetReadBuffer(rcvBuf); err != nil {
			warnf("failed to set UDP read buffer to %d: %v", rcvBuf, err)
		}
	}
	if sndBuf > 0 {
		if err := conn.SetWriteBuffer(sndBuf); err != nil {
			warnf("failed to set UDP write buffer to %d: %v", sndBuf, err)
		}
	}
}

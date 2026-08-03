package network

import (
	"syscall"
)

// Why the tunnel does not ask for SO_REUSEPORT.
//
// Every socket the tunnel opened — listeners and outgoing connections alike —
// used to be given SO_REUSEADDR and, on Linux, SO_REUSEPORT, and a setsockopt
// that failed aborted the dial or the listen. Both halves of that were wrong.
//
// The failure was fatal where it should have been ignored. SO_REUSEPORT needs a
// kernel that has it and a container that permits it; where either is missing
// setsockopt returns an error, and the tunnel then could not open a single
// socket — no connection at all, on a machine where nothing was actually wrong.
// An option that only tunes behaviour must never decide whether the connection
// happens.
//
// Worse, where it succeeded it removed the one safeguard that catches a server
// started twice. SO_REUSEPORT is a deliberate request to let another process
// bind the same port, and the kernel then splits arriving connections between
// them. A leftover process from a crash or an upgrade — likely enough under a
// unit that restarts every few seconds — no longer collides with "address
// already in use"; it quietly takes a share of the connections. The client's
// control channel lands on one process and its pool connections on the other,
// so the tunnel comes up and carries nothing, with both processes logging as
// though they were fine.
//
// Nothing here wants either behaviour: one process binds each port, and an
// outgoing connection has no reason to share a local address with anything. So
// SO_REUSEPORT is not asked for at all, and SO_REUSEADDR — which only governs
// rebinding a port left in TIME_WAIT, and is what every server sets — is asked
// for on listeners, best effort.

// ListenReuseControl sets SO_REUSEADDR on a listening socket, so a restart does
// not have to wait out TIME_WAIT on the port.
//
// It never fails the listen: a kernel that refuses the option still binds and
// accepts perfectly well, and the only cost is that an immediate restart may
// have to retry.
func ListenReuseControl(network, address string, s syscall.RawConn) error {
	_ = s.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	})
	return nil
}

// Package core is the shared half of the plain reverse transports — tcp,
// tcpmux, ws, wss, wsmux and wssmux.
//
// The wire is kept simple on purpose: the framing, the signal bytes, the
// token handshake, the dialers with their retries, the relay loops and the
// port mappings stay as they are, and what Backpack adds — traffic counters,
// the panel's status, the tunnel's lifecycle, the encryption layer — hangs on
// it from outside; each such place says so.
package core

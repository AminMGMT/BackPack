// Package core is the shared half of the plain reverse transports — tcp,
// tcpmux, ws, wss, wsmux and wssmux — ported from Backhaul
// (github.com/Musixal/Backhaul, AGPL-3.0, the project Backpack began as).
//
// The logic and the wire are Backhaul's, on purpose: the framing, the signal
// bytes, the token handshake, the dialers with their retries, the relay loops
// and the port mappings are what Backhaul does, so a Backpack end and a
// Backhaul end speak to each other. What differs is only what Backpack hangs
// on it from outside (traffic counters, the panel's status, the tunnel's
// lifecycle) and what the race detector requires; each such place says so.
package core

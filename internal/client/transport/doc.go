// Package transport is the kharej side of the reverse tunnel's transports: it
// dials the Iran server, holds the control channel, keeps a pool of tunnel
// connections ready, and carries each user to the local service.
//
// Each transport (tcp, tcpmux, ws, wsmux, kcp, quic, udp) is only what is its
// own — how it dials and what a tunnel connection is. What every one of them
// must get the same is shared: the generation and its restart (lifecycle.go),
// the control channel's loop (controlloop.go, over internal/controlwire), the
// pool's size (poolmaintain.go), and the last hop to the backend (backend.go).
// See docs/adr/0001-reverse-transport-generations.md.
package transport

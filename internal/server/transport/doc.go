// Package transport is the Iran side of the reverse tunnel's transports: it
// listens for the kharej client, holds the control channel, serves the
// forwarded ports, and pairs each user with a tunnel connection.
//
// Each transport (tcp, tcpmux, ws, wsmux, kcp, quic, udp) is only what is its
// own — how tunnel connections arrive and how a user rides one. What they must
// get the same is shared: the generation and its restart (lifecycle.go), the
// control channel's loop (controlloop.go, over internal/controlwire), binding
// the tunnel port (bindfail.go, and tunnelport.go for plain TCP), and — for the
// stream transports — the forwarded ports (forward.go) and pairing
// (pairing.go, pairloop.go). udp, whose users are datagram flows, has its own
// port and flow handling and shares the port-mapping parser (eachForward).
// See docs/adr/0001-reverse-transport-generations.md.
package transport

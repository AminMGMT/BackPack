// Package l3 carries whole IP packets between two hosts.
//
// Every other transport in this tree forwards ports: a listener on one side, a
// backend dial on the other, and a stream in between. This one is a layer-3
// tunnel. It opens a TUN device, reads the IP packets the kernel routes into
// it, and hands them to the peer, which writes them into its own TUN. What the
// two hosts get is an ordinary point-to-point link — 10.10.0.1 talking to
// 10.10.0.2 — over which anything at all can be routed, including protocols a
// port forwarder has no way to carry.
//
// # Why it is built here rather than borrowed from the kernel
//
// Linux already implements ipip and gre. Neither is usable for what this is
// for. They ride directly on IP, as protocol 4 and protocol 47, which is a
// thing a censor blocks trivially and a NAT drops by accident; they have no
// encryption at all; and — decisively — the kernel puts its own encapsulated
// packet straight onto the wire, so there is no point at which this process
// could take those bytes and carry them inside one of its own carriers. The
// encapsulation therefore happens here, in userspace, as a framing format we
// own, and the delivery happens over the same obfuscated carriers the rest of
// the tunnel already uses.
//
// The kernel's own GRE is offered as well, as the gre and ip6gre carriers, for
// a route that does pass protocol 47 and an operator who wants the cheapest
// tunnel there is and brings encryption of their own. Everything said above
// about it still holds, and the screens say so where it is chosen. It is not a
// carrier in the sense below — the kernel encapsulates, so there is no TUN and
// no session — and the engine runs it on a path of its own; see kernelgre.go.
//
// # pck is not on this stack
//
// Since v2.0.0 carrier = "pck" is a different engine — paqet's, in
// internal/tunnel/paqet — that carries forwarded ports over crafted TCP
// segments with KCP and smux, and has no TUN device and no session of this
// package's. It shares the configuration table, the forwarder, Up() and Stats()
// with everything here, and takes a path of its own through Run, as the kernel
// GRE carriers do; see pck.go. Everything below, and the raw-datagram pck it
// replaced, is now sni's (sni is that carrier with a ClientHello in front).
//
// # The stack
//
//	[ inner IP packet ]                      read from the TUN device
//	[ encap header    ]  0 or 4-12 bytes     ipip adds nothing; gre names the
//	                                         payload and can carry a key
//	[ AEAD + header   ]  29 bytes            see wire.go and session.go
//	[ carrier         ]  28-60 bytes         pck, sni, xdi or spoof
//
// # Why the carrier must be a datagram carrier
//
// An IP packet already belongs to something that handles its own loss — a TCP
// flow inside the tunnel, or WireGuard, or a game's own protocol. Putting that
// inside a second reliable layer gives two retransmit timers stacked on one
// path, and under loss they fight: the inner one gives up waiting while the
// outer one is still resending, so throughput collapses instead of degrading.
// This is the classic TCP-over-TCP meltdown, and it is why a layer-3 tunnel
// may only ride on the raw datagram carriers — never on tcp, ws, or kcp, all
// of which retransmit. (Plain udp and quic datagrams were carriers too, until
// v2.0.0 removed them.)
//
// # Relationship to the rest of the tree
//
// Nothing here is reachable from the reverse tunnel's code, and nothing here
// calls into it. The two share no state, no sockets and no configuration keys:
// a config without an [l3] section can never enter this package, and a config
// with one never reaches the reverse engine. The Noise handshake below is a
// deliberate second implementation rather than a reuse of the stealth
// transport's, because the two protocols must not be confusable on the wire —
// see session.go for the domain separation that guarantees it.
package l3

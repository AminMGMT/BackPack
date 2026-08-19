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
// # The stack
//
//	[ inner IP packet ]                      read from the TUN device
//	[ encap header    ]  0 or 4-12 bytes     ipip adds nothing; gre names the
//	                                         payload and can carry a key
//	[ AEAD + header   ]  29 bytes            see wire.go and session.go
//	[ carrier         ]  28-40 bytes         udp today; spoof/pck/xdi next
//
// # Why the carrier must be a datagram carrier
//
// An IP packet already belongs to something that handles its own loss — a TCP
// flow inside the tunnel, or WireGuard, or a game's own protocol. Putting that
// inside a second reliable layer gives two retransmit timers stacked on one
// path, and under loss they fight: the inner one gives up waiting while the
// outer one is still resending, so throughput collapses instead of degrading.
// This is the classic TCP-over-TCP meltdown, and it is why a layer-3 tunnel
// may only ride on udp, quic datagrams, or the raw carriers — never on tcp,
// ws, or kcp, all of which retransmit.
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

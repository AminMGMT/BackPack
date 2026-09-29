// Package network is where the unusual networking lives: the Noise record
// layer and handshakes, KCP and its tuning, the raw-socket carriers (pck over
// packet sockets, xdi over ICMP, spoof), QUIC and uTLS, outbound routing
// (proxy, source address, interface, mark), endpoint rotation, socket options,
// and the UDP datagram framing both tunnel ends use.
//
// It is below the engines: they decide what a tunnel is and when it restarts,
// and build on what is here.
package network

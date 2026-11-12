// Package paqet is the engine behind the layer-3 tunnel's "pck" carrier.
//
// It is a port of paqet (https://github.com/hanselime/paqet, MIT), adapted to
// Backpack's logging, configuration and lifecycle. Where it differs from
// paqet, and why, is listed under "What changed" below.
//
// # Copyright notice
//
// The code in this package that derives from paqet is covered by paqet's
// licence, reproduced here as it requires and in the NOTICE file at the root
// of the repository:
//
//	MIT License
//
//	Copyright (c) 2025 hanselime
//
//	Permission is hereby granted, free of charge, to any person obtaining a copy
//	of this software and associated documentation files (the "Software"), to deal
//	in the Software without restriction, including without limitation the rights
//	to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
//	copies of the Software, and to permit persons to whom the Software is
//	furnished to do so, subject to the following conditions:
//
//	The above copyright notice and this permission notice shall be included in all
//	copies or substantial portions of the Software.
//
//	THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
//	IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
//	FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
//	AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
//	LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
//	OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
//	SOFTWARE.
//
// # How it works
//
// There is no TUN device, no session of our own and no kernel socket on the
// tunnel's port. Packets are crafted as TCP segments and written to the
// network card with libpcap; the other side's segments are captured the same
// way, by a BPF filter on the tunnel's port. The kernel never sees a
// connection, so a stateful box in the path sees an ordinary flow and the host
// has nothing to answer it with (the RSTs it would answer with are dropped by
// a firewall rule this package installs, see network.LeasePckPorts).
//
//	user TCP/UDP -> [forwarder] -> smux stream -> smux session -> KCP session
//	             -> block cipher -> crafted TCP segment -> pcap -> wire
//
// KCP provides the reliability, ordering and (optionally) the FEC that the
// pseudo-flow lacks; smux multiplexes many forwarded connections over one KCP
// session; the block cipher, keyed from the tunnel token, keeps the payload
// unreadable. The Iran side is paqet's client: it opens streams and each
// carries one forwarded TCP connection or one UDP flow. The kharej side is
// paqet's server: it accepts sessions and dials the target each stream names.
//
// # What changed from paqet
//
//   - One capture handle for the whole tunnel, shared by every KCP session,
//     instead of a capture per session: a client with conn=4 would otherwise
//     hold four capture rings, which on a small VPS with a 32 MiB ring each is
//     the memory Balance exists to save. See wire.go. Sending takes one small
//     handle per session, up to four, so the sessions' system calls run in
//     parallel instead of queueing on one lock (see OpenPcap); each flow keeps
//     to its own handle, so nothing is reordered.
//   - The server dials only its own addresses unless told otherwise
//     (targets.go), requires the hello before it dials anything, and bounds
//     the forwarded connections open through it.
//   - Interface, local address and gateway MAC are found, not configured; the
//     firewall rules paqet's README tells the operator to type are installed
//     and removed by the engine. See network.DiscoverPckPath and
//     network.LeasePckPorts.
//   - The cipher key is derived from the tunnel token, with a salt of our own,
//     so the two products cannot be confused on the wire.
//   - The control stream grew a hello (protocol version, engine revision) and
//     every request is answered with a status byte, so a mismatch or a refused
//     target is reported instead of being a silent reset. UDP datagrams are
//     length-prefixed instead of relying on smux frame boundaries.
//   - A forwarded TCP connection keeps its half-close. A smux stream closes
//     both directions at once, so paqet ends the whole connection at the first
//     EOF — which breaks every protocol that shuts down its write side and then
//     waits for the reply. The data of a TCP stream is framed (framed.go) so
//     each direction can end on its own, as the TUN path always let it.
//   - paqet's TCP-flag negotiation stream is gone: each side sends the flag
//     cycle it is configured with, as Backpack's pck always did.
//   - The send window is moved by a controller (congestion.go) instead of being
//     fixed: paqet runs KCP with no congestion control and a fixed window, which
//     on a path with a bottleneck queue sends more than the queue holds. The
//     controller reads delay and local overflow and not loss, so random loss
//     does not shrink it; the preset's window is its ceiling.
//   - KCP batches its acknowledgements and small writes in every mode (paqet's
//     fast2 and fast3 answer every packet with one), which halves the packet
//     rate of a link with many short connections. See params.go.
//   - A forwarded UDP flow's datagrams that fit one segment travel on a
//     datagram lane beside KCP, unreliable and unqueued, sealed with a key of
//     their own (datagram.go), instead of as frames in a reliable stream.
//   - smux is a modified copy (internal/thirdparty/smux): window updates are
//     sent by a goroutine of their own, and control frames go ahead of data in
//     the write queue. Without that, a session carrying hundreds of short
//     connections beside bulk transfers stopped reading for up to a minute.
//   - SOCKS5, the Windows path and the YAML configuration were left out.
//   - A peer still running the previous (raw datagram) PCK is recognised by
//     the shape of its first handshake and reported by name. See foreign.go.
//   - Where the link has no Ethernet header (a tunnel or PPP device), segments
//     are captured and sent as bare IP.
package paqet

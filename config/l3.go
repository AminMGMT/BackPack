package config

import "strings"

// L3Config is a direct layer-3 tunnel: an interface on each host carrying
// whole IP packets between them, rather than a set of forwarded ports.
//
// It lives in its own [l3] table, entirely apart from [server] and [client].
// That separation is deliberate and load-bearing: a configuration that does
// not mention [l3] cannot reach the layer-3 engine, and one that does never
// reaches the reverse tunnel. There is no key the two share and no state they
// pass between them, so nothing about this can perturb a working reverse
// tunnel.
//
// A minimal pair, with the Iran side dialling out:
//
//	# Iran
//	[l3]
//	mode     = "dial"
//	addr     = "KHAREJ_IP:9000"
//	token    = "SAME_LONG_TOKEN"
//	local_ip = "10.10.0.1/30"
//	peer_ip  = "10.10.0.2"
//
//	# Kharej
//	[l3]
//	mode     = "listen"
//	addr     = "0.0.0.0:9000"
//	token    = "SAME_LONG_TOKEN"
//	local_ip = "10.10.0.2/30"
//	peer_ip  = "10.10.0.1"
//
// Both ends must agree on the token, the encapsulation and the carrier. Which
// end dials is the direct/reverse question at this layer, and it is free:
// whichever host can accept an inbound connection listens, and the other
// dials.
type L3Config struct {
	// Mode is "dial" or "listen". Empty means there is no layer-3 tunnel in
	// this configuration, which is how every pre-existing config reads.
	Mode string `toml:"mode"`

	// Addr is the peer's host:port when dialling, or the address to bind when
	// listening. On the kernel GRE carriers it is the other server's real
	// address on both sides, with no port — "203.0.113.9" — because a GRE link
	// names both of its ends and neither of them listens.
	Addr string `toml:"addr"`

	// GRELocal is this server's own address for a kernel GRE link. Empty — the
	// default — uses the address the route to addr leaves from, which is right
	// unless the server has several and the other end expects another. Read
	// only when carrier is "gre" or "ip6gre".
	GRELocal string `toml:"gre_local"`

	// Token is the shared secret. It is the only credential: the handshake
	// derives its pre-shared key from it, and a peer without it is answered
	// with silence. The kernel GRE carriers have no handshake and no secret,
	// and need none.
	Token string `toml:"token"`

	// Carrier is the datagram transport underneath: "pck" (a different
	// engine from the rest — see below), "sni" (pck, plus a TLS ClientHello
	// naming an allowed domain at the start of the flow), "xdi" (inside ICMP
	// echo) or "spoof" (raw IP with a forged source), all four Linux-only and
	// needing CAP_NET_RAW; or "gre" / "ip6gre", kernel GRE over IPv4 or IPv6
	// endpoints, which is NOT encrypted.
	//
	// "pck" is not a datagram carrier for the layer-3 engine. Since v2.0.0 it is
	// paqet's engine: TCP segments crafted and captured with libpcap, KCP for
	// reliability, smux streams on top, forwarding the ports below. There is no
	// TUN device and no routed traffic: a pck tunnel carries ports only (Iran
	// dials, listens on ports, and the kharej side dials the target). It reads
	// mode, addr, token, ports, accept_udp, max_connections, bandwidth_mbps,
	// preset, sockbuf (the capture ring), pck_* and the keys under PckConns. A
	// file written for the old PCK needs no change: see l3.ConfigFromTable.
	//
	// The two GRE carriers are a link the kernel creates and encapsulates for
	// itself. There is no carrier socket, no handshake and no encryption — it
	// travels as bare IP protocol 47, and what crosses it is in the clear unless
	// it is encrypted itself. Both need CAP_NET_ADMIN rather than CAP_NET_RAW.
	//
	// It is required. "udp" — which an empty carrier used to mean — and "quic"
	// were removed in v2.0.0, and a file that still names either, or names
	// none, is refused with the list above rather than moved onto one of them.
	//
	// A reliable carrier is not an option here — see the l3 package doc for
	// why stacking retransmission is actively harmful rather than merely
	// wasteful.
	Carrier string `toml:"carrier"`

	// Encap is always GRE now: four bytes, or eight with gre_key. "ipip" is
	// still accepted and read as GRE, so a file written when there was a choice
	// keeps working. One tunnel carries both IPv4 and IPv6.
	Encap string `toml:"encap"`

	// GREKey is the RFC 2890 key, letting more than one logical tunnel share
	// a carrier. Zero omits the field. On the kernel GRE carriers it is the
	// link's key, which both ends must share; the setup picks one per tunnel.
	//
	// It matters more there: the kernel allows one GRE link per pair of
	// addresses and key, so it is what lets several GRE tunnels join the same
	// two servers. The setup link carries it to the other end.
	GREKey uint32 `toml:"gre_key"`

	// SNIDomain is the server name the "sni" carrier puts in that hello. Empty
	// uses the built-in default. It has to be a domain the path already lets
	// through — which one that is depends on the route, so it is the operator's
	// to choose and to test.
	SNIDomain string `toml:"sni_domain"`

	// Iface is the interface to create. Empty makes "bp0".
	Iface string `toml:"iface"`

	// LocalIP is this end's address on the tunnel, normally with a prefix:
	// "10.10.0.1/30". A bare address requires PeerIP and makes a
	// point-to-point link instead.
	LocalIP string `toml:"local_ip"`

	// PeerIP is the other end's address on the tunnel.
	PeerIP string `toml:"peer_ip"`

	// MTU is the interface MTU. Zero takes a deliberately conservative
	// default, because a layer-3 tunnel whose packets are slightly too large
	// does not fail loudly — it passes small flows and stalls large ones. On
	// the kernel GRE carriers zero is worked out from the outgoing interface.
	//
	// That is the MTU of the interface the route to addr leaves by, minus
	// GRE's own 24 bytes (28 with a key; 44 and 48 for ip6gre).
	MTU int `toml:"mtu"`

	// SockBuf sizes the carrier's socket buffers in bytes. Zero takes the
	// carrier default of 4 MiB.
	SockBuf int `toml:"sockbuf"`

	// FECData and FECParity add forward error correction to the carrier: for
	// every FECData datagrams, FECParity extra ones are sent, and any FECParity
	// of the group may be lost without losing anything. Both zero — the default
	// — is no error correction.
	//
	// It is redundancy, not reliability: nothing is retransmitted and nothing
	// waits for a timer, which is what makes it safe here where a reliable
	// carrier is not (see the l3 package doc). It costs the parity's share of
	// the bandwidth, so it earns its keep on a path that drops packets steadily
	// and wastes it on one that does not.
	//
	// Both ends must set the same pair: the scheme is not negotiated, and a
	// receiver expecting a different one cannot rebuild anything.
	FECData int `toml:"fec_data"`
	// FECParity is how many parity packets accompany each group. Both ends must
	// agree on this and on fec_data.
	FECParity int `toml:"fec_parity"`

	// Preset is the name of the tuning profile the two keys below came from.
	// It is a label: the engine reads the values, not this.
	Preset string `toml:"preset"`

	// TxQueueLen is how many packets the kernel may hold for the interface
	// while the tunnel drains it. Deeper is not better — a queue is latency,
	// and a full one is the jitter. Zero takes the default.
	TxQueueLen int `toml:"txqueuelen"`

	// Qdisc is the queueing discipline on the interface, and is the setting
	// that actually decides jitter. Empty takes fq_codel, which drops when
	// packets start waiting instead of letting the queue grow into delay.
	Qdisc string `toml:"qdisc"`

	// AutoMTU measures what the path really carries, once the tunnel is up, and
	// sets the interface to match.
	//
	// The MTU is the one setting here that cannot be derived from anything in
	// this file, and the one that fails worst when it is wrong: too high and
	// the tunnel comes up, answers every health check, carries ping and SSH,
	// and stalls every download and every TLS handshake. The packets that
	// matter are the large ones, and they are dropped out on the path with
	// nothing coming back to say so.
	//
	// On by default. A peer too old to answer a probe leaves the configured
	// figure untouched, so turning it on cannot break an existing pair.
	AutoMTU *bool `toml:"auto_mtu"`

	// MSSClamp caps the TCP segment size of connections crossing the tunnel,
	// which is the fix for the failure a layer-3 tunnel produces most often and
	// advertises least: ping works, pages load, and every large transfer stalls
	// forever, because both endpoints negotiated a segment from their own
	// 1500-byte interfaces and the ICMP message that would have corrected them
	// was dropped somewhere on the way.
	//
	// Zero — the default — derives it from the MTU, which is right for almost
	// every tunnel. A positive value sets it explicitly. -1 turns it off, for a
	// host whose firewall is managed elsewhere.
	MSSClamp int `toml:"mss_clamp"`

	// Ports are forwarded port mappings served over the tunnel, in the same
	// syntax as the reverse tunnel's: "443", "443=8443", "10000-10009", and
	// "443=10.0.0.1:80|10.0.0.2:80" for several backends. A target with no
	// host of its own means PeerIP, which is what almost every mapping wants.
	//
	// Empty is the plain layer-3 case: the tunnel carries whatever the kernel
	// routes into the interface and forwards no ports of its own.
	Ports []string `toml:"ports"`

	// AcceptUDP forwards UDP as well as TCP on the mapped ports. Off unless
	// set, matching the reverse tunnel's default and for the same reason: a
	// tunnel should not silently start carrying every QUIC flow on port 443.
	AcceptUDP bool `toml:"accept_udp"`

	// MaxConnections and BandwidthMbps cap the forwarded ports above
	// (0 = unlimited each). They do not apply to routed traffic: the tunnel
	// carries whatever the kernel puts into the interface, which has no
	// connections to count.
	MaxConnections int `toml:"max_connections"`
	// BandwidthMbps caps total throughput across this tunnel, in megabits per
	// second. 0 is unlimited.
	BandwidthMbps int `toml:"bandwidth_mbps"`

	// Embedded so the spoof_* keys sit at the top level of the [l3] table.
	// This is the only table they are read from, and they are read only when
	// carrier = "spoof".
	SpoofConfig

	// Embedded the same way for the pck_* keys, read only when
	// carrier = "pck".
	PckConfig

	// The pck_* keys below tune the PCK engine and are read only when carrier is
	// "pck". Every one is optional: left out, it comes from the preset the file
	// names (see Preset), which is how a file written by an older version —
	// which has a preset and a sockbuf and none of these — runs unchanged.
	//
	// What the engine is, and which of the keys above it still reads, is under
	// Carrier. Keys that only made sense for the TUN device (iface, local_ip's
	// address, peer_ip's address, mtu, txqueuelen, qdisc, auto_mtu, mss_clamp,
	// fec_*, encap, gre_key) are accepted and ignored on a pck tunnel, so a file
	// from before the engine changed needs no edit.

	// PckConns is how many KCP sessions the Iran side holds open to the kharej
	// side, each its own TCP flow. Zero takes the preset's (Balance 1, Turbo 2,
	// Aggressive 4); at most 16.
	PckConns int `toml:"pck_conns"`
	// PckKCPMode is KCP's speed mode: "normal", "fast", "fast2" or "fast3",
	// from least CPU to lowest latency. Empty takes the preset's (Balance fast,
	// Turbo fast2, Aggressive fast3).
	PckKCPMode string `toml:"pck_kcp_mode"`
	// PckKCPMTU is the size of one KCP packet, which is the TCP payload of one
	// segment. Zero takes 1350, which with the headers fits a 1500-byte path.
	// The segments are not fragmented, so a value the path cannot carry is lost
	// without any message, not slow.
	PckKCPMTU int `toml:"pck_kcp_mtu"`
	// PckKCPSndWnd is the ceiling of KCP's send window in packets. The engine
	// moves the window below it by what it reads of the path's delay, so a
	// larger figure does not queue more on a path that is full, but one that is
	// too small is the speed limit: throughput is at most window x mtu /
	// round-trip time. Zero takes the preset's.
	PckKCPSndWnd int `toml:"pck_kcp_sndwnd"`
	// PckKCPRcvWnd is KCP's receive window in packets; see pck_kcp_sndwnd. Zero
	// takes the preset's.
	PckKCPRcvWnd int `toml:"pck_kcp_rcvwnd"`
	// PckKCPBlock is the cipher KCP encrypts with, keyed from token. Empty is
	// "aes". Both ends must agree. "aes-128-gcm" adds authentication; "none"
	// and "null" turn encryption off.
	PckKCPBlock string `toml:"pck_kcp_block"`
	// PckRemoteTargets, on the kharej side, lets the tunnel reach machines
	// other than the kharej server itself. Off (the default), a forwarded port
	// reaches only this server's own services — 127.0.0.1 and its own
	// addresses — which is what the previous PCK's TUN device reached too. Turn
	// it on only for a mapping that names another host ("443=10.0.0.5:443").
	PckRemoteTargets bool `toml:"pck_remote_targets"`
}

// Enabled reports whether this configuration describes a layer-3 tunnel. It is
// the single gate between the two engines: false — which is what every
// configuration written before this existed returns — means nothing in the
// layer-3 path is reachable.
func (l L3Config) Enabled() bool {
	return strings.TrimSpace(l.Mode) != ""
}

// AutoMTUEnabled reports whether the path should be measured. A pointer in the
// struct and a method here, because the answer for a key that is absent is
// "yes" — and a plain bool cannot tell an absent key from an explicit false.
func (l L3Config) AutoMTUEnabled() bool {
	return l.AutoMTU == nil || *l.AutoMTU
}

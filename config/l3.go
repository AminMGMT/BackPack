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
	// listening.
	Addr string `toml:"addr"`

	// Token is the shared secret. It is the only credential: the handshake
	// derives its pre-shared key from it, and a peer without it is answered
	// with silence.
	Token string `toml:"token"`

	// Carrier is the datagram transport underneath: "udp" (the default, and
	// the right choice on a path that does not interfere), "pck" (raw TCP
	// segments, so a capture sees an ordinary flow), "quic" (a real QUIC
	// session, so a capture sees HTTP/3), "sni" (pck, plus a TLS ClientHello
	// naming an allowed domain at the start of the flow), "xdi" (inside ICMP
	// echo) or "spoof" (raw IP with a forged source). All but udp and quic are
	// Linux-only and need CAP_NET_RAW.
	//
	// A reliable carrier is not an option here — see the l3 package doc for
	// why stacking retransmission is actively harmful rather than merely
	// wasteful.
	Carrier string `toml:"carrier"`

	// Encap is "ipip" (the default, and free) or "gre" (four bytes, or eight
	// with a key). One tunnel carries both IPv4 and IPv6 either way.
	Encap string `toml:"encap"`

	// GREKey is the RFC 2890 key, letting more than one logical tunnel share
	// a carrier. Zero omits the field. Ignored unless encap is "gre".
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
	// does not fail loudly — it passes small flows and stalls large ones.
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
	FECData   int `toml:"fec_data"`
	FECParity int `toml:"fec_parity"`

	// Paths spreads the udp carrier over this many sockets, on consecutive
	// ports starting at the one in Addr. One (or zero) is a single socket.
	//
	// It is for a path that rate-limits per flow: one socket is one flow and
	// gets one allowance however much headroom the link has. Both ends must set
	// the same number, and the extra ports must be open. The obfuscated
	// carriers do not take it — they already vary their source per packet.
	Paths int `toml:"paths"`

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
	BandwidthMbps  int `toml:"bandwidth_mbps"`

	// Embedded so the spoof_* keys sit at the top level of the [l3] table,
	// exactly as they do in [server] and [client]. They mean the same thing
	// here, and are read only when carrier = "spoof".
	SpoofConfig

	// Embedded the same way for the pck_* keys, read only when
	// carrier = "pck".
	PckConfig
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

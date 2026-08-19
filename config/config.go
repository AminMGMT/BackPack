package config

// TransportType defines the type of transport.
type TransportType string

const (
	TCP    TransportType = "tcp"
	TCPMUX TransportType = "tcpmux"
	WS     TransportType = "ws"
	WSS    TransportType = "wss"
	WSMUX  TransportType = "wsmux"
	WSSMUX TransportType = "wssmux"
	UDP    TransportType = "udp"
	KCP    TransportType = "kcp"
	// QUIC carries the tunnel inside QUIC streams over UDP. Like KCP it survives
	// paths where a long-lived TCP flow stalls, but it brings its own TLS 1.3,
	// stream multiplexing, congestion control and loss recovery — so there is
	// nothing to hand-tune and every byte is encrypted. The certificate is a
	// throwaway self-signed one; the tunnel token is the shared secret.
	QUIC TransportType = "quic"
	// STEALTH is a TCP tunnel wrapped in a Noise (NNpsk0) record layer. It has
	// no TLS fingerprint and no recognisable handshake — on the wire it is
	// indistinguishable from random — so deep packet inspection has nothing to
	// match. The pre-shared key is derived from the tunnel token.
	STEALTH TransportType = "stealth"
	// XDI carries the KCP transport inside ICMP echo instead of UDP —
	// experimental. It is for the network that filters UDP and TCP but not
	// ICMP, where the tunnel rides in ping packets. Linux only, and needs a raw
	// socket. Everything above the packet layer is identical to KCP.
	XDI TransportType = "xdi"
	// SPOOF carries the KCP transport inside raw IPv4 packets whose source
	// address is forged — experimental "IP Spoofing". It is for a path that
	// filters on the real flow's source, or that only lets a particular address
	// pair through: the datagrams route to the real peer as normal, but on the
	// wire they appear to come from spoof_src_ip. Like xdi it is KCP over a
	// hand-built raw socket, so encryption, error correction and the whole
	// tunnel stack sit on top unchanged. Linux only, needs a raw socket, and
	// only works where the upstream network does not drop forged-source packets
	// (no BCP38 egress filtering) — which must be proven on the real route.
	SPOOF TransportType = "spoof"
	// PCK carries the KCP transport inside TCP segments this process builds and
	// reads through a packet socket, instead of through the kernel's TCP stack.
	// It is a TCP transport in everything the wire can see — real source
	// address, real ports, a header with the options and numbering a Linux
	// stack produces — but no socket, no handshake and no connection state
	// exist on either host, so nothing in netfilter or connection tracking is
	// in a position to interfere with it. That is the point: on a path where a
	// kernel TCP flow is reset, throttled or dropped, this one is not visible
	// to the machinery doing it. KCP above supplies the reliability the absent
	// stack would have. Linux only, and needs root or CAP_NET_RAW.
	PCK TransportType = "pck"
)

// KCPConfig holds the tuning of the KCP transport: a reliable, retransmitting
// protocol carried inside UDP datagrams. Every field is filled from the chosen
// performance preset, so a config never has to be edited by hand.
type KCPConfig struct {
	MTU          int  `toml:"kcp_mtu"`
	Interval     int  `toml:"kcp_interval"`
	Resend       int  `toml:"kcp_resend"`
	NoDelay      int  `toml:"kcp_nodelay"`
	NoCongestion int  `toml:"kcp_nocongestion"`
	SndWnd       int  `toml:"kcp_sndwnd"`
	RcvWnd       int  `toml:"kcp_rcvwnd"`
	AckNoDelay   bool `toml:"kcp_acknodelay"`
	// DataShards/ParityShards enable forward error correction: for every
	// DataShards packets, ParityShards extra packets are sent so that many
	// losses are repaired instantly instead of waiting for a retransmit.
	DataShards   int `toml:"kcp_datashards"`
	ParityShards int `toml:"kcp_parityshards"`
}

// WithDefaults returns a copy with any unset field filled in, so a config
// written by an older version — or by hand — can never produce a KCP session
// with a zero window or a zero tick interval.
func (k KCPConfig) WithDefaults() KCPConfig {
	if k.MTU <= 0 {
		k.MTU = 1350
	}
	if k.Interval <= 0 {
		k.Interval = 20
	}
	if k.Resend < 0 {
		k.Resend = 2
	}
	if k.SndWnd <= 0 {
		k.SndWnd = 1024
	}
	if k.RcvWnd <= 0 {
		k.RcvWnd = 1024
	}
	// Parity without data shards is meaningless to the encoder, so treat a
	// half-configured pair as FEC disabled rather than failing to start.
	if k.DataShards <= 0 || k.ParityShards <= 0 {
		k.DataShards, k.ParityShards = 0, 0
	}
	return k
}

// SpoofConfig holds the IP-spoofing carrier's settings, embedded in both the
// server and client config so the spoof_* keys sit at the top level of the
// table. It only takes effect when transport = "spoof"; every field is ignored
// otherwise.
//
// The carrier forges the source address of the raw packets it sends. Routing
// still uses the real peer — the server's bind address, the client's remote
// address — so the packet actually arrives; only the source in the on-wire
// header is replaced with SpoofSrcIP. The two ends must agree on the profile
// and, where it matters, on the spoofed addresses.
type SpoofConfig struct {
	// SpoofProfile is the L4 shim wrapped around each datagram, which decides
	// what the packet looks like to inspection: "udp" (default), "icmp" (looks
	// like ping) or "tcp" (looks like a TCP flow; the receiving side auto-manages
	// an iptables rule to drop the kernel's RSTs). It sets BOTH directions unless
	// SpoofUplink/SpoofDownlink override them.
	SpoofProfile string `toml:"spoof_profile"`
	// SpoofUplink and SpoofDownlink set the profile per direction, for a path
	// whose filtering is not symmetric — e.g. ICMP survives client→server while
	// UDP survives server→client. Uplink is client→server, downlink is
	// server→client; both ends must set the same pair. Empty falls back to
	// SpoofProfile, which is the symmetric case.
	SpoofUplink   string `toml:"spoof_uplink"`
	SpoofDownlink string `toml:"spoof_downlink"`
	// SpoofSrcIP is the forged source address stamped on every outgoing packet.
	// Empty leaves the host's real source in place, which spoofs nothing.
	SpoofSrcIP string `toml:"spoof_src_ip"`
	// SpoofSrcPool is an optional list of forged sources to rotate through: each
	// time the carrier (re)connects it picks one, so the tunnel is not pinned to
	// a single address a firewall might rate-limit or block. SpoofSrcIP, if set,
	// is always a member. Empty means use SpoofSrcIP alone.
	SpoofSrcPool []string `toml:"spoof_src_pool"`
	// SpoofPeerIP is the peer's REAL IPv4 address — where the forged packets are
	// actually routed. On the server it is REQUIRED: because the client forges
	// its source, the server cannot learn where to send replies from the packets
	// themselves and must be told the client's real address. On the client it is
	// optional and defaults to the host of RemoteAddr.
	SpoofPeerIP string `toml:"spoof_peer_ip"`
	// SpoofDstIP is a forged destination written only into the cosmetic L4 shim
	// of the profiles that carry one; the packet is still routed to the real
	// peer. Empty mirrors SpoofSrcIP. Ignored by the udp profile.
	SpoofDstIP string `toml:"spoof_dst_ip"`
	// SpoofInterface pins the raw socket to a named egress device (e.g. "eth0"),
	// for a multi-homed host where the forged source would otherwise pick the
	// wrong link. Empty lets the kernel route by the real destination.
	SpoofInterface string `toml:"spoof_interface"`
	// SpoofXDPInterface, when set to a NIC name (e.g. "eth0"), attaches an XDP/eBPF
	// program to that device to receive the tunnel's forged-source packets in the
	// kernel fast path, before the normal socket stack — higher throughput and
	// lower CPU under load than the default raw-socket receive. Pure Go (no clang
	// or libbpf), opt-in, and best-effort: if the kernel is too old or the attach
	// or verifier fails, the carrier logs it and silently falls back to the
	// ordinary raw/UDP receive, so a working tunnel is never lost to it. Empty
	// disables it. Linux only; needs CAP_BPF/CAP_NET_ADMIN in addition to the
	// carrier's CAP_NET_RAW.
	SpoofXDPInterface string `toml:"spoof_xdp_interface"`
	// SpoofPipe is the legacy alias for spoof_mode = "relay": true selects relay
	// mode. Kept so configs written by older versions keep working; new configs
	// use SpoofMode. Resolved through RelayMode, never read directly.
	SpoofPipe bool `toml:"spoof_pipe"`
	// SpoofPipeAddr is the legacy alias for spoof_forward: the relay mode's local
	// UDP target. Kept for old configs; resolved through RelayForward.
	SpoofPipeAddr string `toml:"spoof_pipe_addr"`
	// SpoofMode selects the carrier's shape: "kcp" (the default) wraps a reliable,
	// encrypted KCP tunnel over the forged-source channel; "relay" strips KCP and
	// runs a bare bidirectional datagram relay between a local UDP socket and the
	// channel — the spoof-tunnel model, for carrying something that brings its own
	// reliability (WireGuard, or another tunnel). Empty means "kcp", unless the
	// legacy SpoofPipe is set, which is treated as "relay". See RelayMode.
	SpoofMode string `toml:"spoof_mode"`
	// SpoofForward is the relay mode's local UDP target: on the client the socket
	// the tunnel listens on (point the inner app's endpoint here); on the server
	// the socket datagrams out of the tunnel are forwarded to (where the inner
	// service listens). Empty falls back to SpoofPipeAddr, then 127.0.0.1:51820.
	// Ignored in kcp mode. See RelayForward.
	SpoofForward string `toml:"spoof_forward"`
	// SpoofSockBuf sizes the send and receive socket buffers (SO_SNDBUF /
	// SO_RCVBUF) of the raw and UDP sockets the carrier owns, in bytes. A large
	// buffer is what lets the forged-source flow reach real bandwidth: under a
	// burst the kernel parks packets here instead of dropping them before the
	// read loop drains them, which is exactly what throttled the throughput
	// before. 0 uses the carrier default (4 MiB), matching the reference
	// spoof-tunnel; raise it on a fat, high-latency path.
	SpoofSockBuf int `toml:"spoof_sockbuf"`
	// SpoofPeerSrcIP pins the forged source the peer stamps on its packets, so
	// anything arriving with a different source is dropped before the encryption
	// ever looks at it. Empty accepts any source and leaves the demux to the port
	// and the encryption, which is safe but noisier. Set it to the peer's
	// spoof_src_ip for a tighter, cheaper receive path.
	SpoofPeerSrcIP string `toml:"spoof_peer_src_ip"`
	// SpoofICMPReply makes an icmp/icmpv6 tunnel look like a real ping exchange:
	// the client sends Echo Requests and the server answers with Echo Replies,
	// instead of both ends sending Requests. Purely cosmetic camouflage; both
	// ends must set it the same. Ignored by the udp/tcp profiles.
	SpoofICMPReply bool `toml:"spoof_icmp_reply"`
	// SpoofMTU is the largest IP packet the carrier emits before it fragments in
	// userspace; a datagram whose headers push it over this is split into IP
	// fragments the peer's kernel reassembles. 0 uses 1500. Lower it on a path
	// with a smaller MTU so oversize packets fragment cleanly rather than being
	// dropped.
	SpoofMTU int `toml:"spoof_mtu"`

	// The DPI-evasion knobs below are optional obfuscation ported from the
	// reference spooftunnel. The header cosmetics (ttl/dscp/source-port) need no
	// agreement between the two ends because the receiver ignores those fields;
	// the wire-changing ones (padding, fake TLS) must be set the same on both.

	// SpoofTTLJitter varies the IP TTL per packet across a pool of realistic OS
	// defaults {64,128,255} instead of a fixed 64, to blur TTL-based fingerprints.
	SpoofTTLJitter bool `toml:"spoof_ttl_jitter"`
	// SpoofRandomDSCP varies the IP DSCP/ToS byte per packet across plausible
	// values instead of leaving it 0.
	SpoofRandomDSCP bool `toml:"spoof_random_dscp"`
	// SpoofShufflePort randomises the L4 SOURCE port per packet (udp/tcp) within
	// [SpoofPortMin,SpoofPortMax], so the flow does not sit on one source port.
	// The destination port stays fixed, so the receiver's demux is unaffected.
	SpoofShufflePort bool `toml:"spoof_shuffle_port"`
	SpoofPortMin     int  `toml:"spoof_port_min"`
	SpoofPortMax     int  `toml:"spoof_port_max"`
	// SpoofPadding appends 1..SpoofPaddingMax random bytes to every payload
	// (self-describing, so the receiver strips them), defeating size fingerprints.
	// Both ends must set it the same.
	SpoofPadding    bool `toml:"spoof_padding"`
	SpoofPaddingMax int  `toml:"spoof_padding_max"`
	// SpoofFakeTLS prepends a fake TLS 1.2 record header to each TCP segment, so a
	// middlebox reads it as TLS. TCP profile only; both ends must agree.
	SpoofFakeTLS bool `toml:"spoof_fake_tls"`
}

// PckConfig holds the packet-level TCP carrier's settings. Every field is
// optional: the transport works out its own egress from the route to the peer,
// and the defaults are what an ordinary data-carrying connection looks like.
// They exist to correct a wrong guess on an unusual host, not to be filled in.
type PckConfig struct {
	// PckInterface pins the carrier to a named egress device. Empty — the
	// normal case — lets the route to the peer choose, which is right on every
	// host with one uplink and on most with several.
	PckInterface string `toml:"pck_interface"`
	// PckGatewayMAC is the next hop's hardware address, used when frames are
	// injected at the link layer. Empty means read it from the kernel's
	// neighbour table, which is where it already is. Set it only where that
	// lookup is wrong — some virtualised networks answer ARP with an address
	// the hypervisor then rewrites.
	PckGatewayMAC string `toml:"pck_gateway_mac"`
	// PckFlags is the cycle of TCP flag combinations stamped on outgoing
	// segments, one per packet, spelled as in tcpdump: ["PA"] is push+ack, the
	// flags bulk data carries and the default. A longer cycle varies the
	// pattern for a path that matches on it. Both ends may differ — each side
	// only decides what it sends.
	PckFlags []string `toml:"pck_flags"`
}

// ServerConfig represents the configuration for the server.
type ServerConfig struct {
	BindAddr         string        `toml:"bind_addr"`
	Transport        TransportType `toml:"transport"`
	Token            string        `toml:"token"`
	Nodelay          bool          `toml:"nodelay"`
	Keepalive        int           `toml:"keepalive_period"`
	ChannelSize      int           `toml:"channel_size"`
	LogLevel         string        `toml:"log_level"`
	LogFormat        string        `toml:"log_format"` // "" (text) or "json"
	Ports            []string      `toml:"ports"`
	PPROF            bool          `toml:"pprof"`
	MuxSession       int           `toml:"mux_session"`
	MuxVersion       int           `toml:"mux_version"`
	MaxFrameSize     int           `toml:"mux_framesize"`
	MaxReceiveBuffer int           `toml:"mux_recievebuffer"`
	MaxStreamBuffer  int           `toml:"mux_streambuffer"`
	Sniffer          bool          `toml:"sniffer"`
	WebPort          int           `toml:"web_port"`
	// WebBind is the address the sniffer/monitor page listens on. It has no
	// authentication of any kind and reports the host's CPU, memory, disk and
	// network along with the tunnel's status and per-port traffic, so it
	// defaults to 127.0.0.1 and is reached over an SSH tunnel:
	//   ssh -L 2060:127.0.0.1:2060 root@server
	// Set it to 0.0.0.0 to serve it on every interface as it used to be, or
	// to one address to serve it on a private network only.
	WebBind     string `toml:"web_bind"`
	SnifferLog  string `toml:"sniffer_log"`
	TLSCertFile string `toml:"tls_cert"`
	TLSKeyFile  string `toml:"tls_key"`
	// ACMEDomain switches wss/wssmux to a Let's Encrypt certificate for this
	// domain instead of the generated self-signed one. The domain must resolve
	// to this server. Empty keeps the self-signed certificate.
	ACMEDomain string `toml:"acme_domain"`
	ACMEEmail  string `toml:"acme_email"`
	// SimpleAuth authorises a wss tunnel by the raw token instead of a proof
	// bound to the TLS session. It exists for one deployment the binding
	// otherwise makes impossible: a TLS-terminating reverse proxy — typically
	// NGINX — in front of the tunnel, which holds a different TLS session from
	// the client so a bound proof can never match. It is off by default because
	// it hands the token to whoever terminates the TLS; turn it on only when a
	// trusted proxy is doing so, and set it on both ends.
	SimpleAuth bool `toml:"simple_auth"`
	Heartbeat  int  `toml:"heartbeat"`
	MuxCon     int  `toml:"mux_con"`
	// AcceptUDP turns UDP forwarding on for the exposed ports. It is off unless
	// set: a forwarded port carries TCP only until the operator asks for UDP as
	// well. The pointer distinguishes "not set" (off) from an explicit
	// accept_udp = true, so a config with no line does not forward UDP.
	AcceptUDP *bool `toml:"accept_udp"`
	SkipOptz  bool  `toml:"skip_optz"`
	MSS       int   `toml:"mss"`
	SO_RCVBUF int   `toml:"so_rcvbuf"`
	SO_SNDBUF int   `toml:"so_sndbuf"`
	// SOPinTCP restores the old behaviour of pinning SO_RCVBUF/SO_SNDBUF on
	// TCP sockets. Off by default: pinning them stops the kernel auto-tuning
	// the window, which costs a large multiple of the throughput on a fast
	// uplink. The datagram transports set their own buffers regardless.
	SOPinTCP bool `toml:"so_pin_tcp"`
	// ZeroCopy lets the kernel move the bytes of forwarded connections
	// directly between the two sockets, without them passing through this
	// process. It is faster and it is the least proven path here, so it is off
	// by default and turned on per tunnel.
	//
	// Purely local: nothing about it reaches the wire, so the two ends need not
	// agree and it is safe to enable on one side first. It applies only to the
	// plain `tcp` transport on Linux, and only when the tunnel has no bandwidth
	// limit — anything else quietly keeps the buffered path.
	ZeroCopy bool `toml:"zero_copy"`

	ProxyProtocol bool `toml:"proxy_protocol"`
	// MaxConnections caps simultaneous forwarded connections (0 = unlimited).
	MaxConnections int `toml:"max_connections"`
	// BandwidthMbps caps total tunnel throughput in Mbit/s (0 = unlimited).
	BandwidthMbps int    `toml:"bandwidth_mbps"`
	Preset        string `toml:"preset"`
	// Embedded so the kcp_* keys sit at the top level of the [server] table
	// alongside every other tuning key.
	KCPConfig
	// Embedded so the spoof_* keys sit at the top level too. Only used when
	// transport = "spoof".
	SpoofConfig
	// Embedded so the pck_* keys sit at the top level too. Only used when
	// transport = "pck".
	PckConfig
}

// ForwardsUDP reports whether the forwarded ports should carry UDP as well as
// TCP. It is off unless the operator turns it on.
//
// It was briefly the other way — on unless turned off — so that Xray and
// Shadowsocks UDP would work without a hidden switch. That default did more harm
// than the problem it solved: a browser's QUIC is UDP on port 443, so every web
// tunnel silently began carrying every QUIC flow, and on the connection-pooled
// transports (ws/wss and the mux family) those long-lived flows each hold a
// pooled connection for as long as the browser keeps them — which starves the
// TCP forwards sharing the pool. The visible symptom was a site half-loading
// (images stalled while audio played) that a restart fixed for a while. So the
// default is back to off: the tunnels that genuinely need UDP — a VPN, a game,
// an Xray inbound — turn it on, and a plain web or proxy tunnel is not made to
// carry traffic nobody asked it to.
func (s ServerConfig) ForwardsUDP() bool {
	return s.AcceptUDP != nil && *s.AcceptUDP
}

// ClientConfig represents the configuration for the client.
type ClientConfig struct {
	RemoteAddr string `toml:"remote_addr"`
	// FallbackAddrs are additional server addresses tried in order whenever the
	// primary cannot be reached (a filtered IP, a blocked port, a CDN edge).
	FallbackAddrs    []string      `toml:"fallback_addrs"`
	Transport        TransportType `toml:"transport"`
	Token            string        `toml:"token"`
	ConnectionPool   int           `toml:"connection_pool"`
	RetryInterval    int           `toml:"retry_interval"`
	Nodelay          bool          `toml:"nodelay"`
	Keepalive        int           `toml:"keepalive_period"`
	LogLevel         string        `toml:"log_level"`
	LogFormat        string        `toml:"log_format"` // "" (text) or "json"
	PPROF            bool          `toml:"pprof"`
	MuxSession       int           `toml:"mux_session"`
	MuxVersion       int           `toml:"mux_version"`
	MaxFrameSize     int           `toml:"mux_framesize"`
	MaxReceiveBuffer int           `toml:"mux_recievebuffer"`
	MaxStreamBuffer  int           `toml:"mux_streambuffer"`
	Sniffer          bool          `toml:"sniffer"`
	WebPort          int           `toml:"web_port"`
	// WebBind is the address the sniffer/monitor page listens on. It has no
	// authentication of any kind and reports the host's CPU, memory, disk and
	// network along with the tunnel's status and per-port traffic, so it
	// defaults to 127.0.0.1 and is reached over an SSH tunnel:
	//   ssh -L 2060:127.0.0.1:2060 root@server
	// Set it to 0.0.0.0 to serve it on every interface as it used to be, or
	// to one address to serve it on a private network only.
	WebBind        string `toml:"web_bind"`
	SnifferLog     string `toml:"sniffer_log"`
	DialTimeout    int    `toml:"dial_timeout"`
	AggressivePool bool   `toml:"aggressive_pool"`
	EdgeIP         string `toml:"edge_ip"`
	// SimpleAuth authorises a wss tunnel by the raw token instead of a proof
	// bound to the TLS session. It exists for one deployment the binding
	// otherwise makes impossible: a TLS-terminating reverse proxy — typically
	// NGINX — in front of the tunnel, which holds a different TLS session from
	// the client so a bound proof can never match. It is off by default because
	// it hands the token to whoever terminates the TLS; turn it on only when a
	// trusted proxy is doing so, and set it on both ends.
	SimpleAuth bool `toml:"simple_auth"`
	SkipOptz   bool `toml:"skip_optz"`
	MSS        int  `toml:"mss"`
	SO_RCVBUF  int  `toml:"so_rcvbuf"`
	SO_SNDBUF  int  `toml:"so_sndbuf"`
	// Proxy routes the connection to the tunnel server through a local or
	// nearby proxy, for a client that cannot open an arbitrary outbound
	// connection itself. One URL: "socks5://127.0.0.1:1080" or
	// "http://user:pass@10.0.0.1:8080". Empty means dial the server directly.
	//
	// It applies only to the connections that reach the server. The dial to the
	// local backend never goes through it — that traffic does not leave the
	// machine, so sending it out and back would be both slower and wrong.
	Proxy string `toml:"proxy"`
	// LocalAddr binds the connections that reach the server to a chosen source
	// address, which on a machine with more than one uplink is what decides
	// which of them the tunnel leaves by. An address on its own is enough; the
	// port is the kernel's to pick. Needs no privilege.
	LocalAddr string `toml:"local_addr"`
	// Interface pins those connections to a named device, for when the source
	// address alone does not settle the route. Linux, and needs CAP_NET_RAW.
	Interface string `toml:"interface"`
	// SOMark stamps an fwmark on their packets, which is what `ip rule` matches
	// on — the way to put the tunnel on a routing table of its own without
	// changing routing for the rest of the machine. Linux, and needs
	// CAP_NET_ADMIN. Zero means none.
	SOMark int `toml:"so_mark"`
	// SOPinTCP restores the old behaviour of pinning SO_RCVBUF/SO_SNDBUF on
	// TCP sockets. Off by default: pinning them stops the kernel auto-tuning
	// the window, which costs a large multiple of the throughput on a fast
	// uplink. The datagram transports set their own buffers regardless.
	SOPinTCP bool `toml:"so_pin_tcp"`
	// ZeroCopy lets the kernel move the bytes of forwarded connections
	// directly between the two sockets, without them passing through this
	// process. It is faster and it is the least proven path here, so it is off
	// by default and turned on per tunnel.
	//
	// Purely local: nothing about it reaches the wire, so the two ends need not
	// agree and it is safe to enable on one side first. It applies only to the
	// plain `tcp` transport on Linux, and only when the tunnel has no bandwidth
	// limit — anything else quietly keeps the buffered path.
	ZeroCopy bool `toml:"zero_copy"`

	Preset string `toml:"preset"`
	// LoadBalance spreads the pool's data connections over every configured
	// address instead of putting them all on the live one. All the addresses
	// must reach the SAME server, since the control channel — and therefore
	// the tunnel's identity — lives on one of them.
	LoadBalance bool `toml:"load_balance"`
	// HealthFailover scores every configured address on a timer and keeps
	// traffic on the healthiest — the multi-exit gaming behaviour. It needs more
	// than one address to do anything, and it overrides LoadBalance, because
	// steering to one best exit is the opposite of spreading across all of them.
	HealthFailover bool `toml:"health_failover"`
	// Embedded so the kcp_* keys sit at the top level of the [client] table
	// alongside every other tuning key.
	KCPConfig
	// Embedded so the spoof_* keys sit at the top level too. Only used when
	// transport = "spoof".
	SpoofConfig
	// Embedded so the pck_* keys sit at the top level too. Only used when
	// transport = "pck".
	PckConfig
}

// Config represents the complete configuration, including both server and client settings.
type Config struct {
	Server ServerConfig `toml:"server"`
	Client ClientConfig `toml:"client"`
	// L3 is a direct layer-3 tunnel, and is present only in a configuration
	// that asks for one. It shares nothing with Server and Client: a file
	// without an [l3] table leaves this zero, L3.Enabled() reads false, and
	// the reverse tunnel runs exactly as it always has. See config/l3.go.
	L3 L3Config `toml:"l3"`
	// Direct is a direct layer-4 tunnel — the same forwarded ports, dialled
	// the other way round. Present only in a configuration that asks for one,
	// on the same terms as L3 above. See config/direct.go.
	Direct DirectConfig `toml:"direct"`
}

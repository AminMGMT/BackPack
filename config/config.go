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
	// SpoofPipe switches the spoof transport from a KCP tunnel to a raw UDP pipe
	// for WireGuard: instead of forwarding ports, it relays datagrams between a
	// local WireGuard socket and the forged-source channel, so a whole-device VPN
	// rides over it. WireGuard supplies its own encryption and loss handling, so
	// no KCP sits underneath. Ports/mux settings are ignored in this mode.
	SpoofPipe bool `toml:"spoof_pipe"`
	// SpoofPipeAddr is this host's WireGuard UDP endpoint. On the client it is
	// where the tunnel listens and where WireGuard's `endpoint` points; on the
	// server it is where the real WireGuard listens and datagrams are forwarded.
	// Defaults to 127.0.0.1:51820.
	SpoofPipeAddr string `toml:"spoof_pipe_addr"`
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
	SnifferLog       string        `toml:"sniffer_log"`
	TLSCertFile      string        `toml:"tls_cert"`
	TLSKeyFile       string        `toml:"tls_key"`
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
	AcceptUDP  bool `toml:"accept_udp"`
	SkipOptz   bool `toml:"skip_optz"`
	MSS        int  `toml:"mss"`
	SO_RCVBUF  int  `toml:"so_rcvbuf"`
	SO_SNDBUF  int  `toml:"so_sndbuf"`
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
	SnifferLog       string        `toml:"sniffer_log"`
	DialTimeout      int           `toml:"dial_timeout"`
	AggressivePool   bool          `toml:"aggressive_pool"`
	EdgeIP           string        `toml:"edge_ip"`
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
	// Embedded so the kcp_* keys sit at the top level of the [client] table
	// alongside every other tuning key.
	KCPConfig
	// Embedded so the spoof_* keys sit at the top level too. Only used when
	// transport = "spoof".
	SpoofConfig
}

// Config represents the complete configuration, including both server and client settings.
type Config struct {
	Server ServerConfig `toml:"server"`
	Client ClientConfig `toml:"client"`
}

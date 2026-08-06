package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// EngineType selects the implementation that owns an instance.  An empty
// value is intentionally meaningful: it is the legacy spelling of reverse.
type EngineType string

const (
	EngineReverse EngineType = "reverse"
	// EngineForward keeps Backpack's application-level tunnel and selected
	// transport, but reverses who establishes it: the Iran edge dials the
	// Kharej origin.  It is intentionally distinct from EngineIPTables, which
	// is a kernel-only DNAT engine and does not carry a Backpack transport.
	EngineForward  EngineType = "forward"
	EngineIPTables EngineType = "iptables"
)

const (
	MaxPortsPerMapping  = 1024
	MaxPortsPerInstance = 4096
)

// ForwardMapping is one offset-preserving direct forwarding rule.
type ForwardMapping struct {
	ListenAddress string   `toml:"listen_address" json:"listenAddress"`
	ListenPorts   string   `toml:"listen_ports" json:"listenPorts"`
	TargetAddress string   `toml:"target_address" json:"targetAddress"`
	TargetPorts   string   `toml:"target_ports" json:"targetPorts"`
	Protocols     []string `toml:"protocols" json:"protocols"`
}

type ForwardConfig struct {
	Mappings []ForwardMapping `toml:"mappings" json:"mappings"`
}

// PortRange is the normalised inclusive form used by the netfilter engine.
type PortRange struct{ Start, End uint16 }

func (r PortRange) Len() int { return int(r.End-r.Start) + 1 }

func ParsePortRange(raw string) (PortRange, error) {
	parts := strings.Split(strings.TrimSpace(raw), "-")
	if len(parts) < 1 || len(parts) > 2 {
		return PortRange{}, fmt.Errorf("invalid port range %q", raw)
	}
	parse := func(s string) (uint16, error) {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 1 || n > 65535 {
			return 0, fmt.Errorf("invalid port %q", s)
		}
		return uint16(n), nil
	}
	lo, err := parse(parts[0])
	if err != nil {
		return PortRange{}, err
	}
	hi := lo
	if len(parts) == 2 {
		hi, err = parse(parts[1])
		if err != nil {
			return PortRange{}, err
		}
	}
	if hi < lo {
		return PortRange{}, fmt.Errorf("port range %q ends before it starts", raw)
	}
	return PortRange{Start: lo, End: hi}, nil
}

func (m ForwardMapping) Ranges() (PortRange, PortRange, error) {
	l, err := ParsePortRange(m.ListenPorts)
	if err != nil {
		return PortRange{}, PortRange{}, fmt.Errorf("listen_ports: %w", err)
	}
	t, err := ParsePortRange(m.TargetPorts)
	if err != nil {
		return PortRange{}, PortRange{}, fmt.Errorf("target_ports: %w", err)
	}
	if l.Len() != t.Len() {
		return PortRange{}, PortRange{}, fmt.Errorf("listen and target ranges must contain the same number of ports")
	}
	return l, t, nil
}

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
	// Ports is used only by the forward engine.  In that mode the dialling
	// client is the Iran edge, so it also owns the public ingress listeners.
	// Reverse client configs omit it and retain their historical meaning.
	Ports []string `toml:"ports"`
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
	// The following ingress controls are meaningful on the dialling side only
	// for EngineForward. They mirror the long-standing reverse server knobs.
	AcceptUDP      bool `toml:"accept_udp"`
	ProxyProtocol  bool `toml:"proxy_protocol"`
	MaxConnections int  `toml:"max_connections"`
	BandwidthMbps  int  `toml:"bandwidth_mbps"`

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
	Engine  EngineType    `toml:"engine"`
	Server  ServerConfig  `toml:"server"`
	Client  ClientConfig  `toml:"client"`
	Forward ForwardConfig `toml:"forward"`

	sections sectionPresence `toml:"-"`
}

type sectionPresence struct{ server, client, forward bool }

// LoadFile is the canonical decoder. Besides decoding values it records table
// presence, which is required to distinguish an absent table from an empty but
// invalid one.
func LoadFile(path string) (*Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return &c, err
	}
	c.sections = sectionPresence{
		server: md.IsDefined("server"), client: md.IsDefined("client"), forward: md.IsDefined("forward"),
	}
	if err := c.ValidateStructure(); err != nil {
		return &c, err
	}
	return &c, nil
}

// EffectiveEngine preserves the meaning of every pre-engine configuration.
func (c *Config) EffectiveEngine() EngineType {
	if c.Engine == "" {
		return EngineReverse
	}
	return c.Engine
}

func (c *Config) HasForward() bool { return c.sections.forward || len(c.Forward.Mappings) > 0 }
func (c *Config) HasServer() bool  { return c.sections.server || c.Server.BindAddr != "" }
func (c *Config) HasClient() bool  { return c.sections.client || c.Client.RemoteAddr != "" }

// ValidateStructure validates the engine/section matrix and portable direct
// mapping rules. It is deliberately side-effect free.
func (c *Config) ValidateStructure() error {
	hasServer, hasClient, hasForward := c.HasServer(), c.HasClient(), c.HasForward()
	if hasServer && hasClient {
		return fmt.Errorf("[server] and [client] cannot exist together")
	}
	if hasForward && (hasServer || hasClient) {
		return fmt.Errorf("[forward] cannot exist with [server] or [client]")
	}
	switch c.Engine {
	case "":
		if hasForward {
			return fmt.Errorf("[forward] requires engine = %q", EngineIPTables)
		}
		if hasServer == hasClient {
			return fmt.Errorf("a reverse instance requires exactly one of [server] or [client]")
		}
	case EngineReverse:
		if hasForward || hasServer == hasClient {
			return fmt.Errorf("engine %q requires exactly one of [server] or [client]", EngineReverse)
		}
	case EngineForward:
		if hasForward || hasServer == hasClient {
			return fmt.Errorf("engine %q requires exactly one of [server] or [client]", EngineForward)
		}
		// Operational roles are deliberately used here: [client] is the Iran
		// dialler and therefore owns ingress ports; [server] is the Kharej
		// listener and never exposes ports itself.
		if hasClient {
			if strings.TrimSpace(c.Client.RemoteAddr) == "" {
				return fmt.Errorf("engine %q [client] requires remote_addr", EngineForward)
			}
			if len(c.Client.Ports) == 0 {
				return fmt.Errorf("engine %q Iran [client] requires at least one ingress port mapping", EngineForward)
			}
		} else if strings.TrimSpace(c.Server.BindAddr) == "" {
			return fmt.Errorf("engine %q Kharej [server] requires bind_addr", EngineForward)
		}
	case EngineIPTables:
		if !hasForward || hasServer || hasClient {
			return fmt.Errorf("engine %q requires [forward] and no reverse section", EngineIPTables)
		}
	default:
		return fmt.Errorf("unknown engine %q", c.Engine)
	}
	if c.EffectiveEngine() == EngineIPTables {
		return ValidateForward(c.Forward)
	}
	return nil
}

func ValidateForward(f ForwardConfig) error {
	if len(f.Mappings) == 0 {
		return fmt.Errorf("[forward] requires at least one mapping")
	}
	total := 0
	type tuple struct {
		family, proto, addr string
		ports               PortRange
	}
	var seen []tuple
	for i, m := range f.Mappings {
		prefix := fmt.Sprintf("forward mapping %d", i+1)
		listen, target := net.ParseIP(strings.TrimSpace(m.ListenAddress)), net.ParseIP(strings.TrimSpace(m.TargetAddress))
		if listen == nil {
			return fmt.Errorf("%s: listen_address must be an explicit IPv4 or IPv6 address", prefix)
		}
		if target == nil {
			return fmt.Errorf("%s: target_address must be an explicit IPv4 or IPv6 address", prefix)
		}
		lf, tf := "ipv6", "ipv6"
		if listen.To4() != nil {
			lf = "ipv4"
		}
		if target.To4() != nil {
			tf = "ipv4"
		}
		if lf != tf {
			return fmt.Errorf("%s: listen and target addresses must use the same family", prefix)
		}
		if target.IsUnspecified() || target.IsMulticast() || target.IsLoopback() || target.IsInterfaceLocalMulticast() || target.IsLinkLocalMulticast() {
			return fmt.Errorf("%s: target_address must be a non-loopback unicast address", prefix)
		}
		if v4 := target.To4(); v4 != nil && v4.Equal(net.IPv4bcast) {
			return fmt.Errorf("%s: IPv4 broadcast targets are not supported", prefix)
		}
		lr, _, err := m.Ranges()
		if err != nil {
			return fmt.Errorf("%s: %w", prefix, err)
		}
		if lr.Len() > MaxPortsPerMapping {
			return fmt.Errorf("%s expands to %d ports; maximum per mapping is %d", prefix, lr.Len(), MaxPortsPerMapping)
		}
		total += lr.Len()
		if total > MaxPortsPerInstance {
			return fmt.Errorf("forward instance expands to %d ports; maximum is %d", total, MaxPortsPerInstance)
		}
		if len(m.Protocols) == 0 {
			return fmt.Errorf("%s: at least one protocol is required", prefix)
		}
		protos := map[string]bool{}
		for _, raw := range m.Protocols {
			p := strings.ToLower(strings.TrimSpace(raw))
			if p != "tcp" && p != "udp" {
				return fmt.Errorf("%s: unsupported protocol %q (use tcp or udp)", prefix, raw)
			}
			if protos[p] {
				return fmt.Errorf("%s: protocol %q is duplicated", prefix, p)
			}
			protos[p] = true
			addr := listen.String()
			wild := listen.IsUnspecified()
			for _, old := range seen {
				if old.family != lf || old.proto != p || old.ports.End < lr.Start || lr.End < old.ports.Start {
					continue
				}
				if wild || old.addr == "*" || old.addr == addr {
					return fmt.Errorf("%s overlaps an earlier %s mapping on %s ports %d-%d", prefix, p, lf, lr.Start, lr.End)
				}
			}
			if wild {
				addr = "*"
			}
			seen = append(seen, tuple{family: lf, proto: p, addr: addr, ports: lr})
		}
	}
	return nil
}

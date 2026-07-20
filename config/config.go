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
	// STEALTH is a TCP tunnel wrapped in a Noise (NNpsk0) record layer. It has
	// no TLS fingerprint and no recognisable handshake — on the wire it is
	// indistinguishable from random — so deep packet inspection has nothing to
	// match. The pre-shared key is derived from the tunnel token.
	STEALTH TransportType = "stealth"
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
	ACMEDomain    string `toml:"acme_domain"`
	ACMEEmail     string `toml:"acme_email"`
	Heartbeat     int    `toml:"heartbeat"`
	MuxCon        int    `toml:"mux_con"`
	AcceptUDP     bool   `toml:"accept_udp"`
	SkipOptz      bool   `toml:"skip_optz"`
	MSS           int    `toml:"mss"`
	SO_RCVBUF     int    `toml:"so_rcvbuf"`
	SO_SNDBUF     int    `toml:"so_sndbuf"`
	ProxyProtocol bool   `toml:"proxy_protocol"`
	// MaxConnections caps simultaneous forwarded connections (0 = unlimited).
	MaxConnections int `toml:"max_connections"`
	// BandwidthMbps caps total tunnel throughput in Mbit/s (0 = unlimited).
	BandwidthMbps int    `toml:"bandwidth_mbps"`
	Preset        string `toml:"preset"`
	// Embedded so the kcp_* keys sit at the top level of the [server] table
	// alongside every other tuning key.
	KCPConfig
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
	SkipOptz         bool          `toml:"skip_optz"`
	MSS              int           `toml:"mss"`
	SO_RCVBUF        int           `toml:"so_rcvbuf"`
	SO_SNDBUF        int           `toml:"so_sndbuf"`
	Preset           string        `toml:"preset"`
	// LoadBalance spreads the pool's data connections over every configured
	// address instead of putting them all on the live one. All the addresses
	// must reach the SAME server, since the control channel — and therefore
	// the tunnel's identity — lives on one of them.
	LoadBalance bool `toml:"load_balance"`
	// Embedded so the kcp_* keys sit at the top level of the [client] table
	// alongside every other tuning key.
	KCPConfig
}

// Config represents the complete configuration, including both server and client settings.
type Config struct {
	Server ServerConfig `toml:"server"`
	Client ClientConfig `toml:"client"`
}

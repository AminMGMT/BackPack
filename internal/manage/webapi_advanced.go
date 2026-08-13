package manage

import (
	"fmt"
	"net"
	"strings"

	"github.com/backpack/backpack/internal/utils/network"
)

// The advanced halves of the panel's setup and edit forms: the IP-spoofing
// carrier, the packet-level TCP carrier, the client's connectivity options and
// the per-tunnel limits.
//
// The CLI asks all of these — askSpoof and askPck during setup, and the Edit
// screen's own entries afterwards — but a terminal asks one question at a time
// and a form asks them all at once. As everywhere else in this package, the
// answers are validated here and then written onto the same TunnelSpec the
// wizard fills, so a tunnel built in the browser and a tunnel built over SSH
// are the same tunnel.

// spoofProfiles are the three packet profiles the carrier can wear. Both ends
// must be on the same one; see askSpoofProfile for the descriptions the CLI
// prints beside them.
var spoofProfiles = []string{"udp", "icmp", "tcp"}

// SpoofProfileOption is one packet profile for the panel's spoof menus.
type SpoofProfileOption struct {
	Label string `json:"label"`
	Desc  string `json:"desc"`
	Value string `json:"value"`
}

// SpoofProfiles returns the packet profiles with the same descriptions the CLI
// shows, so the two menus cannot describe the same thing differently.
func SpoofProfiles() []SpoofProfileOption {
	return []SpoofProfileOption{
		{Label: "UDP", Desc: "most compatible — plain datagrams (recommended)", Value: "udp"},
		{Label: "ICMP", Desc: "looks like ping traffic — good where UDP is filtered", Value: "icmp"},
		{Label: "TCP", Desc: "looks like a TCP flow — auto-manages an iptables RST rule", Value: "tcp"},
	}
}

// PckFlagOption is one TCP flag cycle for the packet carrier's menu.
type PckFlagOption struct {
	Value string `json:"value"`
	Desc  string `json:"desc"`
}

// PckFlagCycles returns the suggested flag patterns, the same list the CLI's
// "TCP packet flags" screen offers.
func PckFlagCycles() []PckFlagOption {
	src := network.SuggestedTCPFlagCycles()
	out := make([]PckFlagOption, len(src))
	for i, o := range src {
		out[i] = PckFlagOption{Value: o.Value, Desc: o.Desc}
	}
	return out
}

// RoutableInterfaces lists the network devices a raw socket or a tunnel can be
// pinned to — every interface that is up, is not the loopback and has an
// address. The CLI prints this list beside its "Interface" prompts; the panel
// turns it into a menu so a device name cannot be mistyped.
func RoutableInterfaces() []string { return routableInterfaces() }

// SpoofTune is every setting the IP-spoofing carrier has. The first block is
// what the setup wizard asks for; the rest are the obfuscation and sizing knobs
// that until now could only be set by editing the config by hand.
//
// SrcIPs is a comma-separated list rather than a slice because that is how the
// wizard asks for it and how the field reads in a form: one address, or several
// to rotate through per session.
type SpoofTune struct {
	Profile  string `json:"profile"`  // udp (default), icmp or tcp — both directions
	Uplink   string `json:"uplink"`   // client→server override, empty = symmetric
	Downlink string `json:"downlink"` // server→client override, empty = symmetric

	SrcIPs    string `json:"srcIPs"`    // forged source(s), comma separated
	PeerIP    string `json:"peerIP"`    // the peer's REAL IPv4 — required on the server
	PeerSrcIP string `json:"peerSrcIP"` // the forged source expected from the peer
	DstIP     string `json:"dstIP"`     // forged destination in the cosmetic shim
	Interface string `json:"interface"` // egress device for the raw socket

	Pipe     bool   `json:"pipe"`     // WireGuard-pipe mode instead of a KCP tunnel
	PipeAddr string `json:"pipeAddr"` // this host's WireGuard UDP endpoint

	SockBuf   int  `json:"sockBuf"`   // SO_SNDBUF/SO_RCVBUF for the carrier's sockets
	MTU       int  `json:"mtu"`       // fragment sends larger than this
	ICMPReply bool `json:"icmpReply"` // icmp: client asks, server replies

	TTLJitter   bool `json:"ttlJitter"`
	RandomDSCP  bool `json:"randomDSCP"`
	ShufflePort bool `json:"shufflePort"`
	PortMin     int  `json:"portMin"`
	PortMax     int  `json:"portMax"`
	Padding     bool `json:"padding"`
	PaddingMax  int  `json:"paddingMax"`
	FakeTLS     bool `json:"fakeTLS"`
}

// spoofOf reads a tunnel's current spoof settings off its spec, so the panel's
// drawer opens on what the tunnel actually runs.
func spoofOf(s TunnelSpec) SpoofTune {
	src := s.SpoofSrcIP
	if len(s.SpoofSrcPool) > 0 {
		src = strings.Join(s.SpoofSrcPool, ", ")
	}
	return SpoofTune{
		Profile:     s.SpoofProfile,
		Uplink:      s.SpoofUplink,
		Downlink:    s.SpoofDownlink,
		SrcIPs:      src,
		PeerIP:      s.SpoofPeerIP,
		PeerSrcIP:   s.SpoofPeerSrcIP,
		DstIP:       s.SpoofDstIP,
		Interface:   s.SpoofInterface,
		Pipe:        s.SpoofPipe,
		PipeAddr:    s.SpoofPipeAddr,
		SockBuf:     s.SpoofSockBuf,
		MTU:         s.SpoofMTU,
		ICMPReply:   s.SpoofICMPReply,
		TTLJitter:   s.SpoofTTLJitter,
		RandomDSCP:  s.SpoofRandomDSCP,
		ShufflePort: s.SpoofShufflePort,
		PortMin:     s.SpoofPortMin,
		PortMax:     s.SpoofPortMax,
		Padding:     s.SpoofPadding,
		PaddingMax:  s.SpoofPaddingMax,
		FakeTLS:     s.SpoofFakeTLS,
	}
}

// apply validates the spoof answers and writes them onto a spec. A bad address
// is refused rather than skipped with a warning: the wizard can print a warning
// and ask again, a form that silently dropped a field would leave the operator
// looking at a tunnel configured differently from the page in front of them.
func (f SpoofTune) apply(s *TunnelSpec) error {
	if s.Transport != "spoof" {
		return nil
	}

	profile, err := spoofProfile("packet profile", f.Profile)
	if err != nil {
		return err
	}
	if profile == "" {
		profile = "udp" // the wizard's recommendation, and the engine's default
	}
	s.SpoofProfile = profile
	if s.SpoofUplink, err = spoofProfile("uplink profile", f.Uplink); err != nil {
		return err
	}
	if s.SpoofDownlink, err = spoofProfile("downlink profile", f.Downlink); err != nil {
		return err
	}

	// One forged source is a single address; several are a pool the carrier
	// rotates through per session, which is what evades a per-IP block.
	pool, err := parseIPv4List(f.SrcIPs, "spoof source IP")
	if err != nil {
		return err
	}
	s.SpoofSrcIP, s.SpoofSrcPool = "", nil
	if len(pool) == 1 {
		s.SpoofSrcIP = pool[0]
	} else if len(pool) > 1 {
		s.SpoofSrcIP, s.SpoofSrcPool = pool[0], pool
	}

	// The client forges its source, so the server cannot learn where to send
	// replies from the packets themselves — it has to be told. The client
	// already knows the server's real address: it is the one it dialled.
	if s.SpoofPeerIP, err = optionalIPv4(f.PeerIP, "the peer's real IPv4"); err != nil {
		return err
	}
	if s.Role == "server" && s.SpoofPeerIP == "" {
		return fmt.Errorf("the client's real IPv4 address is required on the Iran side — the forged packets do not carry it")
	}
	if s.SpoofPeerSrcIP, err = optionalIPv4(f.PeerSrcIP, "the peer's forged source IPv4"); err != nil {
		return err
	}
	if s.SpoofDstIP, err = optionalIPv4(f.DstIP, "the forged destination IPv4"); err != nil {
		return err
	}

	if s.SpoofInterface, err = checkInterface(f.Interface); err != nil {
		return err
	}

	s.SpoofPipe = f.Pipe
	s.SpoofPipeAddr = ""
	if f.Pipe {
		addr := strings.TrimSpace(f.PipeAddr)
		if addr == "" {
			addr = "127.0.0.1:51820"
		}
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return fmt.Errorf("the WireGuard endpoint must be host:port, e.g. 127.0.0.1:51820")
		}
		s.SpoofPipeAddr = addr
	}

	if f.SockBuf < 0 || f.MTU < 0 || f.PaddingMax < 0 {
		return fmt.Errorf("socket buffer, MTU and padding cannot be negative")
	}
	s.SpoofSockBuf = f.SockBuf
	s.SpoofMTU = f.MTU
	s.SpoofICMPReply = f.ICMPReply

	s.SpoofTTLJitter = f.TTLJitter
	s.SpoofRandomDSCP = f.RandomDSCP
	s.SpoofShufflePort = f.ShufflePort
	s.SpoofPortMin, s.SpoofPortMax = 0, 0
	if f.ShufflePort {
		if f.PortMin != 0 || f.PortMax != 0 {
			if f.PortMin < 1 || f.PortMin > 65535 || f.PortMax < 1 || f.PortMax > 65535 {
				return fmt.Errorf("the source-port range must be between 1 and 65535")
			}
			if f.PortMin > f.PortMax {
				return fmt.Errorf("the source-port range starts above where it ends")
			}
			s.SpoofPortMin, s.SpoofPortMax = f.PortMin, f.PortMax
		}
	}
	s.SpoofPadding = f.Padding
	s.SpoofPaddingMax = 0
	if f.Padding {
		s.SpoofPaddingMax = f.PaddingMax
	}
	// The fake TLS record header is prepended to a TCP-profile packet. On the
	// other two profiles there is no record to fake, so the setting is dropped
	// rather than written out and ignored.
	s.SpoofFakeTLS = f.FakeTLS && (profile == "tcp" || f.Uplink == "tcp" || f.Downlink == "tcp")
	return nil
}

// spoofProfile checks one profile answer. An empty answer is allowed and means
// "not set" — for the direction overrides that is symmetric operation.
func spoofProfile(what, v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "", nil
	}
	for _, p := range spoofProfiles {
		if v == p {
			return v, nil
		}
	}
	return "", fmt.Errorf("the %s must be udp, icmp or tcp", what)
}

// parseIPv4List splits a comma-separated list of IPv4 addresses. The carrier
// writes raw IPv4 headers, so an IPv6 address in here is not a narrower answer
// but an impossible one.
func parseIPv4List(raw, what string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if net.ParseIP(part).To4() == nil {
			return nil, fmt.Errorf("%q is not an IPv4 address — %s takes addresses like 203.0.113.10", part, what)
		}
		out = append(out, part)
	}
	return out, nil
}

// optionalIPv4 accepts an empty answer or one IPv4 address.
func optionalIPv4(raw, what string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if net.ParseIP(raw).To4() == nil {
		return "", fmt.Errorf("%s must be an IPv4 address, e.g. 203.0.113.10", what)
	}
	return raw, nil
}

// checkInterface accepts an empty answer — let the kernel route — or the name
// of a device that exists on this machine.
func checkInterface(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if _, err := net.InterfaceByName(raw); err != nil {
		return "", fmt.Errorf("no network interface named %q on this machine", raw)
	}
	return raw, nil
}

// PckTune is the packet-level TCP carrier's settings: where its frames leave
// from, who the next hop is, and what the flag field of its packets says.
// All three are optional — the transport finds its own egress from the route to
// the peer and defaults to the flags ordinary data carries.
type PckTune struct {
	Interface  string `json:"interface"`
	GatewayMAC string `json:"gatewayMAC"`
	Flags      string `json:"flags"` // comma separated, e.g. "PA,A"
}

func pckOf(s TunnelSpec) PckTune {
	return PckTune{
		Interface:  s.PckInterface,
		GatewayMAC: s.PckGatewayMAC,
		Flags:      strings.Join(s.PckFlags, ","),
	}
}

func (f PckTune) apply(s *TunnelSpec) error {
	if s.Transport != "pck" {
		return nil
	}
	iface, err := checkInterface(f.Interface)
	if err != nil {
		return err
	}
	s.PckInterface = iface

	s.PckGatewayMAC = ""
	if mac := strings.TrimSpace(f.GatewayMAC); mac != "" {
		if _, err := net.ParseMAC(mac); err != nil {
			return fmt.Errorf("%q is not a MAC address — it looks like aa:bb:cc:dd:ee:ff", mac)
		}
		s.PckGatewayMAC = mac
	}

	// Each end decides only its own packets, so an empty cycle is the default
	// rather than something the peer has to be told about.
	s.PckFlags = nil
	if flags := cleanPckFlags(f.Flags); len(flags) > 0 {
		s.PckFlags = flags
	}
	return nil
}

// cleanPckFlags normalises a flag cycle: upper case, no blanks, only the
// letters the carrier understands.
func cleanPckFlags(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.ToUpper(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

// ConnTune is the connectivity block: how this end reaches the other one, and
// what it falls back to when it cannot. The CLI gathers it behind one confirm
// during client setup ("Configure optional connection settings") and under
// Backup server addresses / Load balancing afterwards.
//
// SimpleAuth is the one entry here that exists on both sides: it changes how a
// wss tunnel authorises, and both ends must give the same answer.
type ConnTune struct {
	SimpleAuth    bool   `json:"simpleAuth"`
	EdgeIP        string `json:"edgeIP"`    // client, websocket transports only
	Proxy         string `json:"proxy"`     // client, socks5:// or http://
	Interface     string `json:"interface"` // client, pin the tunnel to a device
	LocalAddr     string `json:"localAddr"` // client, pin it to a source address
	FallbackAddrs string `json:"fallbackAddrs"`
	LoadBalance   bool   `json:"loadBalance"`
}

func connOf(s TunnelSpec) ConnTune {
	return ConnTune{
		SimpleAuth:    s.SimpleAuth,
		EdgeIP:        s.EdgeIP,
		Proxy:         s.Proxy,
		Interface:     s.Interface,
		LocalAddr:     s.LocalAddr,
		FallbackAddrs: strings.Join(s.FallbackAddrs, ", "),
		LoadBalance:   s.LoadBalance,
	}
}

// apply writes the connectivity answers onto a spec. defaultPort is the tunnel
// port a bare backup address inherits, exactly as the wizard fills it in.
func (f ConnTune) apply(s *TunnelSpec, defaultPort string) error {
	// Only a TLS transport has a TLS binding to turn off. Elsewhere the setting
	// would be written out and ignored, which is worse than not offering it.
	s.SimpleAuth = f.SimpleAuth && needsTLS(s.Transport)

	if s.Role != "client" {
		return nil
	}

	s.EdgeIP = ""
	if edge := strings.TrimSpace(f.EdgeIP); edge != "" {
		if !isWS(s.Transport) {
			return fmt.Errorf("an edge IP only applies to the websocket transports")
		}
		if net.ParseIP(edge) == nil {
			return fmt.Errorf("the edge IP must be an IP address, e.g. 104.16.0.1")
		}
		s.EdgeIP = edge
	}

	// A TCP proxy cannot relay a datagram transport's UDP, so the field is
	// refused there rather than accepted and quietly dropped.
	s.Proxy = ""
	if proxy := strings.TrimSpace(f.Proxy); proxy != "" {
		if isDatagram(s.Transport) {
			return fmt.Errorf("a proxy cannot carry %s — it is a datagram transport", s.Transport)
		}
		if _, err := network.ParseProxy(proxy); err != nil {
			return fmt.Errorf("%v", err)
		}
		s.Proxy = proxy
	}

	iface, err := checkInterface(f.Interface)
	if err != nil {
		return err
	}
	s.Interface = iface

	s.LocalAddr = ""
	if local := strings.TrimSpace(f.LocalAddr); local != "" {
		if net.ParseIP(local) == nil {
			return fmt.Errorf("the source address must be an IP address of this machine")
		}
		s.LocalAddr = local
	}

	// A bare address reuses the tunnel port, which is what makes "a second IP
	// of the same server" a one-word answer.
	s.FallbackAddrs = nil
	for _, part := range strings.Split(f.FallbackAddrs, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(part); err != nil {
			if defaultPort == "" {
				return fmt.Errorf("backup address %q has no port and the tunnel port is not known", part)
			}
			part = net.JoinHostPort(strings.Trim(part, "[]"), defaultPort)
		}
		s.FallbackAddrs = append(s.FallbackAddrs, part)
	}

	// Load balancing spreads the connections over every address at once. With
	// no backups there is nothing to spread them over.
	s.LoadBalance = f.LoadBalance && len(s.FallbackAddrs) > 0
	return nil
}

// TunnelLimits caps what this tunnel as a whole may use. Both are 0 by default,
// which is no limit — the same answer the CLI's Limits screen starts on.
type TunnelLimits struct {
	MaxConnections int `json:"maxConnections"`
	BandwidthMbps  int `json:"bandwidthMbps"`
}

func limitsOf(s TunnelSpec) TunnelLimits {
	return TunnelLimits{MaxConnections: s.MaxConnections, BandwidthMbps: s.BandwidthMbps}
}

func (f TunnelLimits) apply(s *TunnelSpec) error {
	if f.MaxConnections < 0 || f.BandwidthMbps < 0 {
		return fmt.Errorf("a limit cannot be negative — use 0 for no limit")
	}
	s.MaxConnections = f.MaxConnections
	s.BandwidthMbps = f.BandwidthMbps
	return nil
}

// applyAdvanced runs the four optional blocks over a spec in the order the
// wizard asks them. A nil block was not submitted and leaves its settings
// exactly as they are.
func applyAdvanced(s *TunnelSpec, spoof *SpoofTune, pck *PckTune, conn *ConnTune, limits *TunnelLimits, defaultPort string) error {
	if spoof != nil {
		if err := spoof.apply(s); err != nil {
			return err
		}
	}
	if pck != nil {
		if err := pck.apply(s); err != nil {
			return err
		}
	}
	if conn != nil {
		if err := conn.apply(s, defaultPort); err != nil {
			return err
		}
	}
	if limits != nil {
		if err := limits.apply(s); err != nil {
			return err
		}
	}
	return nil
}

// clearForTransport drops the settings that belong to a transport the tunnel is
// no longer on. Switching away from spoof and leaving a forged source in the
// config would leave a setting nothing reads and the Edit form would keep
// showing it as if it still mattered.
func clearForTransport(s *TunnelSpec) {
	if s.Transport != "spoof" {
		s.SpoofProfile, s.SpoofUplink, s.SpoofDownlink = "", "", ""
		s.SpoofSrcIP, s.SpoofSrcPool = "", nil
		s.SpoofPeerIP, s.SpoofPeerSrcIP, s.SpoofDstIP = "", "", ""
		s.SpoofInterface = ""
		s.SpoofPipe, s.SpoofPipeAddr = false, ""
		s.SpoofSockBuf, s.SpoofMTU, s.SpoofICMPReply = 0, 0, false
		s.SpoofTTLJitter, s.SpoofRandomDSCP = false, false
		s.SpoofShufflePort, s.SpoofPortMin, s.SpoofPortMax = false, 0, 0
		s.SpoofPadding, s.SpoofPaddingMax, s.SpoofFakeTLS = false, 0, false
	}
	if s.Transport != "pck" {
		s.PckInterface, s.PckGatewayMAC, s.PckFlags = "", "", nil
	}
	if !isWS(s.Transport) {
		s.EdgeIP = ""
	}
	if !needsTLS(s.Transport) {
		s.SimpleAuth = false
	}
	if isDatagram(s.Transport) {
		s.Proxy = ""
	}
}

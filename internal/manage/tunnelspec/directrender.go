package tunnelspec

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tunnel/l3"
)

// Rendering a direct tunnel's config.
//
// This writes its own TOML rather than going through TunnelSpec.Render,
// because it describes a different table — [l3] — and shares no keys with
// [server] or [client]. Keeping the renderers apart is what makes it
// impossible for a change here to alter a reverse tunnel's file. (The other
// direct kind, the [direct] stream tunnel, was removed in v2.0.0 and is no
// longer written by anything.)
//
// The output is commented. A config that explains itself is one an operator
// can fix at three in the morning without the documentation, and these files
// are read far more often than they are written.

// L3Spec is everything the wizard collected for an [l3] tunnel.
type L3Spec struct {
	Name    string
	Side    DirectSide
	Carrier string
	// SNIDomain is the server name the "sni" carrier announces. Ignored by
	// every other carrier, and empty means the engine's default.
	SNIDomain string
	Encap     string
	GREKey    uint32
	Addr      string
	// GRELocal is this server's own address for a kernel GRE link. Empty, the
	// usual case, lets the engine take the source of the route to Addr.
	GRELocal       string
	Token          string
	Iface          string
	LocalIP        string
	PeerIP         string
	MTU            int
	AutoMTU        *bool
	SockBuf        int
	FECData        int
	FECParity      int
	MSSClamp       int
	Preset         string
	TxQueueLen     int
	Qdisc          string
	Ports          []string
	AcceptUDP      bool
	MaxConnections int
	BandwidthMbps  int

	// Carried whole rather than field by field, because these are the keys the
	// wizard never asks about and the editor must not lose.
	//
	// An edit re-renders the file from a spec, so any key the spec cannot hold
	// disappears the moment somebody changes the MTU. That is silent and it is
	// expensive: a spoof carrier tuned over an afternoon reverts to its
	// defaults, and the tunnel that comes back up is not the one that went
	// down. Holding the two structs whole means a key added to either is
	// preserved here without this file being touched.
	Spoof config.SpoofConfig
	Pck   config.PckConfig

	// The pck_* tuning of the PCK engine. Zero is "from the preset", and that
	// is how a new tunnel is written: only the keys somebody chose appear in
	// its file. Read only when Carrier is "pck".
	PckConns     int
	PckKCPMode   string
	PckKCPMTU    int
	PckKCPSndWnd int
	PckKCPRcvWnd int
	PckKCPBlock  string
	// PckRemoteTargets is the kharej side's pck_remote_targets: dial targets
	// that are not this server's own addresses. Written only when set.
	PckRemoteTargets bool
}

func (s L3Spec) DefaultName() string {
	// A GRE link has no port to tell two tunnels apart by; the setup takes
	// the next free name from this one (gre-iran, gre-iran-2, …).
	if l3.KernelCarrier(s.Carrier) {
		return "gre-" + s.Side.String()
	}
	if s.Carrier == l3.CarrierPck {
		return "pck-" + s.Side.String() + "-" + addrPort(s.Addr)
	}
	return "l3-" + s.Side.String() + "-" + addrPort(s.Addr)
}

func (s L3Spec) Render() string {
	if l3.KernelCarrier(s.Carrier) {
		return s.renderGRE()
	}
	if s.Carrier == l3.CarrierPck {
		return s.renderPck()
	}
	var b strings.Builder

	b.WriteString("# Full IP tunnel (layer 3) — a private network between the two servers.\n")
	b.WriteString("# Created by the Backpack setup wizard. Both ends must share the token,\n")
	b.WriteString("# the carrier, and each other's tunnel addresses.\n")
	b.WriteString("#\n")
	b.WriteString("# Needs root: it creates a TUN network interface.\n")
	b.WriteString("\n[l3]\n")

	mode := "dial"
	if s.Side == SideKharej {
		mode = "listen"
	}
	writeKV(&b, "mode", quote(mode))
	writeKV(&b, "addr", quote(s.Addr))
	writeKV(&b, "token", quote(s.Token))
	writeKV(&b, "carrier", quote(s.Carrier))
	// Only where it means something. A domain in a pck tunnel's file is a
	// setting nothing reads, which is the kind an operator later spends time
	// wondering about.
	if s.Carrier == "sni" && s.SNIDomain != "" {
		writeKV(&b, "sni_domain", quote(s.SNIDomain))
	}

	// Written out even when it is the default, because the two ends have to
	// agree on it and a key that is only implied is one an operator cannot
	// check against the other machine's file.
	// One encapsulation, written plainly rather than defaulted: a file that
	// says what it does is a file the next reader does not have to know the
	// defaults of.
	writeKV(&b, "encap", quote("gre"))
	if s.GREKey != 0 {
		writeKV(&b, "gre_key", fmt.Sprint(s.GREKey))
	}

	b.WriteString("\n# The two ends of the private network. The other machine has these\n")
	b.WriteString("# two the other way round.\n")
	writeKV(&b, "iface", quote(s.Iface))
	writeKV(&b, "local_ip", quote(s.LocalIP))
	writeKV(&b, "peer_ip", quote(s.PeerIP))
	writeKV(&b, "mtu", fmt.Sprint(s.MTU))
	// Only when it has been turned off, since on is the default and a file
	// that repeats every default is one nobody reads.
	if s.AutoMTU != nil && !*s.AutoMTU {
		b.WriteString("\n# The tunnel normally measures what the path really carries once it is\n")
		b.WriteString("# up and corrects the mtu above. This turns that off.\n")
		writeKV(&b, "auto_mtu", "false")
	}
	if s.Preset != "" {
		b.WriteString("\n# Performance tuning. The queue is what decides latency under load, and\n")
		b.WriteString("# fq_codel is what keeps a deep queue from becoming latency: it drops\n")
		b.WriteString("# when packets start waiting, so the sender backs off before they do.\n")
		writeKV(&b, "preset", quote(s.Preset))
		writeKV(&b, "txqueuelen", fmt.Sprint(s.TxQueueLen))
		writeKV(&b, "qdisc", quote(s.Qdisc))
	} else if s.TxQueueLen > 0 || s.Qdisc != "" {
		// A queue set by hand with no preset behind it. The file reads fine
		// without the label, and an edit must not turn it back into the default.
		b.WriteString("\n# Performance tuning, set by hand.\n")
		if s.TxQueueLen > 0 {
			writeKV(&b, "txqueuelen", fmt.Sprint(s.TxQueueLen))
		}
		if s.Qdisc != "" {
			writeKV(&b, "qdisc", quote(s.Qdisc))
		}
	}
	if s.SockBuf > 0 {
		writeKV(&b, "sockbuf", fmt.Sprint(s.SockBuf))
	}
	// Error correction is a pair or nothing: half of it configures a scheme the
	// engine refuses, so both are written together or neither is.
	if s.FECData > 0 && s.FECParity > 0 {
		writeKV(&b, "fec_data", fmt.Sprint(s.FECData))
		writeKV(&b, "fec_parity", fmt.Sprint(s.FECParity))
	}
	// Only when it is not the automatic default, so an ordinary file stays
	// short and the key appears exactly when somebody chose it.
	if s.MSSClamp != 0 {
		b.WriteString("\n# Caps the segment size of TCP crossing the tunnel. 0 derives it from\n")
		b.WriteString("# the MTU, which is almost always right; -1 turns it off.\n")
		writeKV(&b, "mss_clamp", fmt.Sprint(s.MSSClamp))
	}

	writeSpoofKeys(&b, s.Spoof)
	writePckKeys(&b, s.Pck)

	if len(s.Ports) > 0 {
		b.WriteString("\n# Ports forwarded over the tunnel. Optional: without them the tunnel\n")
		b.WriteString("# simply carries whatever is routed into the interface.\n")
		writeKV(&b, "ports", tomlList(s.Ports))
		writeKV(&b, "accept_udp", fmt.Sprint(s.AcceptUDP))
		if s.MaxConnections > 0 || s.BandwidthMbps > 0 {
			b.WriteString("\n# Caps on the forwarded ports only. Zero means unlimited.\n")
			writeKV(&b, "max_connections", fmt.Sprint(s.MaxConnections))
			writeKV(&b, "bandwidth_mbps", fmt.Sprint(s.BandwidthMbps))
		}
	}

	return b.String()
}

// renderPck writes a PCK tunnel's file: only what the PCK engine reads.
//
// PCK is not a TUN carrier any more (internal/tunnel/paqet), so the file has no
// interface, no tunnel addresses, no MTU, no queue and no error correction — a
// key that nothing reads is one an operator later wonders about. A file written
// by an older version carries all of those and still runs, as it is; it is only
// when it is edited and re-rendered here that they are left out.
//
// What it keeps from such a file is what the engine does read: sockbuf (the
// capture ring), the three pck_interface, pck_gateway_mac and pck_flags
// overrides, and the limits on the forwarded ports. The pck_* tuning is written
// only where it was set, because the preset already says what it is.
func (s L3Spec) renderPck() string {
	var b strings.Builder

	b.WriteString("# PCK tunnel — forwards ports between the two servers over KCP, carried in\n")
	b.WriteString("# crafted TCP packets that look like an ordinary connection. Created by the\n")
	b.WriteString("# Backpack setup wizard. Both ends must share the token and the carrier.\n")
	b.WriteString("#\n")
	b.WriteString("# There is no network interface and there are no tunnel addresses to set: the\n")
	b.WriteString("# interface, this server's address and the gateway are found at start.\n")
	b.WriteString("# Needs root and libpcap.\n")
	b.WriteString("\n[l3]\n")

	mode := "dial"
	if s.Side == SideKharej {
		mode = "listen"
	}
	if s.Side == SideKharej {
		b.WriteString("# The kharej side waits here, and dials the target of every forwarded port.\n")
		b.WriteString("# Only this server's own services are reached (127.0.0.1 and its own\n")
		b.WriteString("# addresses); add pck_remote_targets = true to let it reach other machines.\n")
	} else {
		b.WriteString("# The Iran side dials the kharej server and listens on the forwarded ports.\n")
	}
	writeKV(&b, "mode", quote(mode))
	writeKV(&b, "addr", quote(s.Addr))
	b.WriteString("# The token is the encryption key. It must be the same on both ends.\n")
	writeKV(&b, "token", quote(s.Token))
	writeKV(&b, "carrier", quote(s.Carrier))
	if s.Preset != "" {
		b.WriteString("\n# Tuning preset: balance, turbo or aggressive. It sets the number of KCP\n")
		b.WriteString("# sessions, the speed mode and the window sizes; the pck_* keys below, when\n")
		b.WriteString("# present, override it one by one.\n")
		writeKV(&b, "preset", quote(s.Preset))
	}
	tune := s.PckConns != 0 || s.PckKCPMode != "" || s.PckKCPMTU != 0 || s.PckKCPSndWnd != 0 ||
		s.PckKCPRcvWnd != 0 || s.PckKCPBlock != ""
	if tune {
		b.WriteString("\n# Fine-tuning, overriding the preset. KCP's windows are the speed limit on a\n")
		b.WriteString("# long path: throughput is at most window x mtu / round-trip time. The cipher\n")
		b.WriteString("# must be the same on both ends.\n")
		if s.PckConns != 0 {
			writeKV(&b, "pck_conns", fmt.Sprint(s.PckConns))
		}
		if s.PckKCPMode != "" {
			writeKV(&b, "pck_kcp_mode", quote(s.PckKCPMode))
		}
		if s.PckKCPMTU != 0 {
			writeKV(&b, "pck_kcp_mtu", fmt.Sprint(s.PckKCPMTU))
		}
		if s.PckKCPSndWnd != 0 {
			writeKV(&b, "pck_kcp_sndwnd", fmt.Sprint(s.PckKCPSndWnd))
		}
		if s.PckKCPRcvWnd != 0 {
			writeKV(&b, "pck_kcp_rcvwnd", fmt.Sprint(s.PckKCPRcvWnd))
		}
		if s.PckKCPBlock != "" {
			writeKV(&b, "pck_kcp_block", quote(s.PckKCPBlock))
		}
	}
	if s.SockBuf > 0 {
		b.WriteString("\n# The capture ring, in bytes. Left out, the preset's is used.\n")
		writeKV(&b, "sockbuf", fmt.Sprint(s.SockBuf))
	}
	writePckKeys(&b, s.Pck)
	if s.PckRemoteTargets && s.Side == SideKharej {
		b.WriteString("\n# Let the tunnel reach machines other than this server.\n")
		writeKV(&b, "pck_remote_targets", "true")
	}

	if len(s.Ports) > 0 && s.Side != SideKharej {
		b.WriteString("\n# Ports forwarded to the kharej server: \"443\" or \"2053=8443\" (listen here,\n")
		b.WriteString("# dial 8443 there), or a range. The kharej side needs no list: each connection\n")
		b.WriteString("# names the target it wants.\n")
		writeKV(&b, "ports", tomlList(s.Ports))
		writeKV(&b, "accept_udp", fmt.Sprint(s.AcceptUDP))
		if s.MaxConnections > 0 || s.BandwidthMbps > 0 {
			b.WriteString("\n# Caps on the forwarded ports only. Zero means unlimited.\n")
			writeKV(&b, "max_connections", fmt.Sprint(s.MaxConnections))
			writeKV(&b, "bandwidth_mbps", fmt.Sprint(s.BandwidthMbps))
		}
	}
	return b.String()
}

// renderGRE writes a kernel GRE tunnel's file.
//
// A separate body rather than conditions through Render: most of what Render
// writes — the token, the encapsulation, error correction, the carrier tables
// — means nothing to a link the kernel encapsulates itself, and a key in the
// file that nothing reads is one an operator later wonders about. The keys it
// does share are written the same way, so the editor and the setup link read
// both kinds alike.
func (s L3Spec) renderGRE() string {
	var b strings.Builder

	b.WriteString("# Kernel GRE (layer 3) — a private network between the two servers.\n")
	b.WriteString("# Created by the Backpack setup wizard.\n")
	b.WriteString("#\n")
	b.WriteString("# NOT ENCRYPTED. The kernel sends this tunnel as bare IP protocol 47, and\n")
	b.WriteString("# whatever crosses it is in the clear unless it is encrypted itself. Both\n")
	b.WriteString("# ends must name each other's real address, the same gre_key and each\n")
	b.WriteString("# other's tunnel addresses.\n")
	b.WriteString("#\n")
	b.WriteString("# Needs root: it creates a GRE network interface.\n")
	b.WriteString("\n[l3]\n")

	mode := "dial"
	if s.Side == SideKharej {
		mode = "listen"
	}
	// mode is geography here, not direction: neither end of a GRE link dials
	// or listens. It is what tells the screens which server this is.
	writeKV(&b, "mode", quote(mode))
	writeKV(&b, "carrier", quote(s.Carrier))
	b.WriteString("# The other server's real address. A GRE link has no port.\n")
	writeKV(&b, "addr", quote(s.Addr))
	if s.GRELocal != "" {
		writeKV(&b, "gre_local", quote(s.GRELocal))
	}
	// Written whatever it is, because the two ends must agree on it and the
	// kernel allows one link per pair of addresses and key.
	writeKV(&b, "gre_key", fmt.Sprint(s.GREKey))

	b.WriteString("\n# The two ends of the private network. The other machine has these\n")
	b.WriteString("# two the other way round.\n")
	writeKV(&b, "iface", quote(s.Iface))
	writeKV(&b, "local_ip", quote(s.LocalIP))
	writeKV(&b, "peer_ip", quote(s.PeerIP))
	if s.MTU > 0 {
		writeKV(&b, "mtu", fmt.Sprint(s.MTU))
	} else {
		b.WriteString("# No mtu: it is worked out at start from the interface the route to the\n")
		b.WriteString("# other server leaves by, less GRE's own 24 bytes (28 with a key; 44 and\n")
		b.WriteString("# 48 over IPv6).\n")
	}
	if s.AutoMTU != nil && !*s.AutoMTU {
		b.WriteString("\n# The tunnel normally measures what the path really carries once the\n")
		b.WriteString("# other end answers, and lowers the mtu to fit. This turns that off.\n")
		writeKV(&b, "auto_mtu", "false")
	}
	if s.Preset != "" {
		// No txqueuelen and no qdisc: the engine leaves a GRE link the
		// kernel's noqueue, so they would be keys nothing reads (see
		// openGRELink in internal/tunnel/l3).
		b.WriteString("\n# The tuning preset. The link itself keeps the kernel's own queueing:\n")
		b.WriteString("# a GRE link never holds packets, the interface it leaves by does.\n")
		writeKV(&b, "preset", quote(s.Preset))
	}
	if s.MSSClamp != 0 {
		b.WriteString("\n# Caps the segment size of TCP crossing the tunnel. 0 derives it from\n")
		b.WriteString("# the MTU, which is almost always right; -1 turns it off.\n")
		writeKV(&b, "mss_clamp", fmt.Sprint(s.MSSClamp))
	}

	if len(s.Ports) > 0 {
		b.WriteString("\n# Ports forwarded over the tunnel. Optional: without them the tunnel\n")
		b.WriteString("# simply carries whatever is routed into the interface.\n")
		writeKV(&b, "ports", tomlList(s.Ports))
		writeKV(&b, "accept_udp", fmt.Sprint(s.AcceptUDP))
		if s.MaxConnections > 0 || s.BandwidthMbps > 0 {
			b.WriteString("\n# Caps on the forwarded ports only. Zero means unlimited.\n")
			writeKV(&b, "max_connections", fmt.Sprint(s.MaxConnections))
			writeKV(&b, "bandwidth_mbps", fmt.Sprint(s.BandwidthMbps))
		}
	}
	return b.String()
}

// writeSpoofKeys emits the forged-source carrier's settings.
//
// Only what is set: a layer-3 tunnel over pck, sni or xdi has none of these,
// and writing out two dozen zeroes would bury the handful of keys that describe
// the tunnel. It mirrors TunnelSpec.writeSpoof, which does the same for a
// reverse tunnel — the two are separate on purpose, so a change to one carrier
// table cannot reshape the other's file.
func writeSpoofKeys(b *strings.Builder, s config.SpoofConfig) {
	if reflect.DeepEqual(s, config.SpoofConfig{}) {
		return
	}
	b.WriteString("\n# The forged-source carrier. The peer forges the source of every packet\n")
	b.WriteString("# it sends, so spoof_peer_ip is how this side knows where replies go.\n")

	str := func(key, value string) {
		if value != "" {
			writeKV(b, key, quote(value))
		}
	}
	num := func(key string, value int) {
		if value > 0 {
			writeKV(b, key, fmt.Sprint(value))
		}
	}
	flag := func(key string, value bool) {
		if value {
			writeKV(b, key, "true")
		}
	}

	str("spoof_profile", s.SpoofProfile)
	str("spoof_uplink", s.SpoofUplink)
	str("spoof_downlink", s.SpoofDownlink)
	str("spoof_src_ip", s.SpoofSrcIP)
	if len(s.SpoofSrcPool) > 0 {
		writeKV(b, "spoof_src_pool", tomlList(s.SpoofSrcPool))
	}
	str("spoof_peer_ip", s.SpoofPeerIP)
	str("spoof_interface", s.SpoofInterface)
	str("spoof_xdp_interface", s.SpoofXDPInterface)
	num("spoof_sockbuf", s.SpoofSockBuf)
	str("spoof_peer_src_ip", s.SpoofPeerSrcIP)
	flag("spoof_icmp_reply", s.SpoofICMPReply)
	num("spoof_mtu", s.SpoofMTU)
	flag("spoof_ttl_jitter", s.SpoofTTLJitter)
	flag("spoof_random_dscp", s.SpoofRandomDSCP)
	flag("spoof_shuffle_port", s.SpoofShufflePort)
	num("spoof_port_min", s.SpoofPortMin)
	num("spoof_port_max", s.SpoofPortMax)
	flag("spoof_padding", s.SpoofPadding)
	num("spoof_padding_max", s.SpoofPaddingMax)
	flag("spoof_fake_tls", s.SpoofFakeTLS)
}

// writePckKeys emits the packet-level TCP carrier's settings, on the same
// terms: only what is set, and never lost by an edit.
func writePckKeys(b *strings.Builder, p config.PckConfig) {
	if p.PckInterface == "" && p.PckGatewayMAC == "" && len(p.PckFlags) == 0 {
		return
	}
	b.WriteString("\n# The packet-level TCP carrier. Every key is optional: the carrier works\n")
	b.WriteString("# out its own egress from the route to the peer.\n")
	if p.PckInterface != "" {
		writeKV(b, "pck_interface", quote(p.PckInterface))
	}
	if p.PckGatewayMAC != "" {
		writeKV(b, "pck_gateway_mac", quote(p.PckGatewayMAC))
	}
	if len(p.PckFlags) > 0 {
		writeKV(b, "pck_flags", tomlList(p.PckFlags))
	}
}

// Labels for the management screens.
//
// A direct tunnel's two ends are not a "server" and a "client" — in a direct
// tunnel the Iran machine is the one that dials, so those words would say the
// opposite of what is true. Geography does not have that problem.

// Two questions the display code actually asks.
//
// Every screen that shows a tunnel used to ask `Role == "server"`, meaning one
// of two different things by it: "does this side expose the forwarded ports?"
// and "does this side wait to be dialled?". In a reverse tunnel those are the
// same side, so one test answered both. In a direct tunnel they are opposite
// sides — Iran exposes the ports *and* dials out — so the single test silently
// answers one of them wrongly, and a direct tunnel's ports disappear from
// every screen that lists them.
//
// Asking the real question by name fixes that everywhere at once, and makes
// the next screen that needs it hard to get wrong.

// HoldsPorts reports whether this side is the one that exposes the forwarded
// ports to users.
func HoldsPorts(t Tunnel) bool {
	return t.Role == "server" || t.Role == "iran"
}

// DialsOut reports whether this side reaches out to the other, rather than
// waiting to be reached.
func DialsOut(t Tunnel) bool {
	return t.Role == "client" || t.Role == "iran"
}

// IsDirectKind reports whether a listed tunnel is one of the two direct kinds.
// The management screens use it to route to the right editor: the reverse
// editor reads [server] and [client], which these files do not have.
func IsDirectKind(t Tunnel) bool {
	return strings.HasPrefix(t.Transport, "direct/") || strings.HasPrefix(t.Transport, "l3/")
}

// How a tunnel describes itself on a card.
//
// A tunnel has two facts worth showing and they used to share one field: the
// panel printed "l3/pck", which is the name this code uses internally and not
// a thing anyone asked for. The two facts are which way it was built and what
// carries it, and they are separate questions with separate answers.

// TunnelDirection reports whether a tunnel is dialled from Iran or waits for
// kharej to dial in — "direct" or "reverse".
func TunnelDirection(t Tunnel) string {
	if IsDirectKind(t) {
		return "direct"
	}
	return "reverse"
}

// TunnelCarrier is what actually carries the tunnel, with the internal prefix
// taken off: "pck" rather than "l3/pck", "wss" rather than "direct/wss". A
// reverse transport has no prefix and is returned unchanged.
func TunnelCarrier(t Tunnel) string {
	if _, rest, found := strings.Cut(t.Transport, "/"); found {
		return rest
	}
	return t.Transport
}

// writeKV writes one aligned key/value line.
func writeKV(b *strings.Builder, key, value string) {
	fmt.Fprintf(b, "%-12s = %s\n", key, value)
}

// quote renders a string as a TOML basic string.
//
// strconv.Quote rather than wrapping in quotes by hand, because a backslash is
// an escape character in a TOML basic string exactly as it is in a Go one. A
// token containing one — which a hand-picked token easily can — written out
// raw produces either a file that does not parse ("invalid escape in string
// '\d'") or, worse, one that parses into a different token than the operator
// typed. A mismatched token is answered with silence by design, so that second
// case presents as a blocked port. This is what the reverse renderer has always
// done, with %q.

func tomlList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = quote(strings.TrimSpace(item))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// L3EncapLabel renders how a layer-3 tunnel wraps its packets, for the
// management screens. The key is shown because both ends must agree on it and
// a mismatch is otherwise invisible.
func L3EncapLabel(l config.L3Config) string {
	// PCK is not wrapped at all: it is KCP and smux streams inside crafted TCP
	// segments, and "GRE + Noise" would name a layer it has none of.
	if l.Carrier == l3.CarrierPck {
		return "KCP over crafted TCP"
	}
	// The kernel's own GRE is exactly the thing the label below exists to be
	// told apart from, and it says so in the same words.
	if l3.KernelCarrier(l.Carrier) {
		label := "Kernel GRE, unencrypted"
		if l.Carrier == l3.CarrierIP6GRE {
			label = "Kernel GRE over IPv6, unencrypted"
		}
		if l.GREKey != 0 {
			label += fmt.Sprintf(" (key %d)", l.GREKey)
		}
		return label
	}
	// "+ Noise" is part of the name on every screen, because "GRE" on its own
	// means the kernel's tunnel to anyone who has set one up — bare IP protocol
	// 47, unencrypted, and blocked by a single firewall rule. This is the same
	// framing carried inside an encrypted session, which is a different thing
	// with the same header. Saying so everywhere costs eight characters and
	// stops the two being confused.
	label := "GRE + Noise"
	if l.GREKey != 0 {
		label += fmt.Sprintf(" (key %d)", l.GREKey)
	}
	return label
}

// LimitsLabel renders the two caps for a summary or a management screen. It
// says "unlimited" rather than "0", because a zero in a list of numbers reads
// like a setting of zero.
func LimitsLabel(maxConns, bandwidthMbps int) string {
	conns := "unlimited"
	if maxConns > 0 {
		conns = fmt.Sprintf("%d connections", maxConns)
	}
	bandwidth := "unlimited"
	if bandwidthMbps > 0 {
		bandwidth = fmt.Sprintf("%d Mbit/s", bandwidthMbps)
	}
	return conns + ", " + bandwidth
}

// DirectSide is which machine the wizard is being run on.
type DirectSide int

const (
	SideIran DirectSide = iota
	SideKharej
)

func (s DirectSide) String() string {
	if s == SideIran {
		return "iran"
	}
	return "kharej"
}

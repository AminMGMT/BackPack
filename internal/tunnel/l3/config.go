package l3

import (
	"fmt"
	"net"
	"strings"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tunnel/mssclamp"
	"github.com/backpack/backpack/internal/tunnel/portmap"
	"github.com/backpack/backpack/internal/utils/network"
)

// Which end dials. A layer-3 tunnel is symmetric once it is up — both ends
// send and receive the same way — so this decides only who reaches out and who
// waits, which is the whole of the direct/reverse question at this layer.
const (
	// ModeDial reaches out to a peer. This is the Iran side of a direct
	// tunnel: it needs no inbound port of its own.
	ModeDial = "dial"

	// ModeListen waits to be dialled.
	ModeListen = "listen"
)

const (
	// defaultIfaceName is the interface created when the config does not name
	// one.
	defaultIfaceName = "bp0"

	// defaultMTU is deliberately conservative. The arithmetic in MTUFor
	// produces something near 1448 on an unobstructed path, but the effective
	// MTU of a real route is frequently below 1500 — a tunnel somewhere
	// upstream, a PPPoE link, an encapsulating provider — and a layer-3 tunnel
	// whose packets are a little too large does not fail loudly. It drops the
	// large flows and passes the small ones, which presents as "downloads
	// stall but ping works" and costs an afternoon to diagnose. Starting low
	// and letting an operator raise it is the cheaper default.
	defaultMTU = 1400

	// defaultTxQueueLen is how many packets the kernel may hold for the
	// interface while this process is draining it. See configureInterface for
	// why the device's own default of 500 is too shallow here, and Config's
	// TxQueueLen for why deeper is not simply better.
	defaultTxQueueLen = 4096

	// defaultQdisc bounds the time a packet may sit in that queue. See
	// Config.Qdisc.
	defaultQdisc = "fq_codel"

	// minMTU is below anything workable; IPv6 alone requires 1280.
	minMTU = 576

	// maxMTU bounds the read buffers.
	maxMTU = 9000
)

// Config is one resolved layer-3 tunnel, already validated.
type Config struct {
	// Mode is ModeDial or ModeListen.
	Mode string

	// Addr is the peer to dial, or the address to bind, depending on Mode.
	Addr string

	// Token is the shared secret both ends hold. It is the only credential.
	Token string

	// Carrier names the datagram carrier beneath the tunnel.
	Carrier string

	// Encap is "ipip" or "gre".
	Encap string

	// GREKey is the RFC 2890 key. Zero omits the field entirely.
	GREKey uint32

	// Iface is the TUN interface name to create.
	Iface string

	// LocalIP is this end's address on the tunnel, with or without a prefix.
	LocalIP string

	// PeerIP is the other end's address on the tunnel.
	PeerIP string

	// MTU is the interface MTU. Zero takes defaultMTU.
	MTU int

	// SockBuf sizes the carrier's socket buffers. Zero takes the carrier
	// default.
	SockBuf int

	// TxQueueLen is how many packets the kernel may hold for the interface
	// while this process drains it. Zero takes the default.
	//
	// Deeper is not better. A queue is latency: 4096 packets of 1400 bytes is
	// 5.7 MB, and on a 100 Mbit/s link a full one is most of half a second of
	// delay before a packet is even sent. That delay is the jitter. Deep queues
	// stop drops under a burst and cause bufferbloat the rest of the time, and
	// the way out of that trade is not a number but Qdisc below.
	TxQueueLen int

	// Qdisc is the queueing discipline put on the interface. Empty takes the
	// default, which is fq_codel.
	//
	// This is the setting that actually decides jitter. Plain fq paces and
	// shares fairly but lets the queue grow to whatever the length allows;
	// fq_codel measures how long packets are sitting there and starts dropping
	// when the delay climbs, which keeps the queue short without giving up
	// throughput. It is the standard answer to bufferbloat and it is why the
	// queue can be deep and the latency still low.
	Qdisc string

	// AutoMTU lets the tunnel measure the path once it is up and set the
	// interface to what it actually carries, instead of trusting the figure in
	// the file. See mtuprobe.go — this is the setting that fails worst when it
	// is wrong, and the only one that cannot be worked out from the config.
	AutoMTU bool

	// MSSClamp caps the TCP segment size of connections crossing the tunnel.
	// Zero derives it from the MTU, which is what almost every tunnel wants;
	// a positive value sets it explicitly; -1 turns clamping off. See
	// mss_linux.go for why the default is on.
	MSSClamp int

	// Ports are forwarded port mappings served over the tunnel, in the same
	// syntax the reverse tunnel uses. Empty means the tunnel carries routed
	// traffic only, which is the plain layer-3 case.
	Ports []string

	// AcceptUDP forwards UDP as well as TCP on the mapped ports. Off unless
	// asked for: a forwarded port carries TCP until an operator says
	// otherwise, for the same reason it does on the reverse tunnel.
	AcceptUDP bool

	// MaxConnections caps simultaneous forwarded connections (0 = unlimited).
	// It applies to the forwarder only: the tunnel itself carries whatever the
	// kernel routes into the interface, which has no connections to count.
	MaxConnections int

	// BandwidthMbps caps forwarded throughput in Mbit/s (0 = unlimited). Same
	// scope as MaxConnections, and for the same reason.
	BandwidthMbps int

	// Spoof tunes the forged-source carrier. Ignored unless Carrier is
	// "spoof". It is the same type the reverse transports take, so the keys an
	// operator already knows mean the same thing here.
	Spoof config.SpoofConfig

	// Pck tunes the packet-level TCP carrier. Ignored unless Carrier is
	// "pck", and every field is optional: the carrier works out its own egress
	// from the route to the peer.
	Pck network.PcapCarrier
}

// Validate fills in what was left out and refuses what cannot work. It is
// called before anything is opened, so a bad config costs nothing.
func (c *Config) Validate() error {
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	switch c.Mode {
	case ModeDial, ModeListen:
	case "":
		return fmt.Errorf("l3: mode is required (%q or %q)", ModeDial, ModeListen)
	default:
		return fmt.Errorf("l3: unknown mode %q (want %q or %q)", c.Mode, ModeDial, ModeListen)
	}

	if strings.TrimSpace(c.Addr) == "" {
		if c.Mode == ModeDial {
			return fmt.Errorf("l3: addr is required and must be the peer's host:port")
		}
		return fmt.Errorf("l3: addr is required and must be the address to bind")
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return fmt.Errorf("l3: addr %q must be host:port: %w", c.Addr, err)
	}

	if strings.TrimSpace(c.Token) == "" {
		return fmt.Errorf("l3: token is required and must match the other end")
	}

	if _, err := NewEncap(c.Encap, c.GREKey); err != nil {
		return err
	}
	if c.Encap == "" {
		c.Encap = "ipip"
	}
	c.Carrier = strings.ToLower(strings.TrimSpace(c.Carrier))
	if c.Carrier == "" {
		c.Carrier = CarrierUDP
	}
	if !knownCarrier(c.Carrier) {
		return fmt.Errorf("l3: carrier %q is not available (have %q, %q, %q, %q)",
			c.Carrier, CarrierUDP, CarrierPck, CarrierXdi, CarrierSpoof)
	}
	// The listening side of the forged-source carrier cannot learn where its
	// peer really is, because every packet it receives carries a forged
	// source. Catching that here beats a tunnel that comes up and sends its
	// replies nowhere.
	if c.Carrier == CarrierSpoof && c.Mode == ModeListen && c.Spoof.SpoofPeerIP == "" {
		return fmt.Errorf("l3: carrier %q needs spoof_peer_ip when listening, "+
			"because the peer forges the source of every packet it sends", CarrierSpoof)
	}
	if c.Iface == "" {
		c.Iface = defaultIfaceName
	}

	if err := validateTunnelAddr("local_ip", c.LocalIP, true); err != nil {
		return err
	}
	if c.PeerIP != "" {
		if err := validateTunnelAddr("peer_ip", c.PeerIP, false); err != nil {
			return err
		}
	}
	if !strings.Contains(c.LocalIP, "/") && c.PeerIP == "" {
		return fmt.Errorf("l3: local_ip %q has no prefix length, so peer_ip is required", c.LocalIP)
	}

	if c.TxQueueLen <= 0 {
		c.TxQueueLen = defaultTxQueueLen
	}
	if c.TxQueueLen > 1<<20 {
		return fmt.Errorf("l3: txqueuelen %d is absurd; a queue that deep is latency, not capacity", c.TxQueueLen)
	}
	if c.Qdisc == "" {
		c.Qdisc = defaultQdisc
	}
	if c.MTU == 0 {
		c.MTU = defaultMTU
	}
	if c.MTU < minMTU || c.MTU > maxMTU {
		return fmt.Errorf("l3: mtu %d is outside the workable range %d..%d", c.MTU, minMTU, maxMTU)
	}
	// A clamp larger than the MTU can carry is not a clamp; it would leave the
	// stall it exists to prevent, while looking like it had been dealt with.
	if c.MSSClamp > 0 && c.MSSClamp > c.MTU-mssclamp.OverheadV4 {
		return fmt.Errorf("l3: mss_clamp %d does not fit an mtu of %d (the largest that fits is %d)",
			c.MSSClamp, c.MTU, c.MTU-mssclamp.OverheadV4)
	}
	if c.MSSClamp < 0 && c.MSSClamp != mssclamp.Off {
		return fmt.Errorf("l3: mss_clamp %d is not a size (use 0 for automatic, or %d to turn it off)",
			c.MSSClamp, mssclamp.Off)
	}

	// Expanded here purely to reject a bad mapping before anything is opened.
	// The forwarder expands them again for real; doing it twice costs nothing
	// and means a typo is a startup error rather than a half-served config.
	if len(c.Ports) > 0 {
		if _, err := portmap.Expand(c.Ports, c.PeerIP); err != nil {
			return fmt.Errorf("l3: %w", err)
		}
	}
	return nil
}

// validateTunnelAddr checks one of the two addresses the interface is given.
// The local one may carry a prefix; the peer one may not, because it names a
// single host.
func validateTunnelAddr(field, value string, allowPrefix bool) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("l3: %s is required (this end's address on the tunnel, e.g. \"10.10.0.1/30\")", field)
	}
	if strings.Contains(value, "/") {
		if !allowPrefix {
			return fmt.Errorf("l3: %s %q must be a bare address, without a prefix length", field, value)
		}
		if _, _, err := net.ParseCIDR(value); err != nil {
			return fmt.Errorf("l3: %s %q is not a valid address/prefix: %w", field, value, err)
		}
		return nil
	}
	if net.ParseIP(value) == nil {
		return fmt.Errorf("l3: %s %q is not a valid IP address", field, value)
	}
	return nil
}

// MTUFor is the largest interface MTU that fits inside a path without
// fragmenting:
//
//	path − carrier − session − encapsulation
//
// It is what the operator should be told when their configured MTU is larger
// than the path can carry, and what a future automatic mode would set. It is
// reported rather than imposed, because the path MTU is a guess and a wrong
// guess that silently lowers the interface is worse than a number an operator
// can see and argue with.
func MTUFor(pathMTU, carrierOverhead, encapOverhead int) int {
	return pathMTU - carrierOverhead - dataOverhead - encapOverhead
}

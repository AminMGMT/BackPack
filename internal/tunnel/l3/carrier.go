package l3

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// Carriers.
//
// A carrier is how a datagram reaches the peer. The tunnel above does not care
// which one it is holding: it hands over a sealed datagram and an address, and
// gets datagrams back. That indirection is what lets the same layer-3 engine
// run over raw TCP segments, ICMP echo and forged-source packets without the
// engine learning anything about them.
//
// Only datagram carriers may appear here. See the package doc for why a
// reliable carrier is not merely a poor fit but actively harmful.

// DatagramCarrier is a net.PacketConn that can also say what it costs.
type DatagramCarrier interface {
	net.PacketConn

	// Overhead is the number of bytes this carrier adds to every datagram on
	// the wire, including the outer IP header. It feeds the MTU calculation,
	// so an underestimate produces a tunnel that silently fragments.
	Overhead() int

	// CarrierName identifies the carrier in logs and diagnostics.
	CarrierName() string
}

// Carrier names accepted in a configuration. A reliable transport — tcp, ws,
// kcp — is deliberately absent and can never be added: see the package doc.
const (
	// CarrierPck is paqet's engine since v2.0.0, not a datagram carrier: see
	// pck.go. The raw-datagram pck it replaced lives on only inside sni.
	CarrierPck   = "pck"
	CarrierXdi   = "xdi"
	CarrierSpoof = "spoof"
	// CarrierSNI is pck with a fake TLS ClientHello at the start of the flow,
	// so a box that classifies by server name lets the rest through. See
	// carrier_sni.go.
	CarrierSNI = "sni"
)

// Carriers that existed and were taken out, kept by name so a config that
// still asks for one is refused in words that say what happened rather than
// as a typo. See RemovedCarrierError.
const (
	// removedCarrierUDP was the plain UDP carrier, and the default: a config
	// that names no carrier at all was a udp tunnel, so an empty name is
	// refused on the same terms rather than quietly moved onto another one.
	removedCarrierUDP = "udp"
	// removedCarrierQuic was a QUIC connection carrying the tunnel in DATAGRAM
	// frames.
	removedCarrierQuic = "quic"
)

// Carriers lists what this engine can open, in the order the screens offer
// them. It is the one list the error messages below are written from. The two
// kernel GRE links are last: they are not datagram carriers, but a config names
// them in the same key — see kernelgre.go.
func Carriers() []string {
	return append(DatagramCarriers(), CarrierGRE, CarrierIP6GRE)
}

// DatagramCarriers is Carriers without the kernel GRE links: the carriers a
// tunnel built on a removed one can be moved onto by changing its carrier
// alone. A GRE link names both servers' real addresses and has no port and no
// token, so a tunnel becomes one by being set up again, not by an edit.
func DatagramCarriers() []string { return []string{CarrierPck, CarrierXdi, CarrierSNI, CarrierSpoof} }

// RemovedCarrier reports whether a carrier name — lower-cased and trimmed, as
// Validate leaves it — is one Backpack v2.0.0 took out. An empty name counts:
// it meant udp.
func RemovedCarrier(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", removedCarrierUDP, removedCarrierQuic:
		return true
	}
	return false
}

// RemovedCarrierError is what a tunnel on a removed carrier is refused with.
//
// No migration: the owner's decision for v2.0.0 was a clear refusal rather
// than a silent switch, because every remaining carrier needs CAP_NET_RAW and
// looks different on the wire, and choosing one for an operator is choosing
// what their path sees. So the message names what is left and where to change
// it, on both ends.
//
// Exported so the management screens that say why a tunnel cannot start use
// the same words the engine refuses it with.
func RemovedCarrierError(name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	was := fmt.Sprintf("carrier %q", name)
	if name == "" {
		// The file says nothing, which is how a udp tunnel was written before
		// the wizard started naming its carrier. Quoting an empty name would
		// send an operator looking for a key that is not there.
		was = "no carrier, which meant udp,"
	}
	return fmt.Errorf("l3: this tunnel names %s and the udp and quic carriers were removed in "+
		"Backpack v2.0.0. Choose one of %s instead, the same on both servers: "+
		"sudo backpack → Manage → Manage Tunnels → the tunnel → Edit → Carrier, or Edit on "+
		"the tunnel in the web panel, or set carrier = \"pck\" in its [l3] table by hand",
		was, strings.Join(DatagramCarriers(), ", "))
}

// testCarriers are carriers only this package's own tests can name.
//
// Every carrier left in production opens a raw socket and needs CAP_NET_RAW,
// and the engine's tests — the handshake, replay, error correction, the MTU
// probe, the pumps — are about the engine, not the carrier, and have to run as
// an ordinary user. A _test.go file registers a loopback carrier here; nothing
// else may, so in every build that is not a test this is empty and a name in
// it can never come from a config.
var testCarriers = map[string]func(context.Context, Config) (DatagramCarrier, net.Addr, error){}

// KnownCarrier reports whether a carrier name is one this engine can open.
//
// Exported so the screens that offer carriers can be checked against the engine
// that has to open them, rather than against a second list written by hand.
func KnownCarrier(name string) bool { return knownCarrier(name) }

// knownCarrier reports whether a name can be opened, so a configuration can be
// refused before anything is created rather than after.
func knownCarrier(name string) bool {
	switch name {
	case CarrierPck, CarrierXdi, CarrierSpoof, CarrierSNI, CarrierGRE, CarrierIP6GRE:
		return true
	}
	_, ok := testCarriers[name]
	return ok
}

// openCarrier builds the carrier named in the config. The returned address is
// the peer to send to, or nil on the listening side of a carrier that learns
// its peer from the packets that arrive.
func openCarrier(cfg Config) (DatagramCarrier, net.Addr, error) {
	return openCarrierContext(context.Background(), cfg)
}

// openCarrierContext lets Run cancel a carrier that is still being opened at
// shutdown.
func openCarrierContext(ctx context.Context, cfg Config) (DatagramCarrier, net.Addr, error) {
	carrier, peer, err := openBareCarrier(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		carrier.Close()
		return nil, nil, err
	}
	// Error correction wraps whichever carrier was opened, so the scheme is the
	// same over spoof, pck, sni and xdi and the MTU calculation picks up its
	// cost through Overhead(). Disabled, this hands the carrier straight back.
	wrapped, err := newFECCarrier(carrier, cfg.FEC)
	if err != nil {
		carrier.Close()
		return nil, nil, err
	}
	return wrapped, peer, nil
}

// openBareCarrier builds the carrier the config names, without the layers that
// wrap it.
func openBareCarrier(ctx context.Context, cfg Config) (DatagramCarrier, net.Addr, error) {
	name := strings.ToLower(strings.TrimSpace(cfg.Carrier))
	switch name {
	case CarrierPck:
		// Run takes the paqet engine before it would get here; see pck.go.
		return nil, nil, fmt.Errorf("l3: carrier %q is the paqet engine, not a datagram carrier", name)
	case CarrierXdi:
		return openXdi(cfg)
	case CarrierSpoof:
		return openSpoof(cfg)
	case CarrierSNI:
		return openSNI(cfg)
	case CarrierGRE, CarrierIP6GRE:
		// Run takes the kernel path before it would get here; a caller that
		// does not is asking a link type for a socket it does not have.
		return nil, nil, fmt.Errorf("l3: carrier %q is a kernel GRE link, not a datagram carrier", name)
	}
	if open, ok := testCarriers[name]; ok {
		return open(ctx, cfg)
	}
	if RemovedCarrier(name) {
		return nil, nil, RemovedCarrierError(name)
	}
	// Refusing by name is better than accepting one and quietly running it
	// over another carrier, which would look like it worked until the path
	// filtered it.
	return nil, nil, fmt.Errorf("l3: carrier %q is not available (have %s)",
		cfg.Carrier, strings.Join(Carriers(), ", "))
}

// carrierResolveTimeout bounds the lookup of the peer's name, so a resolver
// that never answers cannot hold a generation open past its own shutdown.
const carrierResolveTimeout = 12 * time.Second

// resolveDatagramEndpoint resolves a host:port the way net.ResolveUDPAddr
// does, but under the caller's context, so the lookup is cancelled with the
// generation that asked for it rather than outliving it.
func resolveDatagramEndpoint(ctx context.Context, address string) (*net.UDPAddr, error) {
	host, service, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := net.DefaultResolver.LookupPort(ctx, "udp", service)
	if err != nil {
		return nil, err
	}
	if host == "" {
		return &net.UDPAddr{Port: port}, nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, &net.AddrError{Err: "no suitable address found", Addr: address}
	}
	// Match ResolveUDPAddr: IPv4 is preferred except for bracketed addresses.
	chosen := ips[0]
	want6 := strings.Contains(address, "[")
	for _, ip := range ips {
		if (ip.IP.To4() == nil) == want6 {
			chosen = ip
			break
		}
	}
	return &net.UDPAddr{IP: chosen.IP, Port: port, Zone: chosen.Zone}, nil
}

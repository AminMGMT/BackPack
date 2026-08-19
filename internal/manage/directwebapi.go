package manage

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/optimize"
)

// Creating a direct tunnel from the web panel.
//
// A separate type and a separate function from CreateTunnel, for the reason
// every other piece of the direct work is separate: the reverse create path is
// in production and there is no version of "add a field to it" that cannot
// break it. Nothing here is reachable from a reverse form and nothing there is
// reachable from this one.
//
// What the panel collects is what the wizard asks, in the same order: which
// side, how the packets travel, where the peer is, a name, the token, the
// ports, and a tuning preset. The MTU is deliberately absent — the tunnel
// measures the path itself once it is up.

// NewDirectTunnel is a filled direct-tunnel form.
type NewDirectTunnel struct {
	// Side is "iran" or "kharej". Iran dials out and exposes the ports; kharej
	// waits and holds the real service.
	Side string `json:"side"`

	// Carrier is how the packets travel: "pck", "udp" or "spoof".
	Carrier string `json:"carrier"`

	Name  string `json:"name"`
	Token string `json:"token"`

	// PeerAddr is the kharej server's IP or domain, on the Iran side only.
	PeerAddr string `json:"peerAddr"`

	// TunnelPort is the port the carrier uses: what kharej binds, and what Iran
	// reaches out to.
	TunnelPort string `json:"tunnelPort"`

	// Ports are the forwarded port mappings, on the Iran side only.
	Ports string `json:"ports"`

	// AcceptUDP forwards UDP as well as TCP on those ports.
	AcceptUDP bool `json:"acceptUdp"`

	// LocalIP and PeerIP are the two ends of the private network. Empty picks a
	// free /30, which is what the wizard does.
	LocalIP string `json:"localIp"`
	PeerIP  string `json:"peerIp"`

	// Preset is "balance", "turbo" or "aggressive". Empty takes turbo.
	Preset string `json:"preset"`

	// SpoofPeerIP is required on the kharej side of the spoof carrier, which
	// cannot learn where its peer is: every packet it receives carries a forged
	// source.
	SpoofPeerIP string `json:"spoofPeerIp"`

	// GREKey separates tunnels that share the same two servers. Both ends must
	// match. Zero omits it.
	GREKey uint32 `json:"greKey"`

	// MaxConnections and BandwidthMbps cap the forwarded ports (0 = unlimited).
	MaxConnections int `json:"maxConnections"`
	BandwidthMbps  int `json:"bandwidthMbps"`
}

// DirectCarriers is what the panel offers, in the order it offers them. It is
// the same list and the same order as the CLI wizard's, so the two do not drift.
func DirectCarriers() []map[string]string {
	return []map[string]string{
		{"value": "pck", "label": "PCK",
			"desc": "looks like an ordinary TCP flow, but with no socket the firewall can touch"},
		{"value": "udp", "label": "UDP",
			"desc": "plain and simple — use it where the path does not interfere"},
		{"value": "spoof", "label": "Spoof",
			"desc": "raw packets with a forged source address — needs testing on your route"},
	}
}

// DirectPresets is what the panel offers for tuning, in the same order as the
// CLI.
func DirectPresets() []map[string]string {
	return []map[string]string{
		{"value": PresetTurbo, "label": "Turbo",
			"desc": "the default — 8 MB of socket buffer, suits most links"},
		{"value": PresetBalance, "label": "Balance",
			"desc": "smallest footprint, for a small VPS or several tunnels on one box"},
		{"value": PresetAggressive, "label": "Aggressive",
			"desc": "for a fast link with bursts — 32 MB of buffer and a deep queue"},
	}
}

// SuggestDirectDefaults is what the panel fills a blank form with: a free
// interface, a free subnet and a name nothing else has taken.
func SuggestDirectDefaults(side string) map[string]any {
	s := sideIran
	if strings.EqualFold(strings.TrimSpace(side), "kharej") {
		s = sideKharej
	}
	local, peer := freeL3Subnet(s)
	return map[string]any{
		"localIp": local,
		"peerIp":  peer,
		"iface":   freeL3Iface(),
		"token":   randomToken(64),
		"preset":  PresetTurbo,
	}
}

// CreateDirectTunnel writes a direct tunnel from a filled form and starts it.
//
// A tunnel that is created but does not come up is reported as such rather than
// as a failure, exactly as the reverse path reports it: the config is on disk
// either way, and the usual cause — a port already taken — is fixed by editing
// the tunnel, not by creating it again.
func CreateDirectTunnel(n NewDirectTunnel) (service string, active bool, err error) {
	spec, err := n.spec()
	if err != nil {
		return "", false, err
	}

	body := spec.render()
	// Parsed before it is written. A config that does not decode would leave a
	// tunnel that cannot start and an operator with no idea why, and the cost
	// of checking is one parse of a file we just built.
	var check config.Config
	if _, err := toml.Decode(body, &check); err != nil {
		return "", false, fmt.Errorf("the form produced a config that does not parse: %w", err)
	}

	optimize.ApplyQuiet()

	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(app.ConfigPath(spec.Name), []byte(body), 0644); err != nil {
		return "", false, err
	}
	if err := writeUnit(spec.Name); err != nil {
		return "", false, err
	}
	if err := DaemonReload(); err != nil {
		return "", false, err
	}
	service = app.ServiceName(spec.Name)
	if err := StartService(service); err != nil {
		return service, false, err
	}
	return service, IsActive(service), nil
}

// spec validates a form and turns it into what the renderer takes. Everything
// the wizard refuses, this refuses, with the same wording — the two paths write
// the same file and must reject the same input.
func (n NewDirectTunnel) spec() (l3Spec, error) {
	var side directSide
	switch strings.ToLower(strings.TrimSpace(n.Side)) {
	case "iran":
		side = sideIran
	case "kharej":
		side = sideKharej
	default:
		return l3Spec{}, fmt.Errorf("side must be iran or kharej")
	}

	carrier := strings.ToLower(strings.TrimSpace(n.Carrier))
	switch carrier {
	case "pck", "udp", "spoof":
	case "":
		carrier = "pck"
	default:
		return l3Spec{}, fmt.Errorf("carrier %q is not one the panel offers (pck, udp, spoof)", n.Carrier)
	}

	port := strings.TrimSpace(n.TunnelPort)
	if !validPort(port) {
		return l3Spec{}, fmt.Errorf("tunnel port %q is not a port between 1 and 65535", n.TunnelPort)
	}

	name := strings.TrimSpace(n.Name)
	if name == "" {
		name = "l3-" + side.String() + "-" + port
	}
	if !validName(name) {
		return l3Spec{}, fmt.Errorf("tunnel name %q may use only letters, digits, dots and dashes", name)
	}
	name = uniqueName(name)

	token := strings.TrimSpace(n.Token)
	if token == "" {
		return l3Spec{}, fmt.Errorf("a token is required, and must match the other machine exactly")
	}

	// Iran reaches out; kharej waits. This is the whole of the direct/reverse
	// difference at this layer.
	addr := net.JoinHostPort("0.0.0.0", port)
	if side == sideIran {
		host := strings.TrimSpace(n.PeerAddr)
		if host == "" {
			return l3Spec{}, fmt.Errorf("the kharej server's address is required on the Iran side")
		}
		addr = net.JoinHostPort(host, port)
	}

	local, peer := strings.TrimSpace(n.LocalIP), strings.TrimSpace(n.PeerIP)
	if local == "" || peer == "" {
		local, peer = freeL3Subnet(side)
	}

	spec := l3Spec{
		Name: name, Side: side, Carrier: carrier,
		// Always Backpack's own GRE inside the Noise session. There is no
		// choice here and the panel does not offer one; see askL3Encap's
		// removal in the CLI wizard for why.
		Encap: "gre", GREKey: n.GREKey,
		Addr: addr, Token: token,
		Iface: freeL3Iface(), LocalIP: local, PeerIP: peer,
		MTU:            defaultL3MTU,
		MaxConnections: n.MaxConnections,
		BandwidthMbps:  n.BandwidthMbps,
	}
	findL3Preset(strings.ToLower(strings.TrimSpace(n.Preset))).apply(&spec)

	if side == sideIran {
		spec.Ports = parsePorts(n.Ports)
		if len(spec.Ports) == 0 {
			return l3Spec{}, fmt.Errorf("at least one forwarded port is required on the Iran side")
		}
		if err := validatePortSpecs(spec.Ports); err != nil {
			return l3Spec{}, err
		}
		spec.AcceptUDP = n.AcceptUDP
	}

	// The forged-source carrier cannot learn where its peer really is, because
	// every packet it receives carries a forged source. Catching it here beats
	// a tunnel that comes up and sends its replies nowhere.
	if carrier == "spoof" && side == sideKharej {
		ip := strings.TrimSpace(n.SpoofPeerIP)
		if net.ParseIP(ip) == nil {
			return l3Spec{}, fmt.Errorf(
				"the spoof carrier needs the Iran server's real IP on this side, " +
					"because the peer forges the source of every packet it sends")
		}
		spec.Spoof = config.SpoofConfig{SpoofPeerIP: ip}
	}
	return spec, nil
}

// SuggestDirectPort offers a free port for the carrier, so the panel can fill
// the field the way the wizard's Random button does.
func SuggestDirectPort() string {
	return strconv.Itoa(SuggestPort())
}

// Editing a direct tunnel from the panel.
//
// The reverse settings endpoints go through LoadSpec, which reads [server] and
// [client] and refuses anything else by design — so a direct tunnel sent there
// came back as "not a client tunnel" and the Edit button did nothing. These are
// the direct half, offering what the CLI editor offers and nothing that has to
// match the other machine.

// DirectSettings is what the panel shows for a direct tunnel, and what it may
// change. The address, the carrier and the token are shown but not editable:
// all three have to match the other end, so changing one here alone would only
// break the tunnel.
type DirectSettings struct {
	Name      string `json:"name"`
	Side      string `json:"side"`
	Carrier   string `json:"carrier"`
	Encap     string `json:"encap"`
	Addr      string `json:"addr"`
	Token     string `json:"token"`
	Iface     string `json:"iface"`
	LocalIP   string `json:"localIp"`
	PeerIP    string `json:"peerIp"`
	MTU       int    `json:"mtu"`
	AutoMTU   bool   `json:"autoMtu"`
	Preset    string `json:"preset"`
	Ports     string `json:"ports"`
	AcceptUDP bool   `json:"acceptUdp"`

	MaxConnections int `json:"maxConnections"`
	BandwidthMbps  int `json:"bandwidthMbps"`

	// HoldsPorts says whether this side has a port list at all. The kharej side
	// does not: every target arrives on the stream that asks for it.
	HoldsPorts bool `json:"holdsPorts"`
}

// DirectEdit is what the panel may change.
type DirectEdit struct {
	Ports          *string `json:"ports"`
	AcceptUDP      *bool   `json:"acceptUdp"`
	Preset         *string `json:"preset"`
	MTU            *int    `json:"mtu"`
	AutoMTU        *bool   `json:"autoMtu"`
	MaxConnections *int    `json:"maxConnections"`
	BandwidthMbps  *int    `json:"bandwidthMbps"`
}

// DirectSettingsOf reads one direct tunnel's editable settings.
func DirectSettingsOf(name string) (DirectSettings, error) {
	cfg, err := LoadTunnelConfig(name)
	if err != nil {
		return DirectSettings{}, err
	}
	if !cfg.L3.Enabled() {
		return DirectSettings{}, fmt.Errorf("%q is not a direct tunnel", name)
	}
	return directSettingsFrom(name, cfg.L3), nil
}

// directSettingsFrom is the mapping, with no filesystem in it.
//
// Separated because ConfigDir is a constant and a test cannot point it
// somewhere safe — so the only way to check what this decides is to hand it a
// config directly. Which is the better shape anyway: the decisions are here and
// the I/O is above.
func directSettingsFrom(name string, l config.L3Config) DirectSettings {
	return DirectSettings{
		Name:    name,
		Side:    l3Role(l.Mode),
		Carrier: orDefault(l.Carrier, "udp"),
		Encap:   l3EncapLabel(l),
		Addr:    l.Addr,
		Token:   l.Token,
		Iface:   orDefault(l.Iface, "bp0"),
		LocalIP: l.LocalIP,
		PeerIP:  l.PeerIP,
		MTU:     l.MTU,
		AutoMTU: l.AutoMTUEnabled(),
		Preset:  orDefault(l.Preset, PresetTurbo),
		Ports:   strings.Join(l.Ports, ", "),

		AcceptUDP:      l.AcceptUDP,
		MaxConnections: l.MaxConnections,
		BandwidthMbps:  l.BandwidthMbps,

		// The kharej side has no port list at all: every target arrives on the
		// stream that asks for it, so what is forwarded is set on Iran.
		HoldsPorts: !strings.EqualFold(strings.TrimSpace(l.Mode), "listen"),
	}
}

// EditDirectSettings applies the panel's edit and restarts the tunnel.
//
// Every change lands in one write and one restart, as the reverse editor does.
// A field the form did not send is left exactly as it was, which is what the
// pointers are for: a zero that means "set this to zero" and a zero that means
// "the form did not ask" are otherwise the same value.
func EditDirectSettings(name string, e DirectEdit) error {
	cfg, err := LoadTunnelConfig(name)
	if err != nil {
		return err
	}
	if !cfg.L3.Enabled() {
		return fmt.Errorf("%q is not a direct tunnel", name)
	}

	l, err := applyDirectEdit(cfg.L3, e)
	if err != nil {
		return err
	}
	spec := directSpecFrom(name, l)
	if e.Preset != nil {
		findL3Preset(strings.ToLower(strings.TrimSpace(*e.Preset))).apply(&spec)
	}

	body := spec.render()
	var check config.Config
	if _, err := toml.Decode(body, &check); err != nil {
		return fmt.Errorf("the edit produced a config that does not parse: %w", err)
	}
	if err := os.WriteFile(app.ConfigPath(name), []byte(body), 0644); err != nil {
		return err
	}
	return RestartService(app.ServiceName(name))
}

// applyDirectEdit folds the form into a config.
//
// A field the form did not send is left exactly as it was, which is what the
// pointers are for: a zero meaning "set this to zero" and a zero meaning "the
// form did not ask" are otherwise the same value, and the second one silently
// wipes settings.
func applyDirectEdit(l config.L3Config, e DirectEdit) (config.L3Config, error) {
	if e.Ports != nil {
		ports := parsePorts(*e.Ports)
		if err := validatePortSpecs(ports); err != nil {
			return l, err
		}
		l.Ports = ports
	}
	if e.AcceptUDP != nil {
		l.AcceptUDP = *e.AcceptUDP
	}
	if e.MTU != nil {
		if *e.MTU != 0 && (*e.MTU < 576 || *e.MTU > 9000) {
			return l, fmt.Errorf("mtu %d is outside the workable range 576..9000", *e.MTU)
		}
		l.MTU = *e.MTU
	}
	if e.AutoMTU != nil {
		v := *e.AutoMTU
		l.AutoMTU = &v
	}
	if e.MaxConnections != nil {
		l.MaxConnections = *e.MaxConnections
	}
	if e.BandwidthMbps != nil {
		l.BandwidthMbps = *e.BandwidthMbps
	}
	return l, nil
}

// directSpecFrom turns a config back into what the renderer takes, carrying
// every key the form does not touch — including the carrier tables, so an edit
// cannot quietly drop a spoof or pck profile.
func directSpecFrom(name string, l config.L3Config) l3Spec {
	side := sideIran
	if strings.EqualFold(strings.TrimSpace(l.Mode), "listen") {
		side = sideKharej
	}
	return l3Spec{
		Name:    name,
		Side:    side,
		Carrier: orDefault(l.Carrier, "udp"),
		Encap:   orDefault(l.Encap, "gre"),
		GREKey:  l.GREKey,
		Addr:    l.Addr,
		Token:   l.Token,
		Iface:   orDefault(l.Iface, "bp0"),
		LocalIP: l.LocalIP,
		PeerIP:  l.PeerIP,
		MTU:     l.MTU,
		AutoMTU: l.AutoMTU,
		SockBuf: l.SockBuf, MSSClamp: l.MSSClamp,
		Preset: l.Preset, TxQueueLen: l.TxQueueLen, Qdisc: l.Qdisc,
		Ports:          l.Ports,
		AcceptUDP:      l.AcceptUDP,
		MaxConnections: l.MaxConnections,
		BandwidthMbps:  l.BandwidthMbps,
		Spoof:          l.SpoofConfig,
		Pck:            l.PckConfig,
	}
}

package manage

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/optimize"
	"github.com/backpack/backpack/internal/tunnel/l3"
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

	// Spoof is the forged-source carrier's drawer, nil unless it was opened.
	// It is the same shape the CLI's IP Spoofing screen fills, and it is only
	// read when the carrier is spoof — a drawer sent for a udp tunnel would put
	// keys in the config that nothing reads.
	Spoof *SpoofTune `json:"spoof"`

	// Stealth turns on the obfuscation group in one answer, exactly as the
	// wizard's Stealth question does: padding, header cosmetics, and the fake
	// TLS record header where the profile carries one. It is applied after the
	// drawer, so a form can send both and the group wins — which is the answer
	// the operator gave last.
	Stealth bool `json:"stealth"`

	// Paths spreads the udp carrier over several sockets. One or zero is the
	// ordinary single socket. Refused on the other carriers, which vary their
	// source per packet already.
	Paths int `json:"paths"`

	// FEC turns on error correction with the recommended scheme, the same one
	// answer the wizard asks. The exact pair is a CLI-only tuning.
	FEC bool `json:"fec"`

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
		{"value": "quic", "label": "QUIC",
			"desc": "a real QUIC session on UDP — indistinguishable from HTTP/3, and needs no root"},
		{"value": "spoof", "label": "Spoof",
			"desc": "raw packets with a forged source address — needs testing on your route"},
		{"value": "xdi", "label": "ICMP",
			"desc": "inside ping, for a path that filters UDP and TCP but lets ping through"},
	}
}

// carrierNames is DirectCarriers reduced to the values, in the same order.
func carrierNames() []string {
	out := make([]string, 0, len(DirectCarriers()))
	for _, c := range DirectCarriers() {
		out = append(out, c["value"])
	}
	return out
}

// offeredCarrier reports whether a carrier is one the screens offer.
func offeredCarrier(name string) bool {
	for _, c := range DirectCarriers() {
		if c["value"] == name {
			return true
		}
	}
	return false
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
	name, body, err := directBody(n)
	if err != nil {
		return "", false, err
	}
	if err := writeDirectConfig(name, body); err != nil {
		return "", false, err
	}
	service = app.ServiceName(name)
	if err := StartService(service); err != nil {
		return service, false, err
	}
	return service, IsActive(service), nil
}

// ApplyDirectTunnel writes the direct tunnel this form describes, whether or
// not it is already there. It is the direct half of ApplyTunnel; see there for
// why a managed node has one verb rather than create and edit.
func ApplyDirectTunnel(n NewDirectTunnel) (service string, active bool, created bool, err error) {
	name, body, err := directBody(n)
	if err != nil {
		return "", false, false, err
	}
	path := app.ConfigPath(name)
	service = app.ServiceName(name)

	if !fileExists(path) {
		if err := writeDirectConfig(name, body); err != nil {
			return service, false, true, err
		}
		if err := StartService(service); err != nil {
			return service, false, true, err
		}
		return service, IsActive(service), true, nil
	}

	// Replacing a running tunnel, so the same protection an edit gets on this
	// machine applies: keep the old file, and put it back if the new one does
	// not come up. On a node this is the difference between a bad push being an
	// error message and a bad push being a server that has to be fixed by hand.
	prev, err := os.ReadFile(path)
	if err != nil {
		return service, false, false, fmt.Errorf("could not read the current config: %w", err)
	}
	wasActive := IsActive(service)

	if err := writeDirectConfig(name, body); err != nil {
		_ = os.WriteFile(path, prev, 0644)
		return service, IsActive(service), false, err
	}
	if err := RestartService(service); err != nil {
		revertSpec(path, prev, service, wasActive)
		return service, IsActive(service), false,
			fmt.Errorf("the tunnel failed to restart with the new settings — reverted: %w", err)
	}
	if !WaitServiceActive(service, 10*time.Second) {
		detail := lastLogLine(service)
		revertSpec(path, prev, service, wasActive)
		if detail != "" {
			return service, IsActive(service), false,
				fmt.Errorf("the tunnel did not come up with the new settings — reverted. Reason: %s", detail)
		}
		return service, IsActive(service), false,
			fmt.Errorf("the tunnel did not come up with the new settings — reverted to the previous config")
	}
	recordConfigChange(name, prev, "")
	return service, IsActive(service), false, nil
}

// directBody turns a filled form into the name and the config text it
// describes, writing nothing.
func directBody(n NewDirectTunnel) (name, body string, err error) {
	spec, err := n.spec()
	if err != nil {
		return "", "", err
	}
	body = spec.render()
	// Parsed before it is written. A config that does not decode would leave a
	// tunnel that cannot start and an operator with no idea why, and the cost
	// of checking is one parse of a file we just built.
	var check config.Config
	if _, err := toml.Decode(body, &check); err != nil {
		return "", "", fmt.Errorf("the form produced a config that does not parse: %w", err)
	}
	return spec.Name, body, nil
}

// writeDirectConfig puts the config and its unit on disk and reloads systemd.
// It does not start anything.
func writeDirectConfig(name, body string) error {
	optimize.ApplyQuiet()
	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(app.ConfigPath(name), []byte(body), 0644); err != nil {
		return err
	}
	if err := writeUnit(name); err != nil {
		return err
	}
	return DaemonReload()
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

	// Checked against the one list the screens are built from, not a copy of
	// it. There were three copies — this one, the panel's and the wizard's —
	// and adding a carrier to two of them produced a screen that offered
	// something the form behind it refused by name.
	carrier := strings.ToLower(strings.TrimSpace(n.Carrier))
	if carrier == "" {
		carrier = "pck"
	}
	if !offeredCarrier(carrier) {
		return l3Spec{}, fmt.Errorf("carrier %q is not one the panel offers (%s)",
			n.Carrier, strings.Join(carrierNames(), ", "))
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
	if n.Paths > 1 {
		spec.Paths = n.Paths
	}
	if n.FEC {
		plan := defaultL3FEC()
		spec.FECData, spec.FECParity = plan.Data, plan.Parity
	}
	if carrier == "spoof" {
		if n.Spoof != nil {
			if err := n.Spoof.apply(&spec.Spoof); err != nil {
				return l3Spec{}, err
			}
		}
		// The dedicated field wins over the drawer's copy of it, because it is
		// the one the form asks for on its own and the one an operator filling
		// only the basics will have typed into.
		if ip := strings.TrimSpace(n.SpoofPeerIP); ip != "" {
			if net.ParseIP(ip) == nil {
				return l3Spec{}, fmt.Errorf("%q is not an IP address", ip)
			}
			spec.Spoof.SpoofPeerIP = ip
		}
		if n.Stealth {
			applySpoofStealth(&spec.Spoof)
		}
		if side == sideKharej && net.ParseIP(spec.Spoof.SpoofPeerIP) == nil {
			return l3Spec{}, fmt.Errorf(
				"the spoof carrier needs the Iran server's real IP on this side, " +
					"because the peer forges the source of every packet it sends")
		}
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

	// Spoof opens the carrier's drawer on what this tunnel actually runs, and
	// Stealth is the one-answer group over it. Both are only meaningful when
	// the carrier is spoof; on any other carrier they are zero and the panel
	// does not draw the drawer.
	Spoof   SpoofTune `json:"spoof"`
	Stealth bool      `json:"stealth"`

	// Paths and FEC as the panel shows them: how many sockets, and whether
	// error correction is on at all. The exact scheme stays a CLI tuning.
	Paths int  `json:"paths"`
	FEC   bool `json:"fec"`
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

	// Spoof replaces the carrier's settings wholesale, and Stealth turns the
	// obfuscation group on or off. Both are nil unless the form sent them, on
	// the same terms as everything else here.
	Spoof   *SpoofTune `json:"spoof"`
	Stealth *bool      `json:"stealth"`

	// Paths changes how many sockets the udp carrier spreads over, and FEC
	// turns error correction on or off with the recommended scheme. Both are
	// nil unless the form sent them, on the same terms as everything else here.
	Paths *int  `json:"paths"`
	FEC   *bool `json:"fec"`
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

		Spoof:   spoofOf(l.SpoofConfig),
		Stealth: spoofStealthOn(l.SpoofConfig),
		Paths:   l.Paths,
		FEC:     l.FECData > 0 && l.FECParity > 0,
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
	// The carrier's drawer, then the group over it — in that order, so a form
	// that sends both gets the group's answer rather than whichever field the
	// drawer happened to carry. It is the answer the operator gave last, and
	// the one the screen was showing them.
	//
	// Both are refused on a carrier that has no forged source to configure,
	// rather than written and ignored: a config holding spoof keys for a udp
	// tunnel reads as a tunnel doing something it is not.
	if e.Spoof != nil || e.Stealth != nil {
		if !strings.EqualFold(strings.TrimSpace(l.Carrier), "spoof") {
			return l, fmt.Errorf("this tunnel's carrier is %s, which has no forged source to configure",
				orDefault(l.Carrier, "udp"))
		}
	}
	if e.Spoof != nil {
		if err := e.Spoof.apply(&l.SpoofConfig); err != nil {
			return l, err
		}
	}
	if e.Stealth != nil {
		if *e.Stealth {
			applySpoofStealth(&l.SpoofConfig)
		} else {
			clearSpoofStealth(&l.SpoofConfig)
		}
	}
	// Spreading over sockets belongs to the plain UDP carrier; the engine
	// refuses it elsewhere, so the edit is refused here rather than written and
	// then rejected at the next start.
	if e.Paths != nil {
		if *e.Paths > 1 && !strings.EqualFold(strings.TrimSpace(orDefault(l.Carrier, "udp")), "udp") {
			return l, fmt.Errorf("spreading over sockets is for the udp carrier; %s varies its source per packet already",
				orDefault(l.Carrier, "udp"))
		}
		if err := (MultipathFor(*e.Paths)).Validate(); err != nil {
			return l, err
		}
		l.Paths = *e.Paths
	}
	if e.FEC != nil {
		if *e.FEC {
			plan := defaultL3FEC()
			l.FECData, l.FECParity = plan.Data, plan.Parity
		} else {
			l.FECData, l.FECParity = 0, 0
		}
	}
	return l, nil
}

// MultipathFor is the engine's own validation of a socket count, so the panel
// refuses exactly what the tunnel would refuse rather than keeping a second
// copy of the rule.
func MultipathFor(paths int) l3.MultipathConfig { return l3.MultipathConfig{Paths: paths} }

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
		FECData: l.FECData, FECParity: l.FECParity,
		Paths:  l.Paths,
		Preset: l.Preset, TxQueueLen: l.TxQueueLen, Qdisc: l.Qdisc,
		Ports:          l.Ports,
		AcceptUDP:      l.AcceptUDP,
		MaxConnections: l.MaxConnections,
		BandwidthMbps:  l.BandwidthMbps,
		Spoof:          l.SpoofConfig,
		Pck:            l.PckConfig,
	}
}

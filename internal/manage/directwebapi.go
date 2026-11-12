package manage

import (
	"fmt"
	"maps"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage/tunnelspec"
	"github.com/backpack/backpack/internal/optimize"
	"github.com/backpack/backpack/internal/tunnel/l3"
	"github.com/backpack/backpack/internal/tunnel/paqet"
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

	// Carrier is how the packets travel: "xdi" or "pck" from the panel, and
	// "sni" or "spoof" from a setup link or the CLI. Empty takes pck.
	Carrier string `json:"carrier"`

	Name  string `json:"name"`
	Token string `json:"token"`

	// PeerAddr is the kharej server's IP or domain, on the Iran side only —
	// except on GRE, where each side names the other's: a GRE link has both
	// servers' real addresses in it.
	PeerAddr string `json:"peerAddr"`

	// GRELocal is this server's own address for a GRE link. Empty, the usual
	// case, takes the source of the route to the other server.
	GRELocal string `json:"greLocal"`

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
	// read when the carrier is spoof — a drawer sent for a pck tunnel would put
	// keys in the config that nothing reads.
	Spoof *SpoofTune `json:"spoof"`

	// Stealth turns on the obfuscation group in one answer, exactly as the
	// wizard's Stealth question does: padding, header cosmetics, and the fake
	// TLS record header where the profile carries one. It is applied after the
	// drawer, so a form can send both and the group wins — which is the answer
	// the operator gave last.
	Stealth bool `json:"stealth"`

	// FEC turns on error correction with the recommended scheme, the same one
	// answer the wizard asks. The exact pair is a CLI-only tuning — or comes
	// from the other side's setup link, in FECData and FECParity, which then
	// win: the two ends must use the same pair.
	FEC       bool `json:"fec"`
	FECData   int  `json:"fecData"`
	FECParity int  `json:"fecParity"`

	// MTU, MSSClamp and AutoMTU are set from the other side's setup link.
	// Zero and nil keep the defaults.
	MTU      int   `json:"mtu"`
	MSSClamp int   `json:"mssClamp"`
	AutoMTU  *bool `json:"autoMtu"`

	// SpoofPeerIP is required on the kharej side of the spoof carrier, which
	// cannot learn where its peer is: every packet it receives carries a forged
	// source.
	SpoofPeerIP string `json:"spoofPeerIp"`

	// SNIDomain is the server name the sni carrier announces at the start of
	// the flow. Empty takes the built-in default. Both ends may name different
	// domains — each one is only telling the box in front of it what to think —
	// but the same one on both is one less thing to remember.
	SNIDomain string `json:"sniDomain"`

	// GREKey separates tunnels that share the same two servers. Both ends must
	// match. Zero omits it — except on GRE, where zero on the Iran side picks a
	// free one, and the kharej side must be given Iran's.
	GREKey uint32 `json:"greKey"`

	// MaxConnections and BandwidthMbps cap the forwarded ports (0 = unlimited).
	MaxConnections int `json:"maxConnections"`
	BandwidthMbps  int `json:"bandwidthMbps"`

	// The PCK engine's fine-tuning, read only when the carrier is pck. Zero or
	// empty takes the preset's value; see config.L3Config.
	PckConns     int    `json:"pckConns"`
	PckKCPMode   string `json:"pckKcpMode"`
	PckKCPMTU    int    `json:"pckKcpMtu"`
	PckKCPSndWnd int    `json:"pckKcpSndWnd"`
	PckKCPRcvWnd int    `json:"pckKcpRcvWnd"`
	PckKCPBlock  string `json:"pckKcpBlock"`
}

// DirectCarriers is what the panel offers, in the order it offers them. It is
// the same list and the same order as the CLI wizard's, so the two do not drift.
func DirectCarriers() []map[string]string {
	// needsRoot is the server's answer, not the screen's guess: whether a
	// carrier can open its socket without CAP_NET_RAW is a property of how it
	// is built, and a screen that decided that for itself would be a second
	// place to keep in step.
	//
	// The order is the operator's: xDi, PCK, then the two spoofing carriers,
	// whose answers depend on the route. (UDP and Quic sat between them until
	// v2.0.0 removed both; see l3.RemovedCarrierError.)
	return []map[string]string{
		{"value": "xdi", "label": "xDi", "needsRoot": "1",
			"desc": "inside ping (ICMP), for a path that filters UDP and TCP but lets ping through"},
		{"value": "pck", "label": "PCK", "needsRoot": "1",
			"desc": "forwards your ports over KCP in packets that look like an ordinary TCP flow, with no socket the firewall can touch"},
		// cliOnly keeps a carrier out of the panel without taking it away.
		//
		// These two are the ones whose answers depend on the route rather than
		// on the tunnel: which forged source a path will carry, which domain it
		// already lets through. Getting them wrong produces a tunnel that comes
		// up and moves nothing, and finding out costs a capture on the machine
		// itself. That is CLI work, so the panel does not offer them — and the
		// engine still runs a config that names one, whichever screen wrote it.
		{"value": "spoof", "label": "IP Spoofing", "needsRoot": "1", "cliOnly": "1",
			"desc": "raw packets with a forged source address — needs testing on your route"},
		{"value": "sni", "label": "SNI Spoofing", "needsRoot": "1", "cliOnly": "1",
			"desc": "PCK, plus a TLS hello naming a domain your route allows — the box in front reads that name and lets the rest through"},
		// The kernel's own GRE. One entry for both of its links: gre or ip6gre
		// follows from the other server's address. It is the one carrier here
		// with no encryption at all, and its description says so first.
		{"value": "gre", "label": "GRE", "needsRoot": "1", "kernel": "1",
			"desc": "kernel GRE — NOT encrypted: bare IP protocol 47, fast where the route passes it; run your own encryption inside"},
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

// offeredCarrier reports whether a carrier is one the screens offer. ip6gre is
// offered under GRE's entry.
func offeredCarrier(name string) bool {
	if name == l3.CarrierIP6GRE {
		return true
	}
	for _, c := range DirectCarriers() {
		if c["value"] == name {
			return true
		}
	}
	return false
}

// PanelDirectCarriers is DirectCarriers without the ones the panel does not
// offer. The CLI reads the whole list; this is what /api/direct/options serves.
func PanelDirectCarriers() []map[string]string {
	out := make([]map[string]string, 0, len(DirectCarriers()))
	for _, c := range DirectCarriers() {
		if c["cliOnly"] == "" {
			out = append(out, c)
		}
	}
	return out
}

// pckSuits says in a line what each preset is for, on PCK. Aggressive is not
// the fast one: on a clean path its shorter tick and four sessions cost more
// than they win, and it is measured to be slower than Turbo there. What it is
// for is a route that is long and loses packets, where four sessions lose less
// to each lost packet than two do.
var pckSuits = map[string]string{
	PresetBalance:    "a small VPS, a quiet tunnel",
	PresetTurbo:      "most links",
	PresetAggressive: "long, lossy routes; slower than Turbo on a clean path",
}

// DirectPresets is what the panel offers for tuning, in the same order as the
// CLI. Each carries two descriptions: desc for the carriers that run over a TUN
// device, and pck for PCK, which is the engine's own parameters in one line
// ("2 sessions, fast2, 1024-packet windows …"); the pck* entries are the
// numbers behind it, which the Fine-Tune section shows as the values a blank
// field takes.
func DirectPresets() []map[string]string {
	pck := func(name string) map[string]string {
		p := pckParamsOf(name)
		return map[string]string{
			"pck": p.Summary(), "pckConns": strconv.Itoa(p.Conns), "pckMode": p.Mode,
			"pckSndWnd": strconv.Itoa(p.SndWnd), "pckRcvWnd": strconv.Itoa(p.RcvWnd), "pckMtu": strconv.Itoa(p.MTU),
			"pckBuf": fmt.Sprintf("%d MiB", p.SmuxBuf>>20), "pckCapture": fmt.Sprintf("%d MiB", p.PcapBuf>>20),
			"pckSuits": pckSuits[name],
		}
	}
	out := []map[string]string{
		{"value": PresetTurbo, "label": "Turbo",
			"desc": "the default — 8 MB of socket buffer, suits most links"},
		{"value": PresetBalance, "label": "Balance",
			"desc": "smallest footprint, for a small VPS or several tunnels on one box"},
		{"value": PresetAggressive, "label": "Aggressive",
			"desc": "for a fast link with bursts — 32 MB of buffer and a deep queue"},
	}
	for _, o := range out {
		maps.Copy(o, pck(o["value"]))
	}
	return out
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
	// A second kharej given the first one's ports: those ports are shared
	// with it rather than bound twice, which could only fail. The panel has no
	// screen to ask on, and there is no other reading of the request — see
	// l3share.go.
	if spec.Side == sideIran && len(spec.Ports) > 0 {
		// Not for PCK: its ports are dialled on its one kharej, and there is no
		// peer address on a TUN to add a second kharej to (see eachIranL3Tunnel).
		// A port another tunnel already holds is reported as in use, below.
		if spec.Carrier != l3.CarrierPck {
			if spec.Ports, err = shareL3PortsQuietly(spec); err != nil {
				return "", false, err
			}
		}
		if busy := busyForwardPorts(spec.Ports, spec.PeerIP); len(busy) > 0 {
			return "", false, fmt.Errorf("already in use on this server: %s — "+
				"the web panel's own port is the usual one; pick other ports", strings.Join(busy, ", "))
		}
	}
	if err := pckRunnable(spec.Carrier); err != nil {
		return "", false, err
	}
	name, body, err := directBodyFromSpec(spec)
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
	if err := pckRunnable(n.Carrier); err != nil {
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
		_ = app.WriteFileAtomic(path, prev, app.TunnelConfigMode)
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
	return directBodyFromSpec(spec)
}

// directBodyFromSpec is directBody for a spec already built.
func directBodyFromSpec(spec l3Spec) (name, body string, err error) {
	body = spec.Render()
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
	optimize.ApplyQuiet(ReservedPorts())
	if err := os.MkdirAll(app.ConfigDir, 0755); err != nil {
		return err
	}
	if err := app.WriteFileAtomic(app.ConfigPath(name), []byte(body), app.TunnelConfigMode); err != nil {
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
	// A kernel GRE link has no port and no token, and names the other server
	// on both sides; see directgre.go.
	if l3.KernelCarrier(carrier) {
		return n.greSpec(side, carrier)
	}
	// PCK is a port-forwarding transport with no TUN and none of its keys; see
	// directpck.go.
	if carrier == l3.CarrierPck {
		return n.pckSpec(side)
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
	if err := l3.CheckTunnelEnds(local, peer); err != nil {
		return l3Spec{}, err
	}
	// Given addresses — from a setup link, or typed into the form — were
	// chosen on the other machine or by hand, not against this one's tunnels.
	// Two tunnels here on one block put two interfaces on one subnet, and the
	// second one never carries anything.
	if strings.TrimSpace(n.LocalIP) != "" {
		if other := l3BlockOwner(local); other != "" {
			return l3Spec{}, fmt.Errorf("tunnel %s on this server already uses %sx, the block given for "+
				"this one: pick a free block on the %s server and set up both ends again",
				other, l3Block(local), map[directSide]string{sideIran: "kharej", sideKharej: "Iran"}[side])
		}
	}

	spec := l3Spec{
		Name: name, Side: side, Carrier: carrier,
		// Always Backpack's own GRE inside the Noise session. There is no
		// choice here and the panel does not offer one; see askL3Encap's
		// removal in the CLI wizard for why.
		Encap: "gre", GREKey: n.GREKey,
		SNIDomain: strings.ToLower(strings.TrimSpace(n.SNIDomain)),
		Addr:      addr, Token: token,
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

	if n.FECData > 0 && n.FECParity > 0 {
		spec.FECData, spec.FECParity = n.FECData, n.FECParity
	} else if n.FEC {
		plan := defaultL3FEC()
		spec.FECData, spec.FECParity = plan.Data, plan.Parity
	}
	if n.MTU > 0 {
		spec.MTU = n.MTU
	}
	spec.MSSClamp, spec.AutoMTU = n.MSSClamp, n.AutoMTU
	// The forged-source carrier cannot learn where its peer really is, because
	// every packet it receives carries a forged source. Catching it here beats
	// a tunnel that comes up and sends its replies nowhere.
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
// the direct half, offering what the CLI editor offers.

// DirectSettings is what the panel shows for a direct tunnel, and what it may
// change. The address and the token are shown but not editable: both have to
// match the other end, so changing one here alone would only break the
// tunnel. The carrier has to match too, and is editable anyway, with the form
// saying so — since v2.0.0 removed udp and quic it is the one change a tunnel
// built on either needs before it can start again.
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

	// Carriers is what the carrier menu offers: the panel's own list, plus
	// this tunnel's carrier when it is one the panel does not offer itself
	// (sni and spoof are set up from the CLI), so the menu can show what the
	// tunnel runs rather than a blank.
	Carriers []map[string]string `json:"carriers"`
	// Unrunnable is why the tunnel cannot start as it stands, in the engine's
	// words, or empty. Set for a carrier v2.0.0 removed; the form shows it
	// above the carrier menu, which is the fix.
	Unrunnable string `json:"unrunnable,omitempty"`

	// HoldsPorts says whether this side has a port list at all. The kharej side
	// does not: every target arrives on the stream that asks for it.
	HoldsPorts bool `json:"holdsPorts"`

	// Kernel marks a kernel GRE tunnel: no token, no error correction, and a
	// carrier menu of GRE alone. GREKey is what the other end has to match.
	Kernel bool   `json:"kernel"`
	GREKey uint32 `json:"greKey"`

	// Spoof opens the carrier's drawer on what this tunnel actually runs, and
	// Stealth is the one-answer group over it. Both are only meaningful when
	// the carrier is spoof; on any other carrier they are zero and the panel
	// does not draw the drawer.
	Spoof   SpoofTune `json:"spoof"`
	Stealth bool      `json:"stealth"`

	// FEC as the panel shows it: whether error correction is on at all. The
	// exact scheme stays a CLI tuning.
	FEC bool `json:"fec"`

	// Pck marks a PCK tunnel, which has no TUN device: the form shows no
	// interface, no addresses, no MTU and no error correction for it. The six
	// values are what the file sets explicitly (zero or empty: from the preset),
	// PckEffective is what the engine will actually run with, and PckNotes says
	// what in a file from an older version is no longer read.
	Pck          bool     `json:"pck"`
	PckConns     int      `json:"pckConns"`
	PckKCPMode   string   `json:"pckKcpMode"`
	PckKCPMTU    int      `json:"pckKcpMtu"`
	PckKCPSndWnd int      `json:"pckKcpSndWnd"`
	PckKCPRcvWnd int      `json:"pckKcpRcvWnd"`
	PckKCPBlock  string   `json:"pckKcpBlock"`
	PckEffective string   `json:"pckEffective,omitempty"`
	PckNotes     []string `json:"pckNotes,omitempty"`
}

// DirectEdit is what the panel may change.
type DirectEdit struct {
	// Carrier moves the tunnel onto another carrier. Both ends must match, so
	// the form says to change the other server too.
	Carrier        *string `json:"carrier"`
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

	// FEC turns error correction on or off with the recommended scheme. Nil
	// unless the form sent it, on the same terms as everything else here.
	FEC *bool `json:"fec"`

	// The PCK engine's fine-tuning. Zero or empty sets a value back to the
	// preset's; nil leaves it. Refused on a carrier that is not pck.
	PckConns     *int    `json:"pckConns"`
	PckKCPMode   *string `json:"pckKcpMode"`
	PckKCPMTU    *int    `json:"pckKcpMtu"`
	PckKCPSndWnd *int    `json:"pckKcpSndWnd"`
	PckKCPRcvWnd *int    `json:"pckKcpRcvWnd"`
	PckKCPBlock  *string `json:"pckKcpBlock"`
}

// DirectSettingsOf reads one direct tunnel's editable settings.
func DirectSettingsOf(name string) (DirectSettings, error) {
	cfg, err := LoadTunnelConfig(name)
	if err != nil {
		return DirectSettings{}, err
	}
	if !cfg.L3.Enabled() {
		// A [direct] stream tunnel has nothing left to edit: the engine is
		// gone, and the panel says so in the engine's words.
		if cfg.Direct.Present() {
			return DirectSettings{}, config.ErrDirectRemoved
		}
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
	carrier := orDefault(strings.ToLower(strings.TrimSpace(l.Carrier)), "udp")
	// The menu offers what an edit can move this tunnel onto: another datagram
	// carrier, or — for a GRE tunnel — GRE alone. The two kinds are built from
	// different facts, so one is never edited into the other.
	kernel := l3.KernelCarrier(carrier)
	family := carrierFamily(carrier)
	var carriers []map[string]string
	for _, c := range PanelDirectCarriers() {
		// A removed carrier has no family of its own: it can be moved onto
		// either of the other two kinds of tunnel, apart from GRE.
		if family == "" {
			if !l3.KernelCarrier(c["value"]) {
				carriers = append(carriers, c)
			}
			continue
		}
		if carrierFamily(c["value"]) == family {
			carriers = append(carriers, c)
		}
	}
	if !offeredOnPanel(carrier) {
		label := carrierLabel(carrier)
		if l3.RemovedCarrier(carrier) {
			label += " (removed)"
		}
		carriers = append(carriers, map[string]string{"value": carrier, "label": label})
	}
	unrunnable := ""
	if l3.RemovedCarrier(l.Carrier) {
		unrunnable = strings.TrimPrefix(l3.RemovedCarrierError(l.Carrier).Error(), "l3: ")
	}
	pck := carrier == l3.CarrierPck
	ds := DirectSettings{
		Name:       name,
		Carriers:   carriers,
		Unrunnable: unrunnable,
		Side:       l3Role(l.Mode),
		Carrier:    orDefault(l.Carrier, "udp"),
		Encap:      l3EncapLabel(l),
		Addr:       l.Addr,
		Token:      l.Token,
		Iface:      orDefault(l.Iface, "bp0"),
		LocalIP:    l.LocalIP,
		PeerIP:     l.PeerIP,
		MTU:        l.MTU,
		AutoMTU:    l.AutoMTUEnabled(),
		Preset:     orDefault(l.Preset, PresetTurbo),
		Ports:      strings.Join(l.Ports, ", "),

		AcceptUDP:      l.AcceptUDP,
		MaxConnections: l.MaxConnections,
		BandwidthMbps:  l.BandwidthMbps,

		// The kharej side has no port list at all: every target arrives on the
		// stream that asks for it, so what is forwarded is set on Iran.
		HoldsPorts: !strings.EqualFold(strings.TrimSpace(l.Mode), "listen"),

		Spoof:   spoofOf(l.SpoofConfig),
		Stealth: spoofStealthOn(l.SpoofConfig),
		FEC:     l.FECData > 0 && l.FECParity > 0,

		Kernel: kernel,
		GREKey: l.GREKey,
	}
	if pck {
		// None of the TUN's settings is real for it, whatever the file still says.
		ds.Iface, ds.LocalIP, ds.PeerIP, ds.MTU, ds.AutoMTU, ds.FEC = "", "", "", 0, false, false
		ds.Pck = true
		ds.PckConns, ds.PckKCPMode, ds.PckKCPMTU = l.PckConns, l.PckKCPMode, l.PckKCPMTU
		ds.PckKCPSndWnd, ds.PckKCPRcvWnd, ds.PckKCPBlock = l.PckKCPSndWnd, l.PckKCPRcvWnd, l.PckKCPBlock
		if c, err := l3.ConfigFromTable(l); err == nil {
			if pc, err := l3.PckEngineConfig(c); err == nil {
				ds.PckEffective = pc.Params.Summary()
			}
			ds.PckNotes = l3.PckNotes(c)
		}
		// A file the previous PCK wrote still holds its TUN's settings. They are
		// read for nothing and dropped the next time the tunnel is saved, which
		// is worth one line, since the file looks like it is doing something
		// with them.
		if l.Iface != "" || l.LocalIP != "" || l.PeerIP != "" || l.MTU != 0 || l.TxQueueLen != 0 || l.Qdisc != "" {
			ds.PckNotes = append(ds.PckNotes, "This file still has the previous PCK's TUN settings "+
				"(interface, tunnel addresses, MTU); PCK has no TUN device now, so they are ignored, "+
				"and saving this tunnel drops them")
		}
	}
	return ds
}

// carrierFamily groups the carriers an edit may move a tunnel between. A
// tunnel is changed onto another carrier of its own family and never out of
// it, because the families are built from different facts:
//
//	"tun"    xdi, sni, spoof — a TUN device and a /30 both servers agree on
//	"pck"    pck — ports over KCP, a token and a port, no addresses at all
//	"kernel" gre, ip6gre — the two servers' real addresses and a key
//
// "" is a carrier that no longer runs (udp, quic), which has no facts to
// protect.
func carrierFamily(carrier string) string {
	switch {
	case l3.KernelCarrier(carrier):
		return "kernel"
	case carrier == l3.CarrierPck:
		return "pck"
	case l3.KnownCarrier(carrier):
		return "tun"
	}
	return ""
}

// offeredOnPanel reports whether the panel's own carrier menu lists a carrier.
func offeredOnPanel(name string) bool {
	for _, c := range PanelDirectCarriers() {
		if c["value"] == name {
			return true
		}
	}
	return false
}

// EditDirectSettings applies the panel's edit and restarts the tunnel.
//
// Every change lands in one write and one restart, as the reverse editor does.
// A field the form did not send is left exactly as it was, which is what the
// pointers are for: a zero that means "set this to zero" and a zero that means
// "the form did not ask" are otherwise the same value.
func EditDirectSettings(name string, e DirectEdit) error {
	// Read through the loader first: it is what refuses a name that is not a
	// tunnel's, before the path is built from it.
	if _, err := LoadTunnelConfig(name); err != nil {
		return err
	}
	raw, err := os.ReadFile(app.ConfigPath(name))
	if err != nil {
		return err
	}
	body, err := editedDirectBody(name, string(raw), e)
	if err != nil {
		return err
	}
	return tunnelspec.ApplyBody(name, body, "panel edit")
}

// editedDirectBody is the file the panel's edit writes, with no filesystem in
// it: raw is the tunnel's file as it is now.
//
// It is rendered, not re-encoded. The CLI's Edit writes through directSpecFrom
// and the renderer (saveL3), so a tunnel edited from either place ends up as the
// same file: the header, the carrier's own comments, the key order, and only the
// keys that carrier reads. Encoding a decoded document instead threw all of that
// away and wrote every zero-valued field of the struct — a token on a GRE link,
// a qdisc and a txqueuelen it never reads.
//
// What the renderer cannot hold is carried over from raw by keptForeignKeys, so
// the one thing re-rendering costs — a key somebody added by hand that this
// engine has no field for — is not paid.
func editedDirectBody(name, raw string, e DirectEdit) (string, error) {
	var cfg config.Config
	md, err := toml.Decode(raw, &cfg)
	if err != nil {
		return "", err
	}
	l, err := editedL3(name, cfg, e)
	if err != nil {
		return "", err
	}
	body := directSpecFrom(name, l).Render()
	body, err = withForeignKeys(body, raw, md)
	if err != nil {
		return "", err
	}
	// What is about to replace the file has to be a file: checked here, with the
	// reason, rather than at the restart.
	if err := validateRendered(body); err != nil {
		return "", err
	}
	return body, nil
}

// withForeignKeys puts back into a freshly rendered body every key of the old
// file that the config has no field for: the ones a hand edit or a newer
// version of the file added, which the renderer cannot write because it does
// not know them.
//
// Known keys are not carried over, deliberately. The renderer decides which of
// those a carrier reads, and a key it leaves out was either turned off by this
// very edit (ports cleared, error correction off) or has no meaning for the
// carrier — resurrecting it would undo the edit or bring back the junk.
func withForeignKeys(body, raw string, md toml.MetaData) (string, error) {
	undecoded := md.Undecoded()
	if len(undecoded) == 0 {
		return body, nil
	}
	var doc map[string]any
	if _, err := toml.Decode(raw, &doc); err != nil {
		return "", err
	}
	l3Table, _ := doc["l3"].(map[string]any)

	l3Keys := map[string]any{}
	topKeys := map[string]any{}
	for _, key := range undecoded {
		switch {
		case len(key) >= 2 && key[0] == "l3":
			l3Keys[key[1]] = l3Table[key[1]]
		case len(key) >= 1 && key[0] != "l3":
			topKeys[key[0]] = doc[key[0]]
		}
	}

	// Plain values go inside [l3], which is the last table of the body; tables
	// go after it. A top-level value has to come before [l3] or it would read as
	// one of its keys.
	var inL3, tables, top strings.Builder
	for _, k := range slices.Sorted(maps.Keys(l3Keys)) {
		v := l3Keys[k]
		if _, isTable := v.(map[string]any); isTable {
			if err := encodeInto(&tables, map[string]any{"l3": map[string]any{k: v}}); err != nil {
				return "", err
			}
			tables.WriteString("\n")
			continue
		}
		if err := encodeInto(&inL3, map[string]any{k: v}); err != nil {
			return "", err
		}
	}
	for _, k := range slices.Sorted(maps.Keys(topKeys)) {
		v := topKeys[k]
		out := &top
		if _, isTable := v.(map[string]any); isTable {
			out = &tables
		}
		if err := encodeInto(out, map[string]any{k: v}); err != nil {
			return "", err
		}
		if out == &tables {
			tables.WriteString("\n")
		}
	}

	if top.Len() > 0 {
		at := strings.Index(body, "\n[l3]\n")
		if at < 0 {
			return "", fmt.Errorf("the rendered config has no [l3] table")
		}
		body = body[:at] + "\n# Kept from the previous file; this version has no setting by these names.\n" +
			top.String() + body[at:]
	}
	if inL3.Len() > 0 {
		body += "\n# Kept from the previous file; this version has no setting by these names.\n" + inL3.String()
	}
	if tables.Len() > 0 {
		body += "\n" + strings.TrimPrefix(tables.String(), "[l3]\n")
	}
	return body, nil
}

// encodeInto writes one table-shaped value as TOML.
func encodeInto(b *strings.Builder, v map[string]any) error {
	return toml.NewEncoder(b).Encode(v)
}

// editedL3 is the panel's edit applied to a tunnel's config and checked by the
// engine, with no filesystem in it — the whole decision EditDirectSettings
// makes, separate for the same reason directSettingsFrom is.
//
// It reads a tunnel on a removed carrier like any other: refusing to load one
// would leave no way to fix it. What it refuses is saving one that still could
// not start, which is the engine's own validation at the end — so a udp tunnel
// whose MTU is changed is told why it cannot start, and one moved onto pck is
// saved.
func editedL3(name string, cfg config.Config, e DirectEdit) (config.L3Config, error) {
	if !cfg.L3.Enabled() {
		if cfg.Direct.Present() {
			return config.L3Config{}, config.ErrDirectRemoved
		}
		return config.L3Config{}, fmt.Errorf("%q is not a direct tunnel", name)
	}
	l, err := applyDirectEdit(cfg.L3, e)
	if err != nil {
		return l, err
	}
	// A PCK tunnel's preset is applied with its fine-tuning, in applyPckEdit: it
	// maps to engine parameters, not to a queue and a socket buffer.
	if e.Preset != nil && *e.Preset != cfg.L3.Preset && !strings.EqualFold(strings.TrimSpace(l.Carrier), l3.CarrierPck) {
		spec := directSpecFrom(name, l)
		findL3Preset(strings.ToLower(strings.TrimSpace(*e.Preset))).apply(&spec)
		// Decoded over the config as it was, so a key the spec does not carry
		// keeps its value instead of falling to zero.
		again := cfg
		if _, err := toml.Decode(spec.Render(), &again); err != nil {
			return l, err
		}
		l = again.L3
	}
	engine, err := l3.ConfigFromTable(l)
	if err != nil {
		return l, err
	}
	if err := engine.Validate(); err != nil {
		return l, err
	}
	return l, nil
}

// applyDirectEdit folds the form into a config.
//
// A field the form did not send is left exactly as it was, which is what the
// pointers are for: a zero meaning "set this to zero" and a zero meaning "the
// form did not ask" are otherwise the same value, and the second one silently
// wipes settings.
func applyDirectEdit(l config.L3Config, e DirectEdit) (config.L3Config, error) {
	// First, so every check below — the spoof drawer's, the engine's — sees
	// the carrier the tunnel is moving to rather than the one it is leaving.
	// Any carrier the engine opens is accepted, not only the panel's two: a
	// sni or spoof tunnel keeps its carrier through an edit that sends it back
	// unchanged.
	if e.Carrier != nil {
		carrier := strings.ToLower(strings.TrimSpace(*e.Carrier))
		if !l3.KnownCarrier(carrier) {
			if l3.RemovedCarrier(carrier) {
				return l, l3.RemovedCarrierError(carrier)
			}
			return l, fmt.Errorf("carrier %q is not one this engine has (%s)",
				*e.Carrier, strings.Join(l3.Carriers(), ", "))
		}
		// GRE is a link naming both servers' real addresses, with no port and
		// no token; a datagram tunnel's file has none of that to move it onto.
		if l3.KernelCarrier(carrier) != l3.KernelCarrier(l.Carrier) {
			return l, fmt.Errorf("a GRE tunnel is set up anew rather than changed into or out of: " +
				"add the new tunnel on both servers, then delete this one")
		}
		// PCK is refused the same way, and for the same reason: the other
		// carriers run a TUN device whose two addresses both servers must
		// agree on, and an edit has no way to make the other server agree.
		// PCK has no such addresses, and no TUN to carry routed traffic: it
		// forwards ports only. Moving across would write a file the target
		// carrier cannot start from, or silently drop what the tunnel carries.
		if from, to := carrierFamily(l.Carrier), carrierFamily(carrier); from != to && from != "" {
			return l, fmt.Errorf("a tunnel on the %s carrier cannot be changed onto the %s carrier: %s. "+
				"Add the new tunnel on both servers, then delete this one",
				carrierLabel(l.Carrier), carrierLabel(carrier), familyChangeWhy(from, to))
		}
		// "gre" is the screens' one entry for both kernel links, so it keeps
		// whichever the tunnel already runs.
		if carrier == l3.CarrierGRE && l3.KernelCarrier(l.Carrier) {
			carrier = l.Carrier
		}
		l.Carrier = carrier
	}
	pck := strings.EqualFold(strings.TrimSpace(l.Carrier), l3.CarrierPck)
	if pck {
		if err := applyPckEdit(&l, e); err != nil {
			return l, err
		}
	} else if e.PckConns != nil || e.PckKCPMode != nil || e.PckKCPMTU != nil || e.PckKCPSndWnd != nil ||
		e.PckKCPRcvWnd != nil || e.PckKCPBlock != nil {
		return l, fmt.Errorf("this tunnel's carrier is %s, which has no pck_* settings: "+
			"they belong to the PCK engine", orDefault(l.Carrier, "udp"))
	}
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
	if pck && ((e.MTU != nil && *e.MTU != 0) || e.AutoMTU != nil) {
		return l, fmt.Errorf("a PCK tunnel has no TUN device, so no MTU to set: " +
			"the size of its packets is pck_kcp_mtu")
	}
	if e.MTU != nil && !pck {
		if *e.MTU != 0 && (*e.MTU < 576 || *e.MTU > 9000) {
			return l, fmt.Errorf("mtu %d is outside the workable range 576..9000", *e.MTU)
		}
		l.MTU = *e.MTU
	}
	if e.AutoMTU != nil && !pck {
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
	// rather than written and ignored: a config holding spoof keys for a pck
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
	if e.FEC != nil {
		if *e.FEC && l3.KernelCarrier(l.Carrier) {
			return l, fmt.Errorf("a GRE tunnel has no error correction: the kernel sends it, " +
				"and FEC works only on Backpack's own carriers")
		}
		if *e.FEC && pck {
			return l, fmt.Errorf("a PCK tunnel has no error correction to turn on: KCP retransmits " +
				"what is lost, and FEC works only on the carriers that run a TUN device")
		}
		if *e.FEC {
			if l.FECData > 0 && l.FECParity > 0 {
				return l, nil
			}
			plan := defaultL3FEC()
			l.FECData, l.FECParity = plan.Data, plan.Parity
		} else {
			l.FECData, l.FECParity = 0, 0
		}
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
		Name:      name,
		Side:      side,
		Carrier:   orDefault(l.Carrier, "udp"),
		Encap:     orDefault(l.Encap, "gre"),
		SNIDomain: l.SNIDomain,
		GREKey:    l.GREKey,
		Addr:      l.Addr,
		GRELocal:  l.GRELocal,
		Token:     l.Token,
		Iface:     orDefault(l.Iface, "bp0"),
		LocalIP:   l.LocalIP,
		PeerIP:    l.PeerIP,
		MTU:       l.MTU,
		AutoMTU:   l.AutoMTU,
		SockBuf:   l.SockBuf, MSSClamp: l.MSSClamp,
		FECData: l.FECData, FECParity: l.FECParity,
		Preset: l.Preset, TxQueueLen: l.TxQueueLen, Qdisc: l.Qdisc,
		Ports:          l.Ports,
		AcceptUDP:      l.AcceptUDP,
		MaxConnections: l.MaxConnections,
		BandwidthMbps:  l.BandwidthMbps,
		Spoof:          l.SpoofConfig,
		Pck:            l.PckConfig,

		PckConns: l.PckConns, PckKCPMode: l.PckKCPMode, PckKCPMTU: l.PckKCPMTU,
		PckKCPSndWnd: l.PckKCPSndWnd, PckKCPRcvWnd: l.PckKCPRcvWnd, PckKCPBlock: l.PckKCPBlock,
		PckRemoteTargets: l.PckRemoteTargets,
	}
}

// familyChangeWhy says, for a refused carrier change, what the two kinds of
// tunnel do not share.
func familyChangeWhy(from, to string) string {
	switch {
	case from == "pck":
		return "PCK forwards ports over KCP and has no TUN device, while the other carriers run one, with " +
			"a pair of tunnel addresses both servers must agree on, and an edit cannot make the other server agree"
	case to == "pck":
		return "PCK forwards ports only, with no TUN device and none of the tunnel addresses, MTU or error " +
			"correction this tunnel has, so routed traffic would stop crossing it"
	}
	return "they are built from different settings"
}

// applyPckEdit is the part of an edit that is the PCK engine's: its preset and
// the pck_* fine-tuning.
//
// A changed preset sets the preset-derived values back to the preset's —
// sessions, mode, windows and the capture ring — before the fine-tuning of the
// same request is applied, so that moving from Turbo to Aggressive really moves
// the tunnel to Aggressive's parameters, while a value the form sends along with
// it (zero or empty meaning "the preset's") still wins. What a preset does not
// define, the packet size and the cipher, stays as it was.
func applyPckEdit(l *config.L3Config, e DirectEdit) error {
	if e.Preset != nil {
		preset := strings.ToLower(strings.TrimSpace(*e.Preset))
		if preset == "" {
			preset = paqet.PresetTurbo
		}
		if _, ok := paqet.PresetParams(preset); !ok {
			return fmt.Errorf("preset %q is not one of %s", *e.Preset, strings.Join(paqet.Presets(), ", "))
		}
		if preset != strings.ToLower(strings.TrimSpace(l.Preset)) {
			l.Preset = preset
			l.PckConns, l.PckKCPMode, l.PckKCPSndWnd, l.PckKCPRcvWnd = 0, "", 0, 0
			// Left over from a file the previous PCK wrote: it set the ring
			// for the old preset, and would hold the new one to it.
			l.SockBuf = 0
		}
	}
	if e.PckConns != nil {
		l.PckConns = *e.PckConns
	}
	if e.PckKCPMode != nil {
		l.PckKCPMode = strings.ToLower(strings.TrimSpace(*e.PckKCPMode))
	}
	if e.PckKCPMTU != nil {
		l.PckKCPMTU = *e.PckKCPMTU
	}
	if e.PckKCPSndWnd != nil {
		l.PckKCPSndWnd = *e.PckKCPSndWnd
	}
	if e.PckKCPRcvWnd != nil {
		l.PckKCPRcvWnd = *e.PckKCPRcvWnd
	}
	if e.PckKCPBlock != nil {
		l.PckKCPBlock = strings.ToLower(strings.TrimSpace(*e.PckKCPBlock))
	}
	return nil
}

// l3BlockOwner names the layer-3 tunnel on this machine that uses the block
// addr belongs to, or "".
func l3BlockOwner(addr string) string {
	block := l3Block(addr)
	for _, t := range List() {
		cfg, err := LoadTunnelConfig(t.Name)
		if err != nil || !cfg.L3.Enabled() {
			continue
		}
		if cfg.L3.Carrier == l3.CarrierPck {
			continue // no tunnel addresses
		}
		if l3Block(cfg.L3.LocalIP) == block || l3Block(cfg.L3.PeerIP) == block {
			return t.Name
		}
	}
	return ""
}

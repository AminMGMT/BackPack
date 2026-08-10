package manage

import (
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"strings"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/optimize"
)

// Everything in this file exists so a caller outside the package — the web
// panel — can build and change a tunnel through exactly the same code the CLI
// menu uses. The wizard in setup.go asks its questions one at a time because a
// terminal can only ask one at a time; a form asks them all at once. What must
// not differ is what happens with the answers, so the browser sends a filled
// spec here and this file walks it through the same ApplyPreset → validate →
// Save path the wizard walks.

// TransportOption is one selectable transport inside a family.
type TransportOption struct {
	Label string `json:"label"`
	Desc  string `json:"desc"`
	Value string `json:"value"`
}

// TransportFamily is one group of transports, as the setup menu asks for them:
// the kind of connection first, the specific variant second.
type TransportFamily struct {
	Label   string            `json:"label"`
	Desc    string            `json:"desc"`
	Entries []TransportOption `json:"entries"`
}

// TransportFamilies returns the same two-level transport menu the CLI shows,
// so the panel never drifts from it.
func TransportFamilies() []TransportFamily {
	out := make([]TransportFamily, 0, len(transportGroups))
	for _, g := range transportGroups {
		f := TransportFamily{Label: g.label, Desc: g.desc}
		for _, e := range g.entries {
			if e.value == "" {
				continue // listed for orientation only; not selectable
			}
			f.Entries = append(f.Entries, TransportOption{Label: e.label, Desc: e.desc, Value: e.value})
		}
		out = append(out, f)
	}
	return out
}

// PresetOption is one performance profile for the panel's preset menu.
type PresetOption struct {
	Label string `json:"label"`
	Desc  string `json:"desc"`
	Value string `json:"value"`
}

// Presets returns the performance profiles in menu order.
func Presets() []PresetOption {
	out := make([]PresetOption, len(presetOptions))
	for i, o := range presetOptions {
		out[i] = PresetOption{Label: o.label, Desc: o.desc, Value: o.value}
	}
	return out
}

// NewToken returns a fresh 64-character tunnel token — the same suggestion the
// setup wizard prints.
func NewToken() string { return randomToken(64) }

// SuggestPort returns a free four-digit port, for the panel's "roll a port"
// button. Four digits keeps it clear of the well-known range and of the
// ephemeral one, and every candidate is checked before it is offered so the
// suggestion cannot collide with something already listening.
func SuggestPort() int {
	for i := 0; i < 200; i++ {
		p := 1000 + rand.Intn(9000)
		if !PortInUse(strconv.Itoa(p)) {
			return p
		}
	}
	return 0
}

// FineTune is the set of advanced knobs the CLI offers under "Fine-tune the
// advanced settings by hand". Every field starts life holding the preset's
// value, so a form can be rendered from a preset and anything left untouched
// keeps exactly what the preset chose.
//
// Which fields are meaningful depends on the tunnel: ChannelSize and AcceptUDP
// are server-side, the pool is client-side, and the mux and KCP blocks only
// apply to the transports that use them. The values are carried regardless —
// Render writes only the ones that belong in the config.
type FineTune struct {
	Nodelay   bool   `json:"nodelay"`
	KeepAlive int    `json:"keepAlive"`
	Heartbeat int    `json:"heartbeat"`
	LogLevel  string `json:"logLevel"`
	LogJSON   bool   `json:"logJSON"`

	// MSS caps the largest TCP segment the tunnel sends. Zero — the default —
	// leaves it to the kernel. No preset sets it and a preset change does not
	// clear it: it belongs to the path, not to the performance profile. It is
	// carried on every tunnel but only means anything on the TCP-based
	// transports, so the form hides it elsewhere. See SetMSS.
	MSS int `json:"mss"`

	ChannelSize int  `json:"channelSize"` // server
	AcceptUDP   bool `json:"acceptUDP"`   // server

	ConnectionPool int  `json:"connectionPool"` // client
	AggressivePool bool `json:"aggressivePool"` // client

	MuxCon          int `json:"muxCon"`
	MuxVersion      int `json:"muxVersion"`
	MuxFrameSize    int `json:"muxFrameSize"`
	MuxRecvBuffer   int `json:"muxRecvBuffer"`
	MuxStreamBuffer int `json:"muxStreamBuffer"`

	KCPMTU          int `json:"kcpMTU"`
	KCPInterval     int `json:"kcpInterval"`
	KCPSndWnd       int `json:"kcpSndWnd"`
	KCPRcvWnd       int `json:"kcpRcvWnd"`
	KCPDataShards   int `json:"kcpDataShards"`
	KCPParityShards int `json:"kcpParityShards"`

	ZeroCopy bool `json:"zeroCopy"` // plain TCP only
}

// tuneOf reads the current advanced settings off a spec, so the panel's Fine
// Tune drawer opens on what the tunnel actually runs rather than on defaults.
func tuneOf(s TunnelSpec) FineTune {
	return FineTune{
		Nodelay:         s.Nodelay,
		KeepAlive:       s.KeepAlive,
		Heartbeat:       s.Heartbeat,
		LogLevel:        s.LogLevel,
		LogJSON:         s.LogFormat == "json",
		MSS:             s.MSS,
		ChannelSize:     s.ChannelSize,
		AcceptUDP:       s.AcceptUDP,
		ConnectionPool:  s.ConnectionPool,
		AggressivePool:  s.AggressivePool,
		MuxCon:          s.MuxCon,
		MuxVersion:      s.MuxVersion,
		MuxFrameSize:    s.MuxFrameSize,
		MuxRecvBuffer:   s.MuxRecvBuffer,
		MuxStreamBuffer: s.MuxStreamBuffer,
		KCPMTU:          s.KCPMTU,
		KCPInterval:     s.KCPInterval,
		KCPSndWnd:       s.KCPSndWnd,
		KCPRcvWnd:       s.KCPRcvWnd,
		KCPDataShards:   s.KCPDataShards,
		KCPParityShards: s.KCPParityShards,
		ZeroCopy:        s.ZeroCopy,
	}
}

// apply writes the advanced settings onto a spec. Like the CLI's manual
// tuning, it clears the preset: the numbers no longer match any profile, and
// leaving the label on would let a later preset change overwrite them silently.
//
// A zero is treated as "not answered" for the numeric knobs. A form that never
// opened the Fine Tune drawer posts zeros, and writing those through would give
// the tunnel a zero window or no heartbeat at all — the preset's value is the
// right answer there. The booleans are genuine answers and always applied.
func (f FineTune) apply(s *TunnelSpec) {
	s.Nodelay = f.Nodelay
	s.AggressivePool = f.AggressivePool
	s.AcceptUDP = f.AcceptUDP
	s.ZeroCopy = f.ZeroCopy && s.Transport == "tcp"
	// Heartbeat is the one number whose zero is meaningful — it disables the
	// heartbeat, which the CLI offers in as many words.
	s.Heartbeat = f.Heartbeat

	setInt(&s.KeepAlive, f.KeepAlive)
	setInt(&s.ChannelSize, f.ChannelSize)
	setInt(&s.ConnectionPool, f.ConnectionPool)
	setInt(&s.MuxCon, f.MuxCon)
	setInt(&s.MuxVersion, f.MuxVersion)
	setInt(&s.MuxFrameSize, f.MuxFrameSize)
	setInt(&s.MuxRecvBuffer, f.MuxRecvBuffer)
	setInt(&s.MuxStreamBuffer, f.MuxStreamBuffer)
	setInt(&s.KCPMTU, f.KCPMTU)
	setInt(&s.KCPInterval, f.KCPInterval)
	setInt(&s.KCPSndWnd, f.KCPSndWnd)
	setInt(&s.KCPRcvWnd, f.KCPRcvWnd)
	// FEC shards may legitimately be set to zero (error correction off), so
	// they are copied as given rather than through setInt. The MSS clamp is the
	// same shape of answer: zero means "let the kernel choose", which is a
	// setting rather than a blank, and clearing the box has to be able to
	// restore it.
	s.KCPDataShards = f.KCPDataShards
	s.KCPParityShards = f.KCPParityShards
	s.MSS = f.MSS

	switch strings.ToLower(strings.TrimSpace(f.LogLevel)) {
	case "debug", "info", "warn", "error":
		s.LogLevel = strings.ToLower(strings.TrimSpace(f.LogLevel))
	}
	if f.LogJSON {
		s.LogFormat = "json"
	} else {
		s.LogFormat = ""
	}
	s.Preset = ""
}

// setInt copies v over dst unless v is zero, which means "unanswered".
func setInt(dst *int, v int) {
	if v > 0 {
		*dst = v
	}
}

// PresetTune returns the advanced settings a preset would produce for a tunnel
// of this role and transport — what the panel's Fine Tune drawer shows before
// anything is edited, and what it resets to when the preset is changed.
func PresetTune(preset, role, transport string) FineTune {
	// AcceptUDP is not part of a preset — it is a defaulted setting — so it is
	// seeded here with the answer a new tunnel gets, or the drawer would show
	// the switch off on a tunnel that is about to forward UDP.
	s := TunnelSpec{Role: role, Transport: transport, AcceptUDP: true}
	ApplyPreset(&s, preset)
	return tuneOf(s)
}

// NewTunnel is a whole tunnel described in one go, as the panel's setup form
// collects it. It is the form's shape, not the config's: Ports and IPv6 are
// server-only, ServerAddr is client-only, and the rest is common.
type NewTunnel struct {
	Role       string `json:"role"` // "server" (Iran) or "client" (kharej)
	Transport  string `json:"transport"`
	Name       string `json:"name"`
	TunnelPort string `json:"tunnelPort"` // server: bind port — client: the server's port
	ServerAddr string `json:"serverAddr"` // client only: IP or domain of the server
	Token      string `json:"token"`
	Ports      string `json:"ports"` // server only: comma-separated forwarded ports
	Preset     string `json:"preset"`

	IPv6          bool `json:"ipv6"`          // server only: bind :: instead of 0.0.0.0
	ProxyProtocol bool `json:"proxyProtocol"` // server only

	// Tune is nil unless the operator opened the Fine Tune drawer, in which case
	// it replaces the preset's answers.
	Tune *FineTune `json:"tune"`
}

// CreateTunnel builds a tunnel from a filled form and starts it. It returns the
// service name, and whether that service came up — a tunnel whose port is taken
// is created and reported as not running, exactly as the CLI reports it, rather
// than being refused after the config was already written.
func CreateTunnel(n NewTunnel) (service string, active bool, err error) {
	// Forwarded ports carry UDP as well as TCP unless the Fine Tune drawer
	// turns it off, which is what the CLI wizard does too.
	s := TunnelSpec{AcceptUDP: true}

	switch n.Role {
	case "server", "client":
		s.Role = n.Role
	default:
		return "", false, fmt.Errorf("role must be server or client")
	}

	s.Transport = strings.ToLower(strings.TrimSpace(n.Transport))
	if !validTransport(s.Transport) {
		return "", false, fmt.Errorf("unknown transport %q", n.Transport)
	}

	s.Name = strings.TrimSpace(n.Name)
	if !validName(s.Name) {
		return "", false, fmt.Errorf("invalid name %q — use letters, digits, dots and dashes (max 40)", n.Name)
	}
	if fileExists(app.ConfigPath(s.Name)) {
		return "", false, fmt.Errorf("a tunnel named %q already exists", s.Name)
	}

	port := strings.TrimSpace(n.TunnelPort)
	if !validPort(port) {
		return "", false, fmt.Errorf("the tunnel port must be between 1 and 65535")
	}

	s.Token = strings.TrimSpace(n.Token)
	if s.Token == "" {
		return "", false, fmt.Errorf("a security token is required — both ends must use the same one")
	}

	if s.Role == "server" {
		// The IPv6 wildcard accepts IPv4 too on a dual-stack host, so this is
		// "IPv6 as well" rather than "IPv6 instead".
		bind := "0.0.0.0"
		if n.IPv6 {
			bind = "::"
		}
		s.BindAddr = net.JoinHostPort(bind, port)

		s.Ports = parsePorts(n.Ports)
		if len(s.Ports) == 0 {
			return "", false, fmt.Errorf("at least one forwarded port is required")
		}
		if err := validatePortSpecs(s.Ports); err != nil {
			return "", false, err
		}
		if supportsProxyProtocol(s.Transport) {
			s.ProxyProtocol = n.ProxyProtocol
		}
	} else {
		host := strings.Trim(strings.TrimSpace(n.ServerAddr), "[]")
		if host == "" {
			return "", false, fmt.Errorf("the server address is required")
		}
		s.RemoteAddr = net.JoinHostPort(host, port)
	}

	ApplyPreset(&s, n.Preset)
	if n.Tune != nil {
		n.Tune.apply(&s)
	}

	// A WSS server terminates TLS and cannot start without a certificate. The
	// wizard offers three ways to get one; the panel takes the one that always
	// works, and Edit → Certificate can move it to Let's Encrypt afterwards.
	if s.Role == "server" && needsTLS(s.Transport) {
		cert, key, err := EnsureSelfSignedCert(s.Name, "")
		if err != nil {
			return "", false, fmt.Errorf("could not generate a TLS certificate: %w", err)
		}
		s.TLSCert, s.TLSKey = cert, key
	}
	// Spoof carries a profile both ends must agree on. The wizard asks; the form
	// does not, so it gets the same default the wizard recommends.
	if s.Transport == "spoof" && s.SpoofProfile == "" {
		s.SpoofProfile = "udp"
	}

	optimize.ApplyQuiet()

	service, err = s.Save()
	if err != nil {
		return service, false, err
	}
	return service, IsActive(service), nil
}

// TunnelEdit is the set of changes the panel's Edit form can make. Every field
// is optional: an empty one leaves that setting exactly as it is.
type TunnelEdit struct {
	ServerAddr string    `json:"serverAddr"` // client only
	TunnelPort string    `json:"tunnelPort"`
	Ports      string    `json:"ports"` // server only, comma-separated
	Transport  string    `json:"transport"`
	Preset     string    `json:"preset"`
	Tune       *FineTune `json:"tune"`
}

// EditTunnelSettings applies every change in one pass and restarts the tunnel
// once. Doing it as one write matters: the same edit made through the
// individual calls (ChangeTransport, then ChangePreset, then EditTunnel) would
// restart the tunnel three times and leave it half-changed if the second failed.
// A failure here reverts to the config the tunnel had before, as every other
// edit does.
func EditTunnelSettings(name string, e TunnelEdit) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	changed := false

	if t := strings.ToLower(strings.TrimSpace(e.Transport)); t != "" && t != s.Transport {
		if err := switchTransport(&s, t); err != nil {
			return err
		}
		changed = true
	}

	// The preset is re-applied before the manual knobs, so a form that changes
	// both ends up with the preset as the baseline and the edits on top — the
	// order the CLI uses.
	if p := strings.TrimSpace(e.Preset); p != "" && (p != s.Preset || e.Tune != nil) {
		if !validPreset(p) {
			return fmt.Errorf("unknown preset %q", p)
		}
		ApplyPreset(&s, p)
		changed = true
	}
	if e.Tune != nil {
		e.Tune.apply(&s)
		changed = true
	}

	if port := strings.TrimSpace(e.TunnelPort); port != "" {
		if !validPort(port) {
			return fmt.Errorf("the tunnel port must be between 1 and 65535")
		}
		if s.Role == "server" {
			if addrPort(s.BindAddr) != port {
				s.BindAddr = net.JoinHostPort(addrHost(s.BindAddr, "0.0.0.0"), port)
				changed = true
			}
		} else if addrPort(s.RemoteAddr) != port {
			s.RemoteAddr = net.JoinHostPort(addrHost(s.RemoteAddr, ""), port)
			changed = true
		}
	}

	if host := strings.Trim(strings.TrimSpace(e.ServerAddr), "[]"); host != "" {
		if s.Role != "client" {
			return fmt.Errorf("the server address can only be changed on client tunnels")
		}
		p := addrPort(s.RemoteAddr)
		if !validPort(p) {
			return fmt.Errorf("this tunnel has no valid server port")
		}
		if addr := net.JoinHostPort(host, p); addr != s.RemoteAddr {
			s.RemoteAddr = addr
			changed = true
		}
	}

	if raw := strings.TrimSpace(e.Ports); raw != "" {
		if s.Role != "server" {
			return fmt.Errorf("forwarded ports exist only on server tunnels")
		}
		var clean []string
		for _, p := range parsePorts(raw) {
			if !isBotRelayPort(p, s.Token) {
				clean = append(clean, p)
			}
		}
		if len(clean) == 0 {
			return fmt.Errorf("at least one forwarded port is required")
		}
		if err := validatePortSpecs(clean); err != nil {
			return err
		}
		// Keep the hidden Telegram/SOCKS relay mapping the operator never sees.
		for _, p := range s.Ports {
			if isBotRelayPort(p, s.Token) {
				clean = append(clean, p)
			}
		}
		s.Ports = clean
		changed = true
	}

	if !changed {
		return fmt.Errorf("nothing to change")
	}
	return applySpec(s)
}

// TunnelSettings is a tunnel's current editable state, for filling the panel's
// Edit form.
type TunnelSettings struct {
	Name       string   `json:"name"`
	Role       string   `json:"role"`
	Transport  string   `json:"transport"`
	ServerHost string   `json:"serverHost"` // client only
	TunnelPort string   `json:"tunnelPort"`
	Ports      []string `json:"ports"` // server only, bot relay mapping hidden
	Preset     string   `json:"preset"`
	PresetName string   `json:"presetName"`
	Tune       FineTune `json:"tune"`
}

// TunnelSettingsOf reads a tunnel's editable settings from its config.
func TunnelSettingsOf(name string) (TunnelSettings, error) {
	s, err := LoadSpec(name)
	if err != nil {
		return TunnelSettings{}, err
	}
	out := TunnelSettings{
		Name:       s.Name,
		Role:       s.Role,
		Transport:  s.Transport,
		Preset:     s.Preset,
		PresetName: presetLabel(s.Preset),
		Tune:       tuneOf(s),
	}
	if s.Role == "server" {
		out.TunnelPort = addrPort(s.BindAddr)
		out.Ports = VisiblePorts(s.Ports, s.Token)
	} else {
		out.TunnelPort = addrPort(s.RemoteAddr)
		out.ServerHost = addrHost(s.RemoteAddr, "")
	}
	return out, nil
}

// Start, Stop and Restart drive one tunnel's service by tunnel name, so a
// caller never has to know how a service name is built.
func Start(name string) error   { return StartService(app.ServiceName(name)) }
func Stop(name string) error    { return StopService(app.ServiceName(name)) }
func Restart(name string) error { return RestartService(app.ServiceName(name)) }

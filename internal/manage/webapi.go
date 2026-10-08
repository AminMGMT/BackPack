package manage

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"reflect"
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

// TransportFamilies returns the ordinary engine transport menu. Managed HTTPS
// selections require the terminal's role-specific credential and TLS wizard.
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

// Presets returns the performance profiles in menu order. It lists every one,
// including those that only apply to some transports; KCPOnly says which
// entries a transport would drop, so the panel filters them client-side.
func Presets() []PresetOption {
	return append([]PresetOption(nil), presetOptions...)
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

	// sent is the set of keys a form actually posted, when it came from JSON.
	// See UnmarshalJSON. Nil means built in code, where every field counts.
	sent map[string]bool
}

// UnmarshalJSON records which keys the form sent as well as their values.
//
// A zero or a false in this struct was ambiguous: "the operator set it off" or
// "the form never mentioned it". The Add form posts only the boxes that were
// filled in, so a tunnel created with one Fine Tune switch touched had every
// other knob read as zero — heartbeat off, Nagle back on, FEC off — and its
// preset cleared. Knowing what was sent is what lets a missing key mean
// "leave the preset's value", which is what the form means by it.
func (f *FineTune) UnmarshalJSON(data []byte) error {
	type plain FineTune
	var v plain
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	*f = FineTune(v)
	f.sent = make(map[string]bool, len(keys))
	for k := range keys {
		f.sent[k] = true
	}
	return nil
}

// MarshalJSON writes only the answered keys of a drawer that knows which they
// are. The fleet sends a setup form to a managed server as JSON, and the far
// end's UnmarshalJSON reads every key present as answered — so a drawer carrying
// one setting arrived carrying all of them, and its zeros replaced the preset's
// there. A drawer with no marks means every field, and writes every field.
func (f FineTune) MarshalJSON() ([]byte, error) {
	type plain FineTune
	full, err := json.Marshal(plain(f))
	if err != nil || f.sent == nil {
		return full, err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(full, &keys); err != nil {
		return nil, err
	}
	for k := range keys {
		if !f.sent[k] {
			delete(keys, k)
		}
	}
	return json.Marshal(keys)
}

// has reports whether the form sent this key; code-built values have them all.
func (f FineTune) has(key string) bool { return f.sent == nil || f.sent[key] }

// fineTuneKeys maps each JSON key to its field index, once.
var fineTuneKeys = func() map[string]int {
	t := reflect.TypeOf(FineTune{})
	m := map[string]int{}
	for i := 0; i < t.NumField(); i++ {
		if tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
			m[tag] = i
		}
	}
	return m
}()

// notPreset are the knobs no preset sets. Changing them is not a departure
// from the profile, so it does not clear the tunnel's preset.
var notPreset = map[string]bool{"logLevel": true, "logJSON": true, "acceptUDP": true, "mss": true, "zeroCopy": true}

// changedFrom keeps only what the form changed from the values it was filled
// with. The Edit form posts every field it shows, and it shows the tunnel's
// current values — so a preset changed on the same form came with the old
// preset's numbers beside it, and applying them all put the old numbers back
// over the new preset and cleared its name: a tunnel moved to Turbo stayed on
// Balance's settings and read "Custom" (reported as "it always goes back to
// Balance"). Unchanged fields now leave whatever the preset chose.
func (f FineTune) changedFrom(before FineTune) FineTune {
	out := f
	out.sent = map[string]bool{}
	fv, bv := reflect.ValueOf(f), reflect.ValueOf(before)
	for key, i := range fineTuneKeys {
		if f.has(key) && !reflect.DeepEqual(fv.Field(i).Interface(), bv.Field(i).Interface()) {
			out.sent[key] = true
		}
	}
	return out
}

// touchesPreset reports whether applying f moves the tunnel off its preset.
func (f FineTune) touchesPreset() bool {
	for key := range fineTuneKeys {
		if f.has(key) && !notPreset[key] {
			return true
		}
	}
	return false
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
	if f.has("nodelay") {
		s.Nodelay = f.Nodelay
	}
	if f.has("aggressivePool") {
		s.AggressivePool = f.AggressivePool
	}
	if f.has("acceptUDP") {
		s.AcceptUDP = f.AcceptUDP
	}
	if f.has("zeroCopy") {
		s.ZeroCopy = f.ZeroCopy && s.Transport == "tcp"
	}
	// Heartbeat is the one number whose zero is meaningful — it disables the
	// heartbeat, which the CLI offers in as many words.
	if f.has("heartbeat") {
		s.Heartbeat = f.Heartbeat
	}

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
	if f.has("kcpDataShards") {
		s.KCPDataShards = f.KCPDataShards
	}
	if f.has("kcpParityShards") {
		s.KCPParityShards = f.KCPParityShards
	}
	if f.has("mss") {
		s.MSS = f.MSS
	}

	if f.has("logLevel") {
		switch strings.ToLower(strings.TrimSpace(f.LogLevel)) {
		case "debug", "info", "warn", "error":
			s.LogLevel = strings.ToLower(strings.TrimSpace(f.LogLevel))
		}
	}
	if f.has("logJSON") {
		if f.LogJSON {
			s.LogFormat = "json"
		} else {
			s.LogFormat = ""
		}
	}
	// Like the CLI's manual tuning, a changed number clears the preset: the
	// settings no longer match any profile, and leaving the label on would let
	// a later preset change overwrite them silently. A log level or the UDP
	// switch is not a departure from the profile, and leaves it.
	if f.touchesPreset() {
		s.Preset = ""
	}
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
	// seeded here with the answer a new tunnel gets: off. A tunnel forwards UDP
	// only when the operator turns it on. See config.ServerConfig.ForwardsUDP.
	s := TunnelSpec{Role: role, Transport: transport, AcceptUDP: false}
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

	// The advanced drawers, each nil unless it was opened. Pck only means
	// anything on its own transport; Conn carries the connectivity options the
	// wizard asks the client for; Limits caps the tunnel as a whole.
	Pck    *PckTune      `json:"pck"`
	Conn   *ConnTune     `json:"conn"`
	Limits *TunnelLimits `json:"limits"`
}

// CreateTunnel builds a tunnel from a filled form and starts it. It returns the
// service name, and whether that service came up — a tunnel whose port is taken
// is created and reported as not running, exactly as the CLI reports it, rather
// than being refused after the config was already written.
func CreateTunnel(n NewTunnel) (service string, active bool, err error) {
	// Checked before the form is turned into a configuration, because building
	// one has side effects — a WSS server generates a certificate — and doing
	// that for a tunnel that is about to be refused leaves files behind.
	if name := strings.TrimSpace(n.Name); validName(name) && fileExists(app.ConfigPath(name)) {
		return "", false, fmt.Errorf("a tunnel named %q already exists", name)
	}
	s, err := specFromNew(n)
	if err != nil {
		return "", false, err
	}
	if err := refuseBusyForwards(s); err != nil {
		return "", false, err
	}
	service, err = s.Save()
	if err != nil {
		return service, false, err
	}
	return service, IsActive(service), nil
}

// ApplyTunnel writes the tunnel this form describes, whether or not it is
// already there.
//
// It is the operation a managed node performs, where create and edit are not
// two different things: the panel sends the complete state a tunnel should be
// in, and this puts the machine in that state. An existing tunnel goes through
// applySpec, so a configuration that will not start is rolled back and the
// previous one is filed, exactly as an edit made on this machine would be.
func ApplyTunnel(n NewTunnel) (service string, active bool, created bool, err error) {
	s, err := specFromNew(n)
	if err != nil {
		return "", false, false, err
	}
	service = app.ServiceName(s.Name)
	if !fileExists(app.ConfigPath(s.Name)) {
		if err := refuseBusyForwards(s); err != nil {
			return service, false, true, err
		}
		service, err = s.Save()
		if err != nil {
			return service, false, true, err
		}
		return service, IsActive(service), true, nil
	}
	if err := applySpec(s); err != nil {
		return service, IsActive(service), false, err
	}
	return service, IsActive(service), false, nil
}

// refuseBusyForwards is the panel's copy of the wizard's refusal: a new Iran
// server whose forwarded port is already held here, or is its own tunnel port,
// would be written and then fail to bind it. See reverseBusyPorts.
func refuseBusyForwards(s TunnelSpec) error {
	if s.Role != "server" {
		return nil
	}
	if busy := reverseBusyPorts(s.Ports, s.BindAddr, s.Transport); len(busy) > 0 {
		return fmt.Errorf("already in use on this server: %s — the tunnel port or the web "+
			"panel's own port is the usual one; pick other ports", strings.Join(busy, ", "))
	}
	return nil
}

// specFromNew turns a filled setup form into the configuration it describes.
// It writes nothing.
func specFromNew(n NewTunnel) (TunnelSpec, error) {
	// Forwarded ports carry TCP only unless the Fine Tune drawer turns UDP on,
	// which is what the CLI wizard defaults to too. See ForwardsUDP for why the
	// default is off.
	s := TunnelSpec{AcceptUDP: false}

	switch n.Role {
	case "server", "client":
		s.Role = n.Role
	default:
		return s, fmt.Errorf("role must be server or client")
	}

	s.Transport = strings.ToLower(strings.TrimSpace(n.Transport))
	if !validTransport(s.Transport) {
		return s, fmt.Errorf("unknown transport %q", n.Transport)
	}

	s.Name = strings.TrimSpace(n.Name)
	if !validName(s.Name) {
		return s, fmt.Errorf("invalid name %q — use letters, digits, dots and dashes (max 40)", n.Name)
	}

	// Accepts "443" and "85.10.11.51:443" alike. See tunnelbind.go: an address
	// pins the control channel to one of a multi-homed server's interfaces,
	// which is what lets it share a port number with a forwarded port.
	bind, err := parseTunnelBind(n.TunnelPort)
	if err != nil {
		return s, fmt.Errorf("the tunnel port is not valid: %w", err)
	}
	port := bind.Port

	s.Token = strings.TrimSpace(n.Token)
	if s.Token == "" {
		return s, fmt.Errorf("a security token is required — both ends must use the same one")
	}

	if s.Role == "server" {
		// The IPv6 wildcard accepts IPv4 too on a dual-stack host, so the flag
		// is "IPv6 as well" rather than "IPv6 instead". It only decides the
		// wildcard family: an operator who named an address has answered the
		// question already.
		s.BindAddr = bind.Addr(n.IPv6)

		s.Ports = parsePorts(n.Ports)
		if len(s.Ports) == 0 {
			return s, fmt.Errorf("at least one forwarded port is required")
		}
		if err := validatePortSpecs(s.Ports); err != nil {
			return s, err
		}
		if supportsProxyProtocol(s.Transport) {
			s.ProxyProtocol = n.ProxyProtocol
		}
	} else {
		if bind.HasHost() {
			return s, fmt.Errorf("a client binds nothing — its tunnel port is the port on the " +
				"server, so it takes a port alone")
		}
		host := strings.Trim(strings.TrimSpace(n.ServerAddr), "[]")
		if host == "" {
			return s, fmt.Errorf("the server address is required")
		}
		s.RemoteAddr = net.JoinHostPort(host, port)
	}

	// Refused here, where it is still a typo, rather than discovered later from
	// a log that only says EOF. See portClash.
	addr := s.BindAddr
	if s.Role == "client" {
		addr = s.RemoteAddr
	}
	if why := portClash(s.Role, addr, s.Name); why != "" {
		return s, fmt.Errorf("%s", why)
	}

	// A preset this build does not know is a refusal, not a default.
	//
	// ApplyPreset falls back to Turbo for anything it does not recognise, which
	// is right for a config already on disk — it must keep loading — and wrong
	// for a form. Asking for "balanced" and getting Turbo without a word is the
	// kind of quiet substitution that is only discovered later, by wondering why
	// a tunnel behaves like a profile nobody chose. The edit path has always
	// refused it; creating did not, and the two are the same question.
	if p := strings.TrimSpace(n.Preset); p != "" && !validPreset(p) {
		return s, fmt.Errorf("unknown preset %q — choose balance, turbo or aggressive", p)
	}

	// Caught here rather than silently applied, for the reason given in the edit
	// path: on a kernel-stack transport this profile's knobs would be written and
	// then ignored.
	if !presetSuitsTransport(n.Preset, s.Transport) {
		return s, fmt.Errorf("the %s preset applies to the udp+kcp+fec transport only, not %q",
			presetLabel(n.Preset), s.Transport)
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
			return s, fmt.Errorf("could not generate a TLS certificate: %w", err)
		}
		s.TLSCert, s.TLSKey = cert, key
	}
	// The advanced drawers, in the order the wizard asks them. A form that never
	// opened one sends nothing for it, and the tunnel keeps the defaults.
	if err := applyAdvanced(&s, n.Pck, n.Conn, n.Limits, port); err != nil {
		return s, err
	}
	optimize.ApplyQuiet(ReservedPorts())
	return s, nil
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

	// The advanced drawers, nil unless the form opened one. They are the same
	// blocks the setup form fills, so a setting can be changed afterwards
	// wherever it could be chosen in the first place — which is what the CLI's
	// Edit screen offers and what the panel used to be missing.
	Pck    *PckTune      `json:"pck"`
	Conn   *ConnTune     `json:"conn"`
	Limits *TunnelLimits `json:"limits"`

	// ProxyProtocol is a pointer because false is an answer: a plain bool could
	// not tell "turn it off" from "the form did not mention it".
	ProxyProtocol *bool `json:"proxyProtocol"`

	// The certificate, on the transports that present one. Pointers for the
	// same reason: an empty string means "go back to the self-signed one",
	// which is a different instruction from "leave the certificate alone".
	ACMEDomain *string `json:"acmeDomain"`
	ACMEEmail  *string `json:"acmeEmail"`
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
	// What the form was filled with, so only what the operator changed on it
	// is applied. See changedFrom.
	shown := tuneOf(s)

	// In this order, which is load-bearing: the transport first, because it
	// decides what the rest may be; the preset before the manual knobs, so a
	// form that changes both ends up with the preset as the baseline and the
	// edits on top — the order the CLI uses; and the advanced drawers after the
	// port has settled, so a backup address written without one inherits the
	// port the tunnel is being left on.
	changed := false
	for _, step := range []func(*TunnelSpec) (bool, error){
		e.editTransport,
		func(s *TunnelSpec) (bool, error) { return e.editTuning(s, shown) },
		e.editCertificate,
		e.editTunnelPort,
		e.editServerAddr,
		e.editPorts,
		e.editAdvanced,
		e.editProxyProtocol,
	} {
		c, err := step(&s)
		if err != nil {
			return err
		}
		changed = changed || c
	}
	if !changed {
		return fmt.Errorf("nothing to change")
	}
	return applySpec(s)
}

// Each edit step below applies one part of a TunnelEdit to the spec and
// reports whether it changed anything.

// editTransport switches the transport and drops what belonged to the old one.
func (e TunnelEdit) editTransport(s *TunnelSpec) (bool, error) {
	t := strings.ToLower(strings.TrimSpace(e.Transport))
	if t == "" || t == s.Transport {
		return false, nil
	}
	if err := switchTransport(s, t); err != nil {
		return false, err
	}
	// Settings that belonged to the old carrier are dropped rather than left in
	// the file for nothing to read — an edge IP on a KCP tunnel or a forged
	// source on a websocket one is a setting that looks live and is not, which
	// is the hardest kind to debug.
	clearForTransport(s)
	return true, nil
}

// editTuning applies the preset, then whichever manual knobs the operator
// changed on the form (compared against shown, what the form was filled with).
func (e TunnelEdit) editTuning(s *TunnelSpec, shown FineTune) (bool, error) {
	changed := false
	if p := strings.TrimSpace(e.Preset); p != "" && p != s.Preset {
		if !validPreset(p) {
			return false, fmt.Errorf("unknown preset %q", p)
		}
		// Refused rather than quietly applied: on a transport whose congestion
		// control belongs to the kernel, every knob this profile changes would be
		// written to the config and ignored, which looks like a setting that took.
		if !presetSuitsTransport(p, string(s.Transport)) {
			return false, fmt.Errorf("the %s preset applies to the udp+kcp+fec transport only, not %q",
				presetLabel(p), s.Transport)
		}
		ApplyPreset(s, p)
		changed = true
	}
	if e.Tune != nil {
		if t := e.Tune.changedFrom(shown); len(t.sent) > 0 {
			t.apply(s)
			changed = true
		}
	}
	return changed, nil
}

// editCertificate works on the same terms as the CLI's Edit screen: only a
// server on a TLS transport has one, and the self-signed pair stays on disk
// either way because it is what the config still points at when Let's Encrypt
// has nothing to offer yet.
func (e TunnelEdit) editCertificate(s *TunnelSpec) (bool, error) {
	if e.ACMEDomain == nil && e.ACMEEmail == nil {
		return false, nil
	}
	domain := s.ACMEDomain
	if e.ACMEDomain != nil {
		domain = strings.ToLower(strings.TrimSpace(*e.ACMEDomain))
	}
	email := s.ACMEEmail
	if e.ACMEEmail != nil {
		email = strings.TrimSpace(*e.ACMEEmail)
	}
	if domain != "" {
		if s.Role != "server" {
			return false, fmt.Errorf("the certificate is a server-side setting — the client does not present one")
		}
		if !needsTLS(s.Transport) {
			return false, fmt.Errorf("transport %s does not use TLS, so it presents no certificate", s.Transport)
		}
		if net.ParseIP(domain) != nil {
			return false, fmt.Errorf("Let's Encrypt cannot issue a certificate for an IP address — use a domain name")
		}
	}
	if domain == s.ACMEDomain && email == s.ACMEEmail {
		return false, nil
	}
	s.ACMEDomain, s.ACMEEmail = domain, email
	if needsTLS(s.Transport) && s.Role == "server" && (s.TLSCert == "" || !fileExists(s.TLSCert)) {
		cert, key, err := EnsureSelfSignedCert(s.Name, domain)
		if err != nil {
			return false, fmt.Errorf("could not prepare the self-signed certificate: %w", err)
		}
		s.TLSCert, s.TLSKey = cert, key
	}
	return true, nil
}

// editTunnelPort moves the tunnel port: where a server binds, or the port a
// client dials on the server.
func (e TunnelEdit) editTunnelPort(s *TunnelSpec) (bool, error) {
	spec := strings.TrimSpace(e.TunnelPort)
	if spec == "" {
		return false, nil
	}
	bind, err := parseTunnelBind(spec)
	if err != nil {
		return false, fmt.Errorf("the tunnel port is not valid: %w", err)
	}
	if s.Role == "server" {
		// An address pins the tunnel to it; a bare port keeps whatever this
		// tunnel already binds. Widening a pinned tunnel back to every
		// interface is therefore said explicitly: 0.0.0.0:443.
		want := net.JoinHostPort(addrHost(s.BindAddr, "0.0.0.0"), bind.Port)
		if bind.HasHost() {
			want = bind.Addr(false)
		}
		if want == s.BindAddr {
			return false, nil
		}
		s.BindAddr = want
		return true, nil
	}
	if bind.HasHost() {
		return false, fmt.Errorf("a client binds nothing — its tunnel port is the port on the " +
			"server, so it takes a port alone. Change where it dials with the server address instead")
	}
	if addrPort(s.RemoteAddr) == bind.Port {
		return false, nil
	}
	s.RemoteAddr = net.JoinHostPort(addrHost(s.RemoteAddr, ""), bind.Port)
	return true, nil
}

// editServerAddr changes where a client dials, keeping its port.
func (e TunnelEdit) editServerAddr(s *TunnelSpec) (bool, error) {
	host := strings.Trim(strings.TrimSpace(e.ServerAddr), "[]")
	if host == "" {
		return false, nil
	}
	if s.Role != "client" {
		return false, fmt.Errorf("the server address can only be changed on client tunnels")
	}
	p := addrPort(s.RemoteAddr)
	if !validPort(p) {
		return false, fmt.Errorf("this tunnel has no valid server port")
	}
	addr := net.JoinHostPort(host, p)
	if addr == s.RemoteAddr {
		return false, nil
	}
	s.RemoteAddr = addr
	return true, nil
}

// editPorts replaces a server's forwarded ports, keeping the hidden relay
// mapping the operator never sees.
func (e TunnelEdit) editPorts(s *TunnelSpec) (bool, error) {
	raw := strings.TrimSpace(e.Ports)
	if raw == "" {
		return false, nil
	}
	if s.Role != "server" {
		return false, fmt.Errorf("forwarded ports exist only on server tunnels")
	}
	var clean []string
	for _, p := range parsePorts(raw) {
		if !isBotRelayPort(p, s.Token) {
			clean = append(clean, p)
		}
	}
	if len(clean) == 0 {
		return false, fmt.Errorf("at least one forwarded port is required")
	}
	if err := validatePortSpecs(clean); err != nil {
		return false, err
	}
	// Keep the hidden Telegram/SOCKS relay mapping the operator never sees.
	for _, p := range s.Ports {
		if isBotRelayPort(p, s.Token) {
			clean = append(clean, p)
		}
	}
	s.Ports = clean
	return true, nil
}

// editAdvanced applies the advanced drawers: pck, connection and limits.
func (e TunnelEdit) editAdvanced(s *TunnelSpec) (bool, error) {
	if e.Pck == nil && e.Conn == nil && e.Limits == nil {
		return false, nil
	}
	port := addrPort(s.BindAddr)
	if s.Role == "client" {
		port = addrPort(s.RemoteAddr)
	}
	if err := applyAdvanced(s, e.Pck, e.Conn, e.Limits, port); err != nil {
		return false, err
	}
	return true, nil
}

// editProxyProtocol turns the PROXY protocol header on or off, where the
// transport can carry one.
func (e TunnelEdit) editProxyProtocol(s *TunnelSpec) (bool, error) {
	if e.ProxyProtocol == nil {
		return false, nil
	}
	if s.Role != "server" {
		return false, fmt.Errorf("the PROXY protocol header is added by the server side")
	}
	if *e.ProxyProtocol && !supportsProxyProtocol(s.Transport) {
		return false, fmt.Errorf("the %s transport has nowhere to put a PROXY protocol header", s.Transport)
	}
	if s.ProxyProtocol == *e.ProxyProtocol {
		return false, nil
	}
	s.ProxyProtocol = *e.ProxyProtocol
	return true, nil
}

// TunnelSettings is a tunnel's current editable state, for filling the panel's
// Edit form.
type TunnelSettings struct {
	Name       string `json:"name"`
	Role       string `json:"role"`
	Transport  string `json:"transport"`
	ServerHost string `json:"serverHost"` // client only
	TunnelPort string `json:"tunnelPort"`

	// BindHost is the one address a server tunnel's control port is pinned to,
	// or "" when it listens on every interface.
	//
	// Kept apart from TunnelPort rather than folded into it, because that field
	// is an identity as well as a setting: adopt.go pairs the two ends of a
	// tunnel by comparing it, and the far end knows the port but has no idea
	// which of this machine's addresses the port was bound to. Merging them
	// would silently stop every pinned tunnel from matching its own other half.
	BindHost   string   `json:"bindHost,omitempty"` // server only
	Ports      []string `json:"ports"`              // server only, bot relay mapping hidden
	Preset     string   `json:"preset"`
	PresetName string   `json:"presetName"`
	Tune       FineTune `json:"tune"`

	// The advanced drawers open on what the tunnel actually runs, so a value
	// that was set by hand in the config file — or by the CLI wizard — shows up
	// here instead of the form pretending it was never set.
	Pck           PckTune      `json:"pck"`
	Conn          ConnTune     `json:"conn"`
	Limits        TunnelLimits `json:"limits"`
	ProxyProtocol bool         `json:"proxyProtocol"`

	// The certificate a TLS transport presents. Empty ACMEDomain means the
	// self-signed pair generated when the tunnel was made. The panel could
	// never see or change this — only the CLI could — so a WSS tunnel set up
	// from the panel stayed on a certificate no browser trusts, with nothing on
	// the screen to say why or what to do about it.
	ACMEDomain string `json:"acmeDomain,omitempty"`
	ACMEEmail  string `json:"acmeEmail,omitempty"`
}

// TunnelSettingsOf reads a tunnel's editable settings from its config.
func TunnelSettingsOf(name string) (TunnelSettings, error) {
	s, err := LoadSpec(name)
	if err != nil {
		return TunnelSettings{}, err
	}
	out := TunnelSettings{
		Name:          s.Name,
		Role:          s.Role,
		Transport:     s.Transport,
		Preset:        s.Preset,
		PresetName:    presetLabel(s.Preset),
		Tune:          tuneOf(s),
		Pck:           pckOf(s),
		Conn:          connOf(s),
		Limits:        limitsOf(s),
		ProxyProtocol: s.ProxyProtocol,
		ACMEDomain:    s.ACMEDomain,
		ACMEEmail:     s.ACMEEmail,
	}
	if s.Role == "server" {
		out.TunnelPort = addrPort(s.BindAddr)
		out.BindHost = bindHostOf(s.BindAddr)
		out.Ports = VisiblePorts(s.Ports, s.Token)
	} else {
		out.TunnelPort = addrPort(s.RemoteAddr)
		out.ServerHost = addrHost(s.RemoteAddr, "")
	}
	return out, nil
}

// Start, Stop and Restart drive one tunnel's service by tunnel name, so a
// caller never has to know how a service name is built.

// Start starts the named tunnel's service.
func Start(name string) error { return StartService(app.ServiceName(name)) }

// Stop stops the named tunnel's service.
func Stop(name string) error { return StopService(app.ServiceName(name)) }

// Restart restarts the named tunnel's service.
func Restart(name string) error { return RestartService(app.ServiceName(name)) }

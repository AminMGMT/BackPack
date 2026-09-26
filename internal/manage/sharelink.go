package manage

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/backpack/backpack/config"
)

// Handing a tunnel's settings to the other server.
//
// Everything in a tunnel is paired. The token, the transport, the port, the
// packet profile, Stealth, the error-correction scheme, the socket count — the
// codebase says "both ends must match" sixty times, and it says it because a
// pair that does not match produces the worst failure this system has: the
// tunnel comes up, reports itself connected, and carries nothing. Nobody is
// told, because from each end's own point of view everything is fine.
//
// The panel's answer used to be a list of four values with a Copy button each,
// and an instruction to type them in again over there. That works for four
// values and stops working the moment a carrier has six more — which is exactly
// when getting one wrong costs the most.
//
// A share link is those settings in one string. The side that was set up first
// produces it; the other side pastes it into its own form, which fills in and
// is then reviewed and submitted like any other. Nothing is executed: it is a
// form filler, not a command.
//
// # What it is not
//
// It is not encrypted, and it should not be described as if it were. Like the
// vmess links it is modelled on, it is a compressed, encoded payload that
// anybody holding it can read — and it carries the token, which is the whole
// credential. A link pasted into a group chat is a tunnel somebody else can
// join. The panel says that next to the button; this package will not pretend
// otherwise.

// shareScheme and shareVersion make the string recognisable and let a later
// format be told apart before anything is decoded.
const (
	shareScheme  = "backpack://"
	shareVersion = "1"
)

// ShareLink is the paired half of a tunnel's settings — everything the two ends
// must agree on, and nothing that is local to one of them.
//
// The field names are short because they travel. What decides whether a setting
// belongs here is one question: would the tunnel be silently broken if the two
// ends disagreed about it? An interface name, a socket buffer or a tunnel's own
// name would not, so they are not here and the receiving side keeps its own.
type ShareLink struct {
	V     int    `json:"v"`           // format version
	Kind  string `json:"k"`           // "reverse" or "direct"
	From  string `json:"f"`           // "iran" or "kharej" — the side that made it
	Name  string `json:"n,omitempty"` // a suggestion, not a requirement
	Tok   string `json:"t"`           // the shared secret
	Tr    string `json:"tr"`          // reverse: transport. direct: carrier
	Encap string `json:"e,omitempty"` // direct only
	// SNI is the domain the sni carrier announces. Both ends should announce
	// the same one — each is only telling the box in front of it what to
	// think, but two different names on one tunnel is a thing to remember for
	// no benefit.
	SNI  string `json:"sni,omitempty"` // direct, sni carrier only
	Port string `json:"p"`             // the tunnel port
	Host string `json:"h,omitempty"`   // the producing side's real address

	Preset    string `json:"pr,omitempty"`
	Ports     string `json:"po,omitempty"` // forwarded ports, as the Iran side has them
	AcceptUDP bool   `json:"u,omitempty"`
	MSS       int    `json:"m,omitempty"`
	MTU       int    `json:"mt,omitempty"`

	// The layer-3 pair of addresses, as the producer sees them. The receiver
	// swaps them, which is the whole of the inversion at this layer.
	LocalIP string `json:"li,omitempty"`
	PeerIP  string `json:"pi,omitempty"`

	FECData   int `json:"fd,omitempty"`
	FECParity int `json:"fp,omitempty"`
	Paths     int `json:"pa,omitempty"`

	// Direct only, and all three paired. They were left out, and a tunnel
	// whose Iran side had set one of them came up on the kharej without it:
	// a GRE key the other end did not expect drops every packet, and so does
	// a segment cap or an MTU the two ends disagree on.
	GREKey  uint32 `json:"gk,omitempty"`
	L3MSS   int    `json:"lm,omitempty"`
	AutoMTU *bool  `json:"am,omitempty"`

	// The forged-source carrier's paired half. SrcIPs is what the PRODUCER
	// forges — the receiver turns it into what it expects from its peer, which
	// is the pairing people get wrong most and the one no wizard question can
	// really help with.
	Profile   string `json:"sp,omitempty"`
	Uplink    string `json:"su,omitempty"`
	Downlink  string `json:"sd,omitempty"`
	SrcIPs    string `json:"ss,omitempty"`
	Stealth   bool   `json:"sl,omitempty"`
	ICMPReply bool   `json:"si,omitempty"`

	// Reverse only, and paired. SimpleAuth changes how a wss client proves
	// itself, and a server expecting the other kind refuses it; the smux
	// version is negotiated and must match. For the kcp transport FECData and
	// FECParity above carry its error-correction pair, which must match too —
	// zero on both meaning off.
	SimpleAuth bool `json:"sa,omitempty"`
	MuxVer     int  `json:"mv,omitempty"`
}

// Encode renders a link as the string an operator copies.
//
// The payload is gzipped before it is encoded, which shortens it and — the part
// that matters more — makes a truncated paste fail loudly: gzip carries its own
// CRC, so half a link is a decode error rather than a tunnel built from half the
// settings.
func (l ShareLink) Encode() (string, error) {
	l.V = 1
	// Refuse rather than corrupt.
	//
	// The payload is JSON, and encoding/json replaces any byte sequence that is
	// not valid UTF-8 with U+FFFD. It does that silently and it does it to the
	// token — so a token carrying a stray byte, from a paste or a terminal in
	// another encoding, arrives at the other end as a *different* token. The
	// tunnel then refuses every connection for the one reason nothing on either
	// machine reports: the two ends do not hold the same secret. The whole point
	// of a setup link is to remove the manual retyping step, so quietly changing
	// the secret it carries is the worst thing it could do.
	//
	// Every string field is checked rather than a chosen few. The first version
	// of this named four of them and the fuzzer immediately found a fifth; a
	// hand-written list is a list that goes stale the next time this struct
	// gains a field, and the failure it lets through is silent.
	//
	// Found by FuzzShareLinkRoundTrip.
	if field, ok := firstUncarryableField(l); !ok {
		return "", fmt.Errorf("the %s contains a byte that cannot be carried in a setup "+
			"link — retype it, or the other end would receive something different", field)
	}
	raw, err := json.Marshal(l)
	if err != nil {
		return "", fmt.Errorf("could not build the setup link: %w", err)
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if _, err := zw.Write(raw); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	return shareScheme + shareVersion + "." + base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

// DecodeShareLink parses a pasted string.
//
// Every failure here is something an operator can act on, so each says which of
// them it was: the wrong kind of string, a version this build does not know, or
// a link that arrived damaged — which is nearly always a copy that stopped
// short.
func DecodeShareLink(s string) (ShareLink, error) {
	var out ShareLink
	s = strings.TrimSpace(s)
	if s == "" {
		return out, fmt.Errorf("paste the setup link from the other server")
	}
	if !strings.HasPrefix(s, shareScheme) {
		return out, fmt.Errorf("that does not look like a Backpack setup link — it should begin with %s", shareScheme)
	}
	body := strings.TrimPrefix(s, shareScheme)
	ver, payload, ok := strings.Cut(body, ".")
	if !ok {
		return out, fmt.Errorf("the setup link is incomplete — copy the whole of it, including the end")
	}
	if ver != shareVersion {
		return out, fmt.Errorf("this setup link is version %s and this server understands version %s — "+
			"update the older of the two machines", ver, shareVersion)
	}
	gz, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return out, fmt.Errorf("the setup link is damaged — copy it again, all of it")
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return out, fmt.Errorf("the setup link is damaged — copy it again, all of it")
	}
	defer zr.Close()
	// Bounded: a link is a few hundred bytes, and a decompressor should never be
	// handed an unbounded read from something pasted in.
	raw, err := io.ReadAll(io.LimitReader(zr, 64<<10))
	if err != nil {
		return out, fmt.Errorf("the setup link is incomplete — copy the whole of it, including the end")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("the setup link is damaged — copy it again, all of it")
	}
	if out.Tok == "" || out.Tr == "" {
		return out, fmt.Errorf("the setup link is missing the token or the transport — it was not made by this version")
	}
	switch out.Kind {
	case "reverse", "direct":
	default:
		return out, fmt.Errorf("the setup link does not say what kind of tunnel it is")
	}
	return out, nil
}

// PeerSide is the side a link is meant to be pasted into: the opposite of the
// one that produced it.
func (l ShareLink) PeerSide() string {
	if strings.EqualFold(l.From, "iran") {
		return "kharej"
	}
	return "iran"
}

// PeerForm is the receiving side's form, filled from a link.
//
// It carries the values AND the list of fields that came from the link, so the
// panel can warn when one of them is edited without keeping its own idea of
// which settings are paired. That list is the contract, and it lives here for
// the same reason the mirroring does: two copies of it would eventually
// disagree, and the disagreement would be invisible until a tunnel went quiet.
type PeerForm struct {
	Kind string `json:"kind"` // "reverse" or "direct"
	Side string `json:"side"` // the side this form is for
	Name string `json:"name,omitempty"`

	Transport  string `json:"transport,omitempty"`  // reverse
	Carrier    string `json:"carrier,omitempty"`    // direct
	SNIDomain  string `json:"sniDomain,omitempty"`  // direct, sni carrier only
	Encap      string `json:"encap,omitempty"`      // direct
	ServerAddr string `json:"serverAddr,omitempty"` // the address this side dials
	TunnelPort string `json:"tunnelPort,omitempty"`
	Token      string `json:"token,omitempty"`
	Ports      string `json:"ports,omitempty"`
	AcceptUDP  bool   `json:"acceptUdp,omitempty"`
	Preset     string `json:"preset,omitempty"`
	MSS        int    `json:"mss,omitempty"`
	MTU        int    `json:"mtu,omitempty"`

	LocalIP string `json:"localIp,omitempty"`
	PeerIP  string `json:"peerIp,omitempty"`

	Paths int  `json:"paths,omitempty"`
	FEC   bool `json:"fec,omitempty"`
	// The exact error-correction pair. FEC above says only whether it is on,
	// and a far end given "on" took the recommended pair — not the one the
	// producer had chosen, and two different pairs cannot decode each other.
	FECData   int `json:"fecData,omitempty"`
	FECParity int `json:"fecParity,omitempty"`

	GREKey   uint32 `json:"greKey,omitempty"`   // direct
	MSSClamp int    `json:"mssClamp,omitempty"` // direct
	AutoMTU  *bool  `json:"autoMtu,omitempty"`  // direct

	Spoof       *SpoofTune `json:"spoof,omitempty"`
	Stealth     bool       `json:"stealth,omitempty"`
	SpoofPeerIP string     `json:"spoofPeerIp,omitempty"`

	// Reverse only: how a wss client proves itself, and the smux version.
	// Both must match the server's.
	SimpleAuth bool `json:"simpleAuth,omitempty"`
	MuxVersion int  `json:"muxVersion,omitempty"`

	// Paired names the form fields that came from the link. Changing one of
	// them breaks the tunnel unless the other end is changed to match, which is
	// what the panel warns about at the moment it is edited.
	Paired []string `json:"paired"`

	// Note is anything the operator should read once, in plain words — a
	// setting the link could not carry across, rather than an error.
	Note string `json:"note,omitempty"`
}

// MirrorForPeer turns a link into the other side's form.
//
// This is the whole inversion, and it is here rather than in the browser
// because it is exactly the part that is easy to get subtly wrong: the
// addresses swap, the tunnel addresses swap, and — the one nobody expects — the
// forged sources do not copy across but become what the far end EXPECTS. A
// second implementation of this in JavaScript would drift from this one, and
// the symptom of the drift would be a tunnel that connects and carries nothing.
func MirrorForPeer(l ShareLink) PeerForm {
	f := PeerForm{
		Kind:       l.Kind,
		Side:       l.PeerSide(),
		Name:       peerName(l),
		Token:      l.Tok,
		TunnelPort: l.Port,
		Preset:     l.Preset,
		MSS:        l.MSS,
		MTU:        l.MTU,
		Paths:      l.Paths,
		FEC:        l.FECData > 0 && l.FECParity > 0,
		FECData:    l.FECData,
		FECParity:  l.FECParity,
		Stealth:    l.Stealth,
		GREKey:     l.GREKey,
		MSSClamp:   l.L3MSS,
		AutoMTU:    l.AutoMTU,
	}
	// Every one of these is a setting the two ends must agree on. The list is
	// what the panel marks, so it is built from what was actually filled rather
	// than written out twice.
	paired := []string{"token", "tunnelPort"}

	if l.Kind == "reverse" {
		f.Transport = l.Tr
		paired = append(paired, "transport")
		f.SimpleAuth, f.MuxVersion = l.SimpleAuth, l.MuxVer
		if l.SimpleAuth {
			paired = append(paired, "simpleAuth")
		}
		if l.MuxVer > 0 {
			paired = append(paired, "muxVersion")
		}
		// The kharej side dials Iran; the ports it forwards are Iran's business
		// and it is not asked for them.
		if f.Side == "kharej" {
			f.ServerAddr = l.Host
			paired = append(paired, "serverAddr")
		} else {
			f.Ports = l.Ports
			f.AcceptUDP = l.AcceptUDP
		}
	} else {
		f.Carrier = l.Tr
		if l.SNI != "" {
			f.SNIDomain = l.SNI
			paired = append(paired, "sniDomain")
		}
		f.Encap = l.Encap
		paired = append(paired, "carrier")
		if l.Encap != "" {
			paired = append(paired, "encap")
		}
		// The private network's two addresses swap: the producer's local is the
		// receiver's peer.
		f.LocalIP, f.PeerIP = l.PeerIP, hostOf(l.LocalIP)
		if f.LocalIP != "" {
			paired = append(paired, "localIp", "peerIp")
		}
		// Iran dials kharej on a direct tunnel, so only the Iran side is asked
		// for an address to reach.
		if f.Side == "iran" {
			f.ServerAddr = l.Host
			paired = append(paired, "serverAddr")
			f.Ports = l.Ports
			f.AcceptUDP = l.AcceptUDP
		}
	}

	if l.Paths > 1 {
		paired = append(paired, "paths")
	}
	if f.FEC {
		paired = append(paired, "fec")
	}
	if l.MSS > 0 {
		paired = append(paired, "mss")
	}
	if l.GREKey != 0 {
		paired = append(paired, "greKey")
	}
	if l.L3MSS != 0 {
		paired = append(paired, "mssClamp")
	}
	if l.MTU > 0 && l.Kind == "direct" {
		paired = append(paired, "mtu")
	}

	// The carrier decides this, not the tuning. Keying it on a profile or a
	// forged-source list meant a spoof tunnel left on its defaults carried none
	// of this across — including the producer's real address, which the
	// listening side cannot work out for itself and refuses to start without.
	// So a spoof tunnel built from the panel could never have its far end made.
	if l.Tr == "spoof" || l.Profile != "" || l.Uplink != "" || l.Downlink != "" || l.SrcIPs != "" {
		sp := &SpoofTune{
			Profile:   l.Profile,
			Uplink:    l.Uplink,
			Downlink:  l.Downlink,
			ICMPReply: l.ICMPReply,
		}
		paired = append(paired, "spoof.profile")
		if l.Uplink != "" || l.Downlink != "" {
			paired = append(paired, "spoof.uplink", "spoof.downlink")
		}
		if l.Stealth {
			paired = append(paired, "stealth")
		}
		// The producer's forged source becomes what this side expects from it —
		// but only when there is exactly one. A producer rotating a pool has no
		// single source to pin, and pinning one of several would drop every
		// packet sent from the others.
		if one, ok := singleSource(l.SrcIPs); ok {
			sp.PeerSrcIP = one
			paired = append(paired, "spoof.peerSrcIP")
		} else if l.SrcIPs != "" {
			f.Note = "The other end rotates several forged sources, so this side cannot pin one — " +
				"leave its expected source empty and let the encryption sort the traffic out."
		}
		f.Spoof = sp
		// The listening side of a forged-source carrier cannot learn where its
		// peer is: every packet it receives carries a forged source. The link
		// carries the producer's real address precisely so this can be filled.
		if l.Host != "" {
			f.SpoofPeerIP = l.Host
			paired = append(paired, "spoofPeerIp")
		}
	}

	f.Paired = paired
	return f
}

// peerName suggests a name for the other end, derived from the one the producer
// chose so a pair is recognisable in a list. It is only a suggestion: the name
// is local and nothing breaks if the operator picks another.
func peerName(l ShareLink) string {
	if l.Name == "" {
		return ""
	}
	base := strings.TrimSuffix(strings.TrimSuffix(l.Name, "-iran"), "-kharej")
	return base + "-" + l.PeerSide()
}

// hostOf drops a prefix from a tunnel address: the producer's "10.20.0.1/30"
// is the receiver's peer "10.20.0.1".
func hostOf(addr string) string {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		return addr[:i]
	}
	return addr
}

// singleSource reports the one forged source in a list, when there is exactly
// one to report.
func singleSource(list string) (string, bool) {
	var seen []string
	for _, p := range strings.Split(list, ",") {
		if p = strings.TrimSpace(p); p != "" {
			seen = append(seen, p)
		}
	}
	if len(seen) == 1 {
		return seen[0], true
	}
	return "", false
}

// ShareLinkFor builds the link for a tunnel that already exists, so the side
// that was set up first can hand its settings over without being asked for them
// again.
//
// host is this machine's own address as the other end will reach it — the panel
// knows it and the config does not, because a listening tunnel binds 0.0.0.0
// and a dialling one records where it is going rather than where it is.
func ShareLinkFor(name, host string) (string, error) {
	cfg, err := LoadTunnelConfig(name)
	if err != nil {
		return "", err
	}
	return shareLinkOf(name, host, cfg)
}

// shareLinkOf builds the link from a config in hand, which is how the direct
// wizard shows it in its summary before the tunnel is written.
func shareLinkOf(name, host string, cfg config.Config) (string, error) {
	l := ShareLink{Name: name, Host: strings.TrimSpace(host)}

	switch {
	case cfg.L3.Enabled():
		l.Kind = "direct"
		l.From = "kharej"
		if !strings.EqualFold(strings.TrimSpace(cfg.L3.Mode), "listen") {
			l.From = "iran" // the dialling side of a direct tunnel is Iran
		}
		l.Tok = cfg.L3.Token
		l.Tr = orDefault(cfg.L3.Carrier, "udp")
		l.SNI = cfg.L3.SNIDomain
		l.Encap = "gre"
		l.Port = addrPort(cfg.L3.Addr)
		l.Preset = cfg.L3.Preset
		l.Ports = strings.Join(cfg.L3.Ports, ", ")
		l.AcceptUDP = cfg.L3.AcceptUDP
		l.MTU = cfg.L3.MTU
		l.LocalIP, l.PeerIP = cfg.L3.LocalIP, cfg.L3.PeerIP
		l.FECData, l.FECParity = cfg.L3.FECData, cfg.L3.FECParity
		l.Paths = cfg.L3.Paths
		l.GREKey, l.L3MSS, l.AutoMTU = cfg.L3.GREKey, cfg.L3.MSSClamp, cfg.L3.AutoMTU
		sc := cfg.L3.SpoofConfig
		l.Profile, l.Uplink, l.Downlink = sc.SpoofProfile, sc.SpoofUplink, sc.SpoofDownlink
		l.SrcIPs = strings.Join(nonEmpty(append([]string{sc.SpoofSrcIP}, sc.SpoofSrcPool...)), ", ")
		l.Stealth = spoofStealthOn(sc)
		l.ICMPReply = sc.SpoofICMPReply

	case cfg.Server.BindAddr != "":
		l.Kind, l.From = "reverse", "iran" // the reverse server is the Iran side
		l.Tok = cfg.Server.Token
		l.Tr = string(cfg.Server.Transport)
		l.Port = addrPort(cfg.Server.BindAddr)
		l.Preset = cfg.Server.Preset
		l.Ports = strings.Join(cfg.Server.Ports, ", ")
		l.AcceptUDP = cfg.Server.ForwardsUDP()
		l.MSS = cfg.Server.MSS
		l.SimpleAuth = cfg.Server.SimpleAuth
		l.MuxVer = cfg.Server.MuxVersion
		if cfg.Server.Transport == "kcp" {
			l.FECData, l.FECParity = cfg.Server.DataShards, cfg.Server.ParityShards
		}

	case cfg.Client.RemoteAddr != "":
		l.Kind, l.From = "reverse", "kharej"
		l.Tok = cfg.Client.Token
		l.Tr = string(cfg.Client.Transport)
		l.Port = addrPort(cfg.Client.RemoteAddr)
		l.Preset = cfg.Client.Preset
		l.MSS = cfg.Client.MSS

	default:
		return "", fmt.Errorf("%q is not a tunnel this version can hand over", name)
	}

	if l.Tok == "" {
		return "", fmt.Errorf("%q has no token to hand over", name)
	}
	return l.Encode()
}

// nonEmpty drops blanks and duplicates from a list of addresses, keeping order.
// The forged source is normally also the first member of the pool, and listing
// it twice would make a single source look like a rotation.
func nonEmpty(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// firstUncarryableField reports the first string in a share link that JSON
// cannot carry unchanged, named the way an operator would name it.
//
// It walks the struct rather than taking a list, so a field added above is
// covered the moment it exists. The name comes from the json tag, which is
// short — "t", "h" — so the map below gives the ones an operator sees a word
// instead; anything not in it falls back to the Go field name, which is at
// least something they can find.
func firstUncarryableField(l ShareLink) (string, bool) {
	spoken := map[string]string{
		"Tok": "token", "Tr": "transport", "Host": "address", "Name": "name",
		"Port": "port", "Encap": "encapsulation", "SNI": "SNI", "Kind": "tunnel kind",
		"From": "side", "Preset": "preset", "Profile": "packet profile",
		"LocalIP": "local address", "PeerIP": "peer address",
		"Uplink": "uplink", "Downlink": "downlink", "SrcIPs": "source addresses",
	}
	v := reflect.ValueOf(l)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			if !utf8.ValidString(f.String()) {
				return fieldName(t.Field(i).Name, spoken), false
			}
		case reflect.Slice:
			if f.Type().Elem().Kind() != reflect.String {
				continue
			}
			for j := 0; j < f.Len(); j++ {
				if !utf8.ValidString(f.Index(j).String()) {
					return fieldName(t.Field(i).Name, spoken), false
				}
			}
		}
	}
	return "", true
}

func fieldName(goName string, spoken map[string]string) string {
	if s, ok := spoken[goName]; ok {
		return s
	}
	return goName
}

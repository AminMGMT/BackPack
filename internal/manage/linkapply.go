package manage

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/optimize"
)

// Building the kharej end from a setup link without a single question.
//
// The Iran wizard prints two things under its summary: the link, for a kharej
// that already runs Backpack, and one command that installs Backpack and hands
// it the link, for one that does not. Both end here. The kharej wizard's Setup
// Link answer builds the same tunnel, but asks this side's own questions — a
// CDN edge, a proxy — which a command pasted into a fresh server has nobody to
// ask, so here they keep their defaults.

// LinkApplyOptions are what `backpack link apply` can be told beyond the link.
type LinkApplyOptions struct {
	// Name replaces the name the link suggests.
	Name string
	// Host is the Iran server's address, for a link made where it could not
	// be found, or to dial another one.
	Host string
}

// LinkApplied is what was built.
type LinkApplied struct {
	Name, Service  string
	Kind           string // "reverse" or "direct"
	Transport      string
	Dials          string // reverse: the Iran address this side dials
	Backups        []string
	Active         bool
	RestartHours   int
	RestartMinute  int
	ScheduleFailed string // why the restart schedule could not be set, if it could not
	// Updated is a link applied over the tunnel it made before: the Iran side
	// was edited, and this side was brought back into step with it.
	Updated bool
}

// ErrLinkNeedsHost is a link that does not say where the Iran server is.
var ErrLinkNeedsHost = errors.New("the setup link does not say where the Iran server is — " +
	"give its address with --host (backpack link apply --host 1.2.3.4 'backpack://…')")

// linkChars is what a setup link is made of after its scheme.
var linkChars = regexp.MustCompile(`^[A-Za-z0-9_\-=.]+`)

// FindSetupLink returns the setup link inside whatever was pasted — a Telegram
// message around it, quotes, a line break a chat app put in the middle of it —
// or text itself when there is none to find.
//
// Pieces after a break are joined on only while the link does not decode yet,
// so the word after a whole link is never taken for the rest of it.
func FindSetupLink(text string) string {
	i := strings.Index(text, "backpack://")
	if i < 0 {
		return strings.TrimSpace(text)
	}
	var pieces []string
	for _, f := range strings.Fields(text[i:]) {
		p := linkChars.FindString(strings.TrimPrefix(f, "backpack://"))
		if len(pieces) == 0 {
			p = "backpack://" + p
		}
		if p == "" || p == "backpack://" {
			break
		}
		pieces = append(pieces, strings.TrimRight(p, "."))
		if linkChars.FindString(f) != f && len(pieces) > 1 {
			break // this piece had something after it: the link ends here
		}
	}
	whole := ""
	for _, p := range pieces {
		whole += p
		if _, err := DecodeShareLink(whole); err == nil {
			return whole
		}
	}
	if len(pieces) > 0 {
		return pieces[0]
	}
	return strings.TrimSpace(text)
}

// ApplySetupLink builds and starts the tunnel a setup link describes, on the
// side it was made for.
func ApplySetupLink(raw string, o LinkApplyOptions) (LinkApplied, error) {
	link, err := DecodeShareLink(FindSetupLink(raw))
	if err != nil {
		return LinkApplied{}, err
	}
	if !strings.EqualFold(link.From, "iran") {
		return LinkApplied{}, fmt.Errorf("this link was made on a kharej server and is for the Iran side: " +
			"paste it there, under sudo backpack → Setup from a link")
	}
	if existing := tunnelWithToken(link.Tok); existing != "" {
		// The same tunnel, set up again. That is what happens after the Iran
		// side is edited — its transport, its port — and the kharej has to
		// follow: the link now describes the tunnel as it is, and applying it
		// brings this end into step. A second tunnel with the same token would
		// take the connection from the first, so it is never a new one.
		if link.Kind == "reverse" {
			return updateReverseFromLink(link, o, existing)
		}
		return LinkApplied{}, fmt.Errorf("this link has already been set up here, as tunnel %q — "+
			"a second tunnel with the same token would take the connection from the first; "+
			"delete that one first to set it up again", existing)
	}

	var out LinkApplied
	switch link.Kind {
	case "reverse":
		out, err = applyReverseLink(link, o)
	case "direct":
		out, err = applyDirectLink(link, o)
	default:
		return LinkApplied{}, fmt.Errorf("a %q setup link is not one this version can apply", link.Kind)
	}
	if err != nil {
		return out, err
	}

	if link.RestartHours > 0 {
		out.RestartHours, out.RestartMinute = EffectiveRestartHours(link.RestartHours), link.RestartMinute
		if err := SetScheduledRestart(out.Name, link.RestartHours, link.RestartMinute); err != nil {
			out.ScheduleFailed = err.Error()
		}
	}
	return out, nil
}

// kharejFromLink is the reverse kharej a link describes, without writing it.
// Split out so what a link builds can be run in a test without a machine to
// install it on.
func kharejFromLink(link ShareLink, o LinkApplyOptions) (TunnelSpec, error) {
	host := strings.Trim(strings.TrimSpace(o.Host), "[]")
	if host == "" {
		host = strings.Trim(strings.TrimSpace(link.Host), "[]")
	}
	if host == "" {
		return TunnelSpec{}, ErrLinkNeedsHost
	}
	s := reverseClientFromLink(link, host)
	if o.Name != "" {
		s.Name = o.Name
	}
	if !validName(s.Name) {
		return TunnelSpec{}, fmt.Errorf("%q is not a usable tunnel name — letters, digits, dots and dashes, up to 40", s.Name)
	}
	s.Name = freeName(s.Name)
	return s, nil
}

// updateReverseFromLink rewrites the kharej end named existing from the link,
// keeping its name, and restarts it — rolled back to what it was if the new
// settings will not start, as every edit is.
func updateReverseFromLink(link ShareLink, o LinkApplyOptions, existing string) (LinkApplied, error) {
	host := strings.Trim(strings.TrimSpace(o.Host), "[]")
	if host == "" {
		host = strings.Trim(strings.TrimSpace(link.Host), "[]")
	}
	if host == "" {
		return LinkApplied{}, ErrLinkNeedsHost
	}
	paired := reverseClientFromLink(link, host)
	s, err := LoadSpec(existing)
	if err != nil {
		return LinkApplied{}, err
	}
	if s.Role != "client" {
		return LinkApplied{}, fmt.Errorf("%q is not a client tunnel", existing)
	}
	// Only the peer-owned wire settings come from the link. Local routing,
	// retry timing, monitoring and capacity tuning belong to this machine.
	s.Transport, s.RemoteAddr, s.Token = paired.Transport, paired.RemoteAddr, paired.Token
	s.FallbackAddrs, s.FallbackTransports, s.FallbackDwell = paired.FallbackAddrs, paired.FallbackTransports, paired.FallbackDwell
	s.MSS, s.SimpleAuth = paired.MSS, paired.SimpleAuth
	s.MuxVersion = paired.MuxVersion
	s.KCPDataShards, s.KCPParityShards = paired.KCPDataShards, paired.KCPParityShards
	clearManagedTransport(&s)
	s.NaiveClient, s.XrayClient = paired.NaiveClient, paired.XrayClient
	if managedTransport(link.Tr) {
		managedClientFromLink(&s, link, host)
		s.Proxy, s.LocalAddr, s.Interface, s.EdgeIP = "", "", "", ""
		s.SOMark, s.LoadBalance = 0, false
	}
	if why := reverseLinkClash(s); why != "" {
		return LinkApplied{}, errors.New(why)
	}
	if err := prepareManagedLink(s, link); err != nil {
		return LinkApplied{}, err
	}
	if err := applySpec(s); err != nil {
		return LinkApplied{}, fmt.Errorf("could not bring %s into step with the Iran side: %w", existing, err)
	}
	service := app.ServiceName(existing)
	return LinkApplied{
		Name: existing, Service: service, Kind: "reverse", Transport: selectedTransport(s),
		Dials: managedEndpoint(s), Backups: s.FallbackAddrs, Active: IsActive(service), Updated: true,
	}, nil
}

func applyReverseLink(link ShareLink, o LinkApplyOptions) (LinkApplied, error) {
	s, err := kharejFromLink(link, o)
	if err != nil {
		return LinkApplied{}, err
	}
	if why := reverseLinkClash(s); why != "" {
		return LinkApplied{}, errors.New(why)
	}
	if err := prepareManagedLink(s, link); err != nil {
		return LinkApplied{}, err
	}
	optimize.ApplyQuiet(ReservedPorts())
	service, err := s.Save()
	if err != nil {
		return LinkApplied{}, err
	}
	return LinkApplied{
		Name: s.Name, Service: service, Kind: "reverse", Transport: selectedTransport(s),
		Dials: managedEndpoint(s), Backups: s.FallbackAddrs, Active: IsActive(service),
	}, nil
}

func reverseLinkClash(s TunnelSpec) string {
	if managedTransport(selectedTransport(s)) {
		return managedEndpointClash(s)
	}
	return portClash(s.Role, s.RemoteAddr, s.Name)
}

func applyDirectLink(link ShareLink, o LinkApplyOptions) (LinkApplied, error) {
	form := MirrorForPeer(link)
	if form.Side != "kharej" {
		return LinkApplied{}, fmt.Errorf("this direct link is for the %s side", form.Side)
	}
	if o.Name != "" {
		form.Name = o.Name
	}
	if !validName(form.Name) {
		return LinkApplied{}, fmt.Errorf("%q is not a usable tunnel name — letters, digits, dots and dashes, up to 40", form.Name)
	}
	form.Name = freeName(form.Name)
	if link.Tr == "spoof" && net.ParseIP(form.SpoofPeerIP) == nil {
		// The one carrier whose listening end must be told where the Iran
		// server really is: every packet it receives carries a forged source.
		ip := net.ParseIP(strings.Trim(strings.TrimSpace(o.Host), "[]"))
		if ip == nil || ip.To4() == nil {
			return LinkApplied{}, fmt.Errorf("a spoof tunnel's kharej needs the Iran server's real IPv4 " +
				"address, and the link does not carry it — give it with --host")
		}
		form.SpoofPeerIP = ip.String()
	}
	service, active, err := CreateDirectTunnel(form.ToNewDirectTunnel())
	if err != nil {
		return LinkApplied{}, err
	}
	return LinkApplied{Name: form.Name, Service: service, Kind: "direct", Transport: link.Tr, Active: active}, nil
}

// freeName is name, or name-2, name-3 … when that one is taken: a command has
// nobody to ask for another.
func freeName(name string) string {
	if !fileExists(app.ConfigPath(name)) {
		return name
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s-%d", name, i)
		if !fileExists(app.ConfigPath(n)) {
			return n
		}
	}
}

// tunnelWithToken names the tunnel here that already holds token, if one does.
func tunnelWithToken(token string) string {
	if token == "" {
		return ""
	}
	for _, t := range List() {
		cfg, err := LoadTunnelConfig(t.Name)
		if err != nil {
			continue
		}
		for _, have := range []string{cfg.Client.Token, cfg.Server.Token, cfg.L3.Token, cfg.Direct.Token} {
			if have == token {
				return t.Name
			}
		}
	}
	return ""
}

// AwaitLinkedTunnel waits up to within for a tunnel just applied to reach the
// far end, and reports what it saw: the answer a person running the command
// is waiting for, rather than only "created".
func AwaitLinkedTunnel(name string, within time.Duration) (connected bool, detail string) {
	deadline := time.Now().Add(within)
	for {
		t, ok := Find(name)
		if !ok {
			return false, "the tunnel is not there"
		}
		h := TunnelHealth(t)
		if h.Connected {
			return true, h.Detail
		}
		if time.Now().After(deadline) {
			return false, h.Detail
		}
		time.Sleep(time.Second)
	}
}

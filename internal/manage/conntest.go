package manage

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/spooftest"
	"github.com/backpack/backpack/internal/tunnel/naive"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

// Connection Test: which transports survive the path between two servers.
//
// Reported from the field: a reverse tunnel between one Iran server and one
// kharej came up and fell over every seventeen seconds. Nothing in either
// configuration was wrong. The path let every new flow carry three to five
// packets and then dropped it — TCP, UDP, TLS and plain HTTP alike — and the
// only way to find that out was a round of hand-written Python on both
// machines. Which transport, if any, survives a given pair of servers is a
// property of the route, not of the program, and the only honest way to know
// is to run each one across it.
//
// So this runs them. The Iran side starts a real engine for every transport,
// on free ports, with throwaway configs, and prints a link. The kharej, given
// the link, builds its own end of each one the way `link apply` builds a real
// tunnel and starts them too. Then the Iran side pushes traffic through every
// tunnel — an echo a second for a minute, and a bulk transfer at the end — and
// reports which carried it all, which dropped, and which never came up. Both
// sides print the table, and nothing is left behind: no unit, no config under
// /etc/backpack, no rule once the engines have stopped.
//
// The Iran side is the judge because it is where users arrive: every tunnel,
// reverse or direct, is used from there. The kharej hears the verdict through
// a small coordinator on the Iran server — one short exchange per question, so
// a path that cuts flows after a few packets still carries it — which is also
// how the Iran side learns the kharej's address for the direct tunnels, whose
// Iran end dials out.

// connTestKind marks a test link. A setup link is "reverse" or "direct".
const connTestKind = "test"

// Which transports a test covers: every one the wizards offer. Spoof joins
// the direct ones when the operator names a source to forge (see
// ConnTestOptions.SpoofSrc); pck, on either side, needs root on both servers.
var (
	connTestReverse = []string{"tcp", "tcpmux", "stealth", "pck", "ws", "wss", "wsmux", "wssmux", "kcp", "quic", "udp", "naive", "xhttp", "reality"}
	connTestDirect  = []string{"udp", "quic", "pck", "xdi", "sni"}
)

// connTestNeedsRoot is a reverse transport that builds its own packets.
func connTestNeedsRoot(tr string) bool { return tr == "pck" }

// Variables rather than constants so a test can run the whole thing in
// seconds.
var (
	// connTestJoinWait is how long the Iran side waits for the kharej.
	connTestJoinWait = 15 * time.Minute
	// connTestConnectWait is how long a tunnel has to carry its first echo.
	connTestConnectWait = 60 * time.Second
	// connTestSoak is how many one-second echoes a tunnel must carry. Long
	// enough for a path that cuts flows after a few seconds to show it.
	connTestSoak = 60
	// connTestSlack is how long past its schedule the kharej keeps waiting
	// for the verdict before it stops on its own.
	connTestSlack    = 5 * time.Minute
	connTestStopWait = 8 * time.Second
)

// connTestBulk is the transfer that measures speed.
const connTestBulk = 1 << 20

// connTestBinary is the engine to run; a variable so a test can point it at a
// built binary rather than at the test itself.
var connTestBinary = os.Executable

// Use the same pinned helper binaries as the setup wizard. Absence is a
// skipped test, rather than evidence that the network blocked a protocol.
var connTestHelperBinary = func(tool string) (string, error) {
	path := managedHelperPath(tool)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("%s helper is not installed at %s; reinstall with BP_HELPERS=naive,xray", tool, path)
	}
	return path, nil
}

// ConnTestLink is everything the kharej needs to build its end of every
// tunnel under test, and to find the Iran side's coordinator.
type ConnTestLink struct {
	V     int    `json:"v"`
	Kind  string `json:"k"` // always connTestKind
	ID    string `json:"id"`
	Host  string `json:"h"`  // the Iran server's address
	Coord int    `json:"c"`  // the coordinator's port, TCP and UDP
	Tok   string `json:"t"`  // the coordinator's secret
	TCP   int    `json:"te"` // kharej: the TCP echo reverse tunnels forward to
	UDP   int    `json:"ue"` // kharej: the UDP echo the udp transport forwards to
	L3    int    `json:"le"` // kharej: the TCP echo on each direct tunnel's address
	Until int64  `json:"u"`  // unix seconds: the kharej stops by then whatever happens
	// Preset is the performance preset every tunnel under test was built
	// with, for the kharej's report; each case carries it too.
	Preset string `json:"pr,omitempty"`
	// The spoofing check: the forged source, and the UDP port each side's
	// receiver counts probes on. Empty SpoofSrc is no check.
	SpoofSrc string `json:"ss,omitempty"`
	SpoofK   int    `json:"sk,omitempty"` // the kharej's receiver
	SpoofI   int    `json:"si,omitempty"` // the Iran server's receiver
	SpoofN   int    `json:"sn,omitempty"` // probes each way
	// Cases are the setup links of the tunnels under test, exactly as the
	// wizard would print them, so the kharej builds each one by the path a
	// real tunnel takes.
	Cases []ShareLink `json:"x"`
}

// The link the operator copies is only where the Iran side's coordinator is
// and the test's secret: backpack://t. and some twenty-seven characters. The
// rest — every tunnel's settings — the kharej asks the coordinator for (see
// ctConfig), and every tunnel's token is derived from the secret rather than
// carried, which is what keeps that answer inside one packet: a path that cuts
// a flow after a few packets still carries it.
//
// The body is base64url of: version (1), port (2, big endian), secret (12),
// then the host — 4 bytes for an IPv4 address, or the name as text.
const (
	connTestScheme   = shareScheme + "t."
	connTestSecret   = 12
	connTestLinkVer  = 1
	connTestFixedLen = 1 + 2 + connTestSecret
)

// Short is the link to copy.
func (l ConnTestLink) Short() string {
	secret, _ := base64.RawURLEncoding.DecodeString(l.Tok)
	b := []byte{connTestLinkVer, byte(l.Coord >> 8), byte(l.Coord)}
	b = append(b, secret...)
	if ip := net.ParseIP(l.Host).To4(); ip != nil {
		b = append(b, ip...)
	} else {
		b = append(b, l.Host...)
	}
	return connTestScheme + base64.RawURLEncoding.EncodeToString(b)
}

// connTestAddr is what a copied link says: where to ask, and the secret.
type connTestAddr struct {
	Host  string
	Coord int
	Tok   string
}

// parseConnTestLink reads a copied test link, wherever it sits in what was
// pasted.
func parseConnTestLink(s string) (connTestAddr, error) {
	s = FindSetupLink(s)
	if !strings.HasPrefix(s, connTestScheme) {
		return connTestAddr{}, fmt.Errorf("that is not a Backpack connection-test link — it begins with %s", connTestScheme)
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, connTestScheme))
	if err != nil || len(b) <= connTestFixedLen || b[0] != connTestLinkVer {
		return connTestAddr{}, fmt.Errorf("the test link is damaged or cut short — copy all of it again")
	}
	a := connTestAddr{
		Coord: int(b[1])<<8 | int(b[2]),
		Tok:   base64.RawURLEncoding.EncodeToString(b[3:connTestFixedLen]),
	}
	host := b[connTestFixedLen:]
	if len(host) == 4 {
		a.Host = net.IP(host).String()
	} else {
		a.Host = string(host)
	}
	if a.Coord == 0 || a.Host == "" {
		return connTestAddr{}, fmt.Errorf("the test link is damaged or cut short — copy all of it again")
	}
	return a, nil
}

// IsConnTestLink reports whether s holds a connection-test link rather than a
// setup link.
func IsConnTestLink(s string) bool {
	_, err := parseConnTestLink(s)
	return err == nil
}

// ctNewSecret is a test's secret, as the link and the coordinator carry it.
func ctNewSecret() string {
	b := make([]byte, connTestSecret)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ctCaseToken is one tunnel's token, derived from the test's secret so that
// neither the link nor the coordinator's answer has to carry it.
func ctCaseToken(secret, kind, tr string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("backpack-conntest/" + kind + "/" + tr))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// ctConfig is the coordinator's answer to "config": the whole test, less the
// tokens the kharej derives for itself.
func ctConfig(l ConnTestLink) string {
	l.V, l.Kind = 1, connTestKind
	l.Cases = append([]ShareLink(nil), l.Cases...)
	for i := range l.Cases {
		l.Cases[i].Tok = ""
	}
	raw, _ := json.Marshal(l)
	return gzipB64(raw)
}

// fetchConnTestLink asks the Iran side's coordinator for the test a copied
// link names, and fills in each tunnel's token.
func fetchConnTestLink(ctx context.Context, a connTestAddr) (ConnTestLink, error) {
	var out ConnTestLink
	for {
		reply, err := ctAskContext(ctx, a.Host, a.Coord, "config "+a.Tok)
		if err == nil && strings.HasPrefix(reply, "config ") {
			raw, err := unGzipB64(strings.TrimPrefix(reply, "config "))
			if err != nil || json.Unmarshal(raw, &out) != nil || out.Kind != connTestKind {
				return out, fmt.Errorf("the test settings from the Iran server arrived damaged — try again")
			}
			out.Host, out.Coord, out.Tok = a.Host, a.Coord, a.Tok
			for i := range out.Cases {
				out.Cases[i].Tok = ctCaseToken(a.Tok, out.Cases[i].Kind, out.Cases[i].Tr)
			}
			return out, nil
		}
		if ctx.Err() != nil {
			return out, fmt.Errorf("the Iran server's test coordinator (%s port %d, TCP and UDP) never answered — "+
				"nothing at all gets through from here to there, or the test there has ended", a.Host, a.Coord)
		}
		ctSleep(ctx, 3*time.Second)
	}
}

func gzipB64(raw []byte) string {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	return base64.RawURLEncoding.EncodeToString(buf.Bytes())
}

func unGzipB64(s string) ([]byte, error) {
	gz, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(io.LimitReader(zr, 256<<10))
}

// ConnTestResult is one tunnel's verdict.
type ConnTestResult struct {
	Kind      string  `json:"k"` // "reverse" or "direct"
	Transport string  `json:"t"`
	Status    string  `json:"s"` // ok, unstable, down, skipped
	Detail    string  `json:"d,omitempty"`
	Connect   float64 `json:"c,omitempty"` // seconds to the first echo
	OK        int     `json:"o"`           // echoes that came back
	Tried     int     `json:"n"`           // echoes sent
	RTTms     int     `json:"r,omitempty"`
	Mbps      float64 `json:"m,omitempty"`
	// Total is how many echoes the test sends each tunnel.
	Total int `json:"z,omitempty"`
}

// Result statuses.
const (
	ctTesting  = "testing" // still running
	ctOK       = "ok"
	ctUnstable = "unstable"
	ctDown     = "down"
	ctSkipped  = "skipped"
)

// connTestCase is one tunnel under test, as the Iran side holds it.
type connTestCase struct {
	kind, tr string
	name     string
	udp      bool   // the forwarded traffic is UDP (the udp transport)
	entry    int    // reverse: where on this server the tunnel's users arrive
	peerIP   string // direct: the kharej's tunnel address, where its echo listens
	l3       l3Spec // direct: this side's spec, finished once the kharej's address is known
	link     ShareLink
	skip     string
	rtts     []time.Duration // every echo's round trip, for the recommendation
}

// ConnTestIran is a test the Iran side is running.
type ConnTestIran struct {
	dir           string
	link          ConnTestLink
	cases         []*connTestCase
	coord         *ctCoordinator
	mu            sync.Mutex
	engines       []*ctEngine
	root          bool
	best          ConnTestBest
	realityTarget string
	realityLog    string
}

// ConnTestOptions are what the Iran operator answers.
type ConnTestOptions struct {
	Context   context.Context // optional cancellation, including startup
	Host      string          // this server's address, as the kharej dials it
	SNIDomain string          // what the sni carrier announces
	// RealityTarget is an explicit reachable TLS 1.3/H2 hostname:port used
	// for the REALITY cover handshake. Empty selects a verified endpoint automatically.
	RealityTarget string
	// Direct includes the direct tunnels. They need root on both servers.
	Direct bool
	// Preset is balance, turbo or aggressive; empty is turbo, the wizards'
	// default.
	Preset string
	// SpoofSrc is the forged source the IP Spoofing Tester sends from, both
	// ways between the two servers (see ctSpoofProbe). Empty leaves it out.
	SpoofSrc string
}

// ConnTestSpoofSource is the forged source a Connection Test probes with.
const ConnTestSpoofSource = "10.10.10.10"

// The two directions the spoofing check runs, as its rows name them.
const (
	ctSpoofToKharej = "iran-kharej"
	ctSpoofToIran   = "kharej-iran"
)

// StartConnTestIran starts the Iran end of every tunnel under test and the
// coordinator, and returns the link to give the kharej.
func StartConnTestIran(o ConnTestOptions) (*ConnTestIran, string, error) {
	ctx := o.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	host := strings.Trim(strings.TrimSpace(o.Host), "[]")
	if host == "" {
		return nil, "", fmt.Errorf("this server's address is needed: it is what the kharej dials")
	}
	dir, err := os.MkdirTemp("", "backpack-conntest-")
	if err != nil {
		return nil, "", err
	}
	s := &ConnTestIran{dir: dir, root: os.Geteuid() == 0}
	fail := func(err error) (*ConnTestIran, string, error) {
		s.Close()
		return nil, "", err
	}

	preset := strings.ToLower(strings.TrimSpace(o.Preset))
	switch preset {
	case PresetBalance, PresetTurbo, PresetAggressive:
	default:
		preset = PresetTurbo
	}
	spoofSrc := strings.TrimSpace(o.SpoofSrc)
	if spoofSrc != "" {
		if ip := net.ParseIP(spoofSrc); ip == nil || ip.To4() == nil {
			return fail(fmt.Errorf("%q is not an IPv4 address to forge", spoofSrc))
		}
	}

	id := randomToken(8)
	used := map[int]bool{}
	s.link = ConnTestLink{
		ID: id, Host: host, Tok: ctNewSecret(), Preset: preset,
		TCP: ctPickPort(used, false), UDP: ctPickPort(used, false), L3: ctPickPort(used, false),
		Until: time.Now().Add(connTestJoinWait + connTestConnectWait + time.Duration(connTestSoak)*time.Second + connTestSlack).Unix(),
	}
	s.link.Coord = ctPickPort(used, true)
	if s.link.TCP == 0 || s.link.UDP == 0 || s.link.L3 == 0 || s.link.Coord == 0 {
		return fail(errors.New("no free ports for the connection test"))
	}
	if s.coord, err = startCTCoordinator(s.link.Coord, s.link.Tok); err != nil {
		return fail(fmt.Errorf("could not open the test coordinator on port %d: %w", s.link.Coord, err))
	}

	for _, tr := range connTestReverse {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		c := &connTestCase{kind: "reverse", tr: tr, name: "ct-" + id + "-" + tr, udp: tr == "udp"}
		if managedTransport(tr) {
			s.startManagedCase(c, used, o)
			s.cases = append(s.cases, c)
			continue
		}
		if connTestNeedsRoot(tr) && !s.root {
			c.skip = "needs root on both servers"
			s.cases = append(s.cases, c)
			continue
		}
		port := ctPickPort(used, true)
		c.entry = ctPickPort(used, true)
		if port == 0 || c.entry == 0 {
			return fail(fmt.Errorf("no free ports for the %s test", tr))
		}
		target := s.link.TCP
		if c.udp {
			target = s.link.UDP
		}
		spec := TunnelSpec{
			Role: "server", Transport: tr, Name: c.name,
			BindAddr: net.JoinHostPort("0.0.0.0", strconv.Itoa(port)),
			Ports:    []string{fmt.Sprintf("127.0.0.1:%d=127.0.0.1:%d", c.entry, target)},
			Token:    ctCaseToken(s.link.Tok, "reverse", tr),
		}
		ApplyPreset(&spec, preset)
		raw := pendingReverseLink(spec, host, linkExtras{})
		if c.link, err = DecodeShareLink(raw); err != nil {
			return fail(fmt.Errorf("building the %s test link: %w", tr, err))
		}
		c.link.Name = c.name
		e, err := startCTEngine(dir, c.name, ctQuiet(spec.Render()))
		if err != nil {
			return fail(err)
		}
		s.engines = append(s.engines, e)
		s.cases = append(s.cases, c)
	}

	if o.Direct {
		sni := strings.TrimSpace(o.SNIDomain)
		if sni == "" {
			sni = "www.speedtest.net"
		}
		for i, carrier := range connTestDirect {
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			c := &connTestCase{kind: "direct", tr: carrier, name: fmt.Sprintf("ct-%s-d%s", id, carrier)}
			if !s.root {
				c.skip = "needs root on both servers"
				s.cases = append(s.cases, c)
				continue
			}
			block := ctFreeL3Block(i)
			c.peerIP = block + "2"
			c.l3 = l3Spec{
				Name: c.name, Side: sideIran, Carrier: carrier, Encap: "gre",
				// The kharej's address is not known until it joins; the link
				// carries only the port.
				Addr:  net.JoinHostPort("0.0.0.0", strconv.Itoa(ctPickPort(used, true))),
				Token: ctCaseToken(s.link.Tok, "direct", carrier), Iface: fmt.Sprintf("bpt%d", i),
				LocalIP: block + "1/30", PeerIP: block + "2",
				// The wizard's defaults, the path MTU measured included: a
				// tunnel tested with other settings is not the one that
				// would be built.
				MTU: defaultL3MTU,
			}
			findL3Preset(preset).apply(&c.l3)
			if carrier == "sni" {
				c.l3.SNIDomain = sni
			}
			raw := pendingShareLink(c.l3, linkExtras{})
			if c.link, err = DecodeShareLink(raw); err != nil {
				return fail(fmt.Errorf("building the %s test link: %w", carrier, err))
			}
			c.link.Name = c.name
			s.cases = append(s.cases, c)
		}
	}
	// IP spoofing, checked the way Manage → IP Spoofing Tester checks it: a
	// receiver on one server counts the probes that arrive from the forged
	// source, the other server sends them. Once each way.
	if spoofSrc != "" {
		s.link.SpoofSrc, s.link.SpoofN = spoofSrc, connTestSoak
		s.link.SpoofK = ctPickPort(used, false)
		s.link.SpoofI = ctPickPort(used, true)
		for _, dir := range []string{ctSpoofToKharej, ctSpoofToIran} {
			c := &connTestCase{kind: "spoof", tr: dir}
			if !s.root {
				c.skip = "needs root on both servers"
			}
			s.cases = append(s.cases, c)
		}
	}

	for _, c := range s.cases {
		if c.skip == "" && c.kind != "spoof" {
			s.link.Cases = append(s.link.Cases, c.link)
		}
	}

	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	s.coord.setConfig(ctConfig(s.link))
	return s, s.link.Short(), nil
}

// ctCarrierSettings travels separately from the small configuration packet.
// Only public trust material is sent; credentials come from the test secret,
// and the certificate/private REALITY keys stay in the temporary Iran config.
type ctCarrierSettings struct {
	TargetPort int    `json:"p"`
	ServerName string `json:"s,omitempty"`
	CA         string `json:"c,omitempty"`
	PublicKey  string `json:"k,omitempty"`
	ShortID    string `json:"i,omitempty"`
}

func ctUUID(token string) string {
	return token[:8] + "-" + token[8:12] + "-" + token[12:16] + "-" + token[16:20] + "-" + token[20:]
}

// ctCertificate creates a short-lived, private test identity with explicit
// SANs. The peer verifies it with this leaf as its trust anchor, never insecure
// TLS. Files are owned by this test directory and removed with it.
func ctCertificate(dir, name, host string) (cert, key, ca string, err error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", "", err
	}
	leaf := &x509.Certificate{SerialNumber: serial, NotBefore: time.Now().Add(-time.Minute),
		NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"conntest.backpack.invalid"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(host); ip != nil {
		leaf.IPAddresses = []net.IP{ip}
	} else {
		leaf.DNSNames = append(leaf.DNSNames, host)
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &private.PublicKey, private)
	if err != nil {
		return "", "", "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return "", "", "", err
	}
	ca = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	cert, key = filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
	if err := os.WriteFile(cert, []byte(ca), 0600); err != nil {
		return "", "", "", err
	}
	if err := os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		return "", "", "", err
	}
	return cert, key, ca, nil
}

// Probe from Iran: DNS, certificate trust, TLS version and ALPN can differ by route.
func ctProbeRealityCover(ctx context.Context, target string, roots *x509.CertPool) error {
	d := tls.Dialer{NetDialer: &net.Dialer{}, Config: &tls.Config{
		MinVersion: tls.VersionTLS13, NextProtos: []string{"h2"}, RootCAs: roots,
		CurvePreferences: []tls.CurveID{tls.X25519},
	}}
	conn, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		return err
	}
	defer conn.Close()
	state := conn.(*tls.Conn).ConnectionState()
	if state.Version != tls.VersionTLS13 || state.NegotiatedProtocol != "h2" {
		return errors.New("cover endpoint does not support TLS 1.3 and HTTP/2")
	}
	return nil
}

// A normal TLS handshake can pass while the pinned REALITY parser rejects the
// cover's ServerHello or certificate records. Authenticate through the actual
// Chrome/Vision helper pair before choosing a cover, so that case never gets
// reported as a blocked Iran/Kharej route merely because its cover is unusable.
func ctProbeRealityTransport(ctx context.Context, target, binary string) error {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer echo.Close()
	stopEcho := context.AfterFunc(ctx, func() { echo.Close() })
	defer stopEcho()
	done := make(chan struct{})
	go func() {
		defer close(done)
		peer, err := echo.Accept()
		if err != nil {
			return
		}
		defer peer.Close()
		stop := context.AfterFunc(ctx, func() { peer.Close() })
		defer stop()
		_, _ = io.Copy(peer, peer)
	}()
	// Join the echo worker even when helper startup or authentication fails.
	defer func() { echo.Close(); <-done }()
	used := map[int]bool{}
	port := ctPickPort(used, false)
	if port == 0 {
		return errors.New("no free port for the REALITY cover probe")
	}
	private, public, err := managedRealityKey("")
	if err != nil {
		return err
	}
	token := ctCaseToken(ctNewSecret(), "cover", "reality")
	server := config.ServerConfig{Transport: config.TCP, BindAddr: echo.Addr().String(),
		Xray: config.XrayServerConfig{Binary: binary, Mode: "reality", Listen: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
			UUID: ctUUID(token), ServerName: host, PrivateKey: private, ShortID: token[:16], Target: target}}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	sh, err := naive.StartXrayServer(ctx, &server, logger)
	if err != nil {
		return fmt.Errorf("REALITY cover server: %w", err)
	}
	defer sh.Close()
	client := config.ClientConfig{Transport: config.TCP, RemoteAddr: server.BindAddr,
		Xray: config.XrayClientConfig{Binary: binary, Mode: "reality", Server: server.Xray.Listen,
			UUID: server.Xray.UUID, ServerName: host, PublicKey: public, ShortID: server.Xray.ShortID}}
	ch, err := naive.StartXrayClient(ctx, &client, logger)
	if err != nil {
		return fmt.Errorf("REALITY cover client: %w", err)
	}
	defer ch.Close()
	dialer, err := proxy.SOCKS5("tcp", strings.TrimPrefix(ch.ProxyURL(), "socks5://"), nil, &net.Dialer{})
	if err != nil {
		return err
	}
	conn, err := dialer.(proxy.ContextDialer).DialContext(ctx, "tcp", server.BindAddr)
	if err != nil {
		return fmt.Errorf("REALITY cover authentication: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	payload := []byte(token)
	if n, err := conn.Write(payload); err != nil {
		return err
	} else if n != len(payload) {
		return io.ErrShortWrite
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		return fmt.Errorf("REALITY cover did not carry authenticated data: %w", err)
	}
	if !bytes.Equal(got, payload) {
		return errors.New("REALITY cover probe returned corrupt data")
	}
	return ctx.Err()
}

var connTestCoverWait = 8 * time.Second

func ctFindRealityCover(ctx context.Context, targets []string, probe func(context.Context, string) error) (string, error) {
	var failures []string
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		attempt, cancel := context.WithTimeout(ctx, connTestCoverWait)
		err := probe(attempt, target)
		if err == nil {
			err = attempt.Err()
		}
		cancel()
		if err == nil && ctx.Err() == nil {
			return target, nil
		}
		failures = append(failures, target+": "+fmt.Sprint(err))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no usable REALITY cover from Iran (%s)", strings.Join(failures, "; "))
}

func (s *ConnTestIran) startManagedCase(c *connTestCase, used map[int]bool, o ConnTestOptions) {
	tool := "xray"
	if c.tr == "naive" {
		tool = "sing-box"
	}
	binary, err := connTestHelperBinary(tool)
	if err != nil {
		c.skip = err.Error()
		return
	}
	coverHost := ""
	coverTarget := strings.TrimSpace(o.RealityTarget)
	if c.tr == "reality" {
		if coverTarget == "" {
			ctx := o.Context
			if ctx == nil {
				ctx = context.Background()
			}
			// The temporary loopback helpers emit banners and generic deployment
			// warnings. Keep them out of the menu without discarding diagnostics.
			s.realityLog = filepath.Join(s.dir, "reality-cover.log")
			output, openErr := os.OpenFile(s.realityLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if openErr != nil {
				c.skip = "creating REALITY probe diagnostics: " + openErr.Error()
				return
			}
			defer output.Close()
			ctx = naive.WithHelperOutput(ctx, output)
			coverTarget, err = ctFindRealityCover(ctx, []string{"www.microsoft.com:443", "www.bing.com:443", "www.apple.com:443"},
				func(ctx context.Context, target string) error {
					fmt.Fprintf(output, "\nREALITY cover probe: %s\n", target)
					if err := ctProbeRealityCover(ctx, target, nil); err != nil {
						return err
					}
					return ctProbeRealityTransport(ctx, target, binary)
				})
			if err != nil {
				c.skip = err.Error()
				return
			}
		}
		coverHost, _, err = net.SplitHostPort(coverTarget)
		if err != nil || coverHost == "" || net.ParseIP(coverHost) != nil {
			c.skip = "set a REALITY cover endpoint (reachable TLS 1.3/H2 hostname:port) on Iran"
			return
		}
	}
	port, targetPort := ctPickPort(used, false), ctPickPort(used, false)
	c.entry = ctPickPort(used, false)
	if port == 0 || targetPort == 0 || c.entry == 0 {
		c.skip = "no free ports for the managed test"
		return
	}
	token := ctCaseToken(s.link.Tok, c.kind, c.tr)
	spec := TunnelSpec{Role: "server", Transport: "tcp", Name: c.name, Token: token,
		BindAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(targetPort)),
		Ports:    []string{fmt.Sprintf("127.0.0.1:%d=127.0.0.1:%d", c.entry, s.link.TCP)}}
	ApplyPreset(&spec, s.link.Preset)
	// Reuse the existing reverse link's paired engine tuning; helper settings
	// are exchanged only through the authenticated test coordinator.
	c.link, err = DecodeShareLink(pendingReverseLink(spec, s.link.Host, linkExtras{}))
	if err != nil {
		c.skip = err.Error()
		return
	}
	c.link.Tr, c.link.Port, c.link.Name = c.tr, strconv.Itoa(port), c.name
	settings := ctCarrierSettings{TargetPort: targetPort, ServerName: "conntest.backpack.invalid"}
	public := net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	var cert, key string
	if c.tr != "reality" {
		cert, key, settings.CA, err = ctCertificate(s.dir, c.name, s.link.Host)
		if err != nil {
			c.skip = "creating the test certificate: " + err.Error()
			return
		}
	}
	switch c.tr {
	case "naive":
		spec.NaiveServer = config.NaiveServerConfig{Binary: binary, Listen: public,
			Username: "backpack", Password: token, Certificate: cert, Key: key}
	case "xhttp":
		spec.XrayServer = config.XrayServerConfig{Binary: binary, Listen: public, Mode: c.tr,
			UUID: ctUUID(token), ServerName: settings.ServerName, Path: "/" + token,
			Certificate: cert, Key: key}
	case "reality":
		var private string
		private, settings.PublicKey, err = managedRealityKey("")
		if err != nil {
			c.skip = err.Error()
			return
		}
		settings.ServerName, settings.ShortID = coverHost, token[:16]
		spec.XrayServer = config.XrayServerConfig{Binary: binary, Listen: public, Mode: c.tr,
			UUID: ctUUID(token), ServerName: coverHost, PrivateKey: private, ShortID: settings.ShortID,
			Target: coverTarget}
	}
	if err := validateManagedSpec(spec); err != nil {
		c.skip = "invalid helper settings: " + err.Error()
		return
	}
	engine, err := startCTEngine(s.dir, c.name, ctQuiet(spec.Render()))
	if err != nil {
		c.skip = "could not start here: " + err.Error()
		return
	}
	s.engines = append(s.engines, engine)
	if c.tr == "reality" {
		s.realityTarget = coverTarget
	}
	body, _ := json.Marshal(settings)
	s.coord.mu.Lock()
	if s.coord.carriers == nil {
		s.coord.carriers = make(map[string]string)
	}
	s.coord.carriers[c.tr] = gzipB64(body)
	s.coord.mu.Unlock()
}

func ctManagedClient(ctx context.Context, link ConnTestLink, c ShareLink, dir string) (TunnelSpec, error) {
	tool := "xray"
	if c.Tr == "naive" {
		tool = "naive"
	}
	binary, err := connTestHelperBinary(tool)
	if err != nil {
		return TunnelSpec{}, err
	}
	reply, err := ctAskContext(ctx, link.Host, link.Coord, "carrier "+link.Tok+" "+c.Tr)
	if err != nil || !strings.HasPrefix(reply, "carrier ") {
		return TunnelSpec{}, fmt.Errorf("could not fetch %s helper settings: %v", c.Tr, err)
	}
	body, err := unGzipB64(strings.TrimPrefix(reply, "carrier "))
	var settings ctCarrierSettings
	if err != nil || json.Unmarshal(body, &settings) != nil || settings.TargetPort < 1 || settings.TargetPort > 65535 {
		return TunnelSpec{}, errors.New("invalid managed test settings")
	}
	// Use the same paired mapping as production Setup Links. Test trust is
	// written inside the disposable directory rather than the persistent store.
	paired := c
	paired.InnerPort = strconv.Itoa(settings.TargetPort)
	paired.HelperUser, paired.HelperPassword = "backpack", c.Tok
	paired.HelperUUID, paired.HelperSNI = ctUUID(c.Tok), settings.ServerName
	paired.HelperPublicKey, paired.HelperShortID = settings.PublicKey, settings.ShortID
	if c.Tr == "xhttp" {
		paired.HelperPath = "/" + c.Tok
	}
	spec := reverseClientFromLink(paired, link.Host)
	var ca string
	if c.Tr != "reality" {
		if settings.CA == "" {
			return TunnelSpec{}, errors.New("the managed TLS test has no trust certificate")
		}
		if _, err := managedLinkCertificates(settings.CA); err != nil {
			return TunnelSpec{}, err
		}
		ca = filepath.Join(dir, c.Tr+".crt")
		if err := os.WriteFile(ca, []byte(settings.CA), 0600); err != nil {
			return TunnelSpec{}, err
		}
	}
	if c.Tr == "naive" {
		spec.NaiveClient.Binary, spec.NaiveClient.CAFile = binary, ca
	} else {
		spec.XrayClient.Binary, spec.XrayClient.CAFile = binary, ca
	}
	return spec, validateManagedSpec(spec)
}

// Joined is closed once the kharej has checked in.
func (s *ConnTestIran) Joined() <-chan struct{} { return s.coord.joined }

// Kharej is the address the kharej checked in from.
func (s *ConnTestIran) Kharej() string { return s.coord.peerAddr() }

// Run tests every tunnel once the kharej has joined, and publishes the
// verdict for the kharej to fetch. progress is told about each tunnel as it
// comes up or fails; it may be nil.
//
// Every tunnel is reported once at the start, skipped ones included, and each
// running one again after every echo, so a screen can show the test as it
// goes. progress is called from several goroutines at once.
func (s *ConnTestIran) Run(ctx context.Context, progress func(int, ConnTestResult)) []ConnTestResult {
	kharej := s.Kharej()
	s.coord.mu.Lock()
	for _, c := range s.cases {
		if c.skip == "" && s.coord.skipped[c.tr] {
			c.skip = "managed helper could not start on kharej; see its local diagnostic"
		}
	}
	s.coord.mu.Unlock()
	for _, c := range s.cases {
		if c.kind != "direct" || c.skip != "" {
			continue
		}
		_, port, _ := net.SplitHostPort(c.l3.Addr)
		c.l3.Addr = net.JoinHostPort(kharej, port)
		_, body, err := directBodyFromSpec(c.l3)
		if err == nil {
			var e *ctEngine
			if e, err = startCTEngine(s.dir, c.name, body); err == nil {
				s.mu.Lock()
				s.engines = append(s.engines, e)
				s.mu.Unlock()
			}
		}
		if err != nil {
			c.skip = "could not start here: " + err.Error()
		}
	}

	results := make([]ConnTestResult, len(s.cases))
	var mu sync.Mutex
	for i, c := range s.cases {
		results[i] = ConnTestResult{Kind: c.kind, Transport: c.tr, Status: ctTesting, Total: connTestSoak}
		if c.skip != "" {
			results[i].Status, results[i].Detail = ctSkipped, c.skip
		}
	}
	// The kharej sees the same rows, a question at a time.
	s.coord.setLive(func() []ConnTestResult {
		mu.Lock()
		defer mu.Unlock()
		return append([]ConnTestResult(nil), results...)
	})
	if progress != nil {
		for i, r := range results {
			progress(i, r)
		}
	}

	var wg sync.WaitGroup
	for i, c := range s.cases {
		if c.skip != "" {
			continue
		}
		wg.Add(1)
		go func(i int, c *connTestCase) {
			defer wg.Done()
			report := func(r ConnTestResult) {
				mu.Lock()
				results[i] = r
				mu.Unlock()
				if progress != nil {
					progress(i, r)
				}
			}
			if c.kind == "spoof" {
				s.spoofProbe(ctx, c, kharej, report)
				return
			}
			ctProbe(ctx, c, s.link.L3, report)
		}(i, c)
	}
	wg.Wait()

	// The kharej's path-MTU measurement, which it has usually sent long before.
	pmtu := 0
	select {
	case pmtu = <-s.coord.pmtu:
	case <-time.After(15 * time.Second):
	case <-ctx.Done():
	}
	s.best = ctComputeBest(results, s.cases, pmtu, s.dir)
	s.coord.publish(results, s.best)
	return results
}

// Best is the recommendation the finished test came to.
func (s *ConnTestIran) Best() ConnTestBest { return s.best }

// spoofProbe is the IP Spoofing Tester, run in one direction with the one
// forged source, and judged the way the tester judges it: by how many of its
// probes arrived from that source. The Iran server sends and the kharej counts
// for one row, the other way round for the other; the kharej hands its count
// over through the coordinator.
func (s *ConnTestIran) spoofProbe(ctx context.Context, c *connTestCase, kharej string, report func(ConnTestResult)) {
	n := s.link.SpoofN
	r := ConnTestResult{Kind: c.kind, Transport: c.tr, Status: ctTesting, Total: n}
	report(r)
	forged := net.ParseIP(s.link.SpoofSrc)
	type count struct {
		arrived int
		err     error // nothing can be judged
		sendErr error // sending stopped part-way; what arrived still counts
	}
	finished := make(chan count, 1)
	start := time.Now()
	switch c.tr {
	case ctSpoofToKharej:
		go func() {
			// The kharej's receiver opens as it checks in; a moment's grace.
			ctSleep(ctx, 2*time.Second)
			// A send that fails part-way — a firewall on this server refusing
			// the rest — still leaves what went before it on the wire, so the
			// verdict is still what the kharej counted.
			sendErr := spooftest.RunSender(spooftest.SenderConfig{
				Context: ctx,
				Token:   s.link.Tok, TargetIP: net.ParseIP(kharej), DstPort: uint16(s.link.SpoofK),
				Attempts: n, Delay: time.Second, IPs: []net.IP{forged},
			})
			select {
			case got := <-s.coord.spoofArrived:
				if got == 0 && sendErr != nil {
					finished <- count{err: sendErr}
					return
				}
				finished <- count{arrived: got, sendErr: sendErr}
			case <-time.After(45 * time.Second):
				if sendErr == nil {
					sendErr = errors.New("the kharej never reported what arrived")
				}
				finished <- count{err: sendErr}
			case <-ctx.Done():
				finished <- count{err: ctx.Err()}
			}
		}()
	default: // ctSpoofToIran
		go func() {
			res, err := spooftest.RunReceiver(spooftest.ReceiverConfig{
				Context: ctx,
				Token:   s.link.Tok, Port: uint16(s.link.SpoofI), Attempts: n,
				Window: time.Duration(n)*time.Second + 20*time.Second,
			})
			finished <- count{arrived: ctArrivedFrom(res, forged), err: err}
		}()
	}

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			r.Status, r.Detail = ctUnstable, "stopped before the spoofing test completed"
			if r.OK == 0 {
				r.Status = ctDown
			}
			report(r)
			return
		case got := <-finished:
			r.Tried, r.OK = n, got.arrived
			switch {
			case got.err != nil:
				r.Status, r.OK, r.Detail = ctDown, 0, got.err.Error()
			case got.arrived == 0:
				r.Status = ctDown
				r.Detail = "no probe from " + s.link.SpoofSrc + " arrived — a private source is often dropped " +
					"on the way; a public one may pass (Manage → IP Spoofing Tester)"
				if c.tr == ctSpoofToIran {
					if why := ctStrictRPFilter(); why != "" {
						r.Detail = why
					}
				}
			case float64(n-got.arrived)*100/float64(n) <= 20: // the tester's default pass mark
				r.Status = ctOK
			default:
				r.Status = ctUnstable
			}
			if got.err == nil && got.sendErr != nil {
				r.Detail = "sending stopped part-way: " + got.sendErr.Error()
			}
			report(r)
			return
		case <-tick.C:
			// Probes go out one a second from the moment the far side starts.
			if sent := int(time.Since(start) / time.Second); sent <= n {
				r.Tried = sent
				report(r)
			}
		}
	}
}

// ctArrivedFrom is how many distinct probes arrived from ip.
func ctArrivedFrom(results []spooftest.Result, ip net.IP) int {
	for _, r := range results {
		if r.IP.Equal(ip) {
			return r.Arrived
		}
	}
	return 0
}

// ctKharejSpoof is the kharej's half of the spoofing check: it counts the Iran
// server's probes and reports the count, and sends its own.
func ctKharejSpoof(ctx context.Context, link ConnTestLink, root bool) {
	n := link.SpoofN
	forged := net.ParseIP(link.SpoofSrc)
	go func() {
		res, err := spooftest.RunReceiver(spooftest.ReceiverConfig{
			Context: ctx,
			Token:   link.Tok, Port: uint16(link.SpoofK), Attempts: n,
			Window: time.Duration(n)*time.Second + 20*time.Second,
		})
		got := 0
		if err == nil {
			got = ctArrivedFrom(res, forged)
		}
		for i := 0; i < 8 && ctx.Err() == nil; i++ {
			if reply, err := ctAskContext(ctx, link.Host, link.Coord, fmt.Sprintf("spoof %s %d", link.Tok, got)); err == nil && reply == "ok" {
				return
			}
			ctSleep(ctx, 2*time.Second)
		}
	}()
	if root {
		go func() {
			ctSleep(ctx, 2*time.Second)
			_ = spooftest.RunSender(spooftest.SenderConfig{
				Context: ctx,
				Token:   link.Tok, TargetIP: net.ParseIP(ctIPv4Context(ctx, link.Host)), DstPort: uint16(link.SpoofI),
				Attempts: n, Delay: time.Second, IPs: []net.IP{forged},
			})
		}()
	}
}

// Fetched is closed once the kharej has collected the verdict.
func (s *ConnTestIran) Fetched() <-chan struct{} { return s.coord.fetched }

// Close stops every engine and the coordinator and removes the test's files.
func (s *ConnTestIran) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	engines := s.engines
	s.engines = nil
	s.mu.Unlock()
	ctStopAll(engines)
	if s.coord != nil {
		s.coord.close()
	}
	os.RemoveAll(s.dir)
}

// ctProbe pushes traffic through one tunnel and judges it.
// report is told the tunnel's state whenever it changes: after every echo,
// and once more with the verdict, which is also returned.
func ctProbe(ctx context.Context, c *connTestCase, l3Echo int, report func(ConnTestResult)) ConnTestResult {
	r := ConnTestResult{Kind: c.kind, Transport: c.tr, Status: ctTesting, Total: connTestSoak}
	say := func(string) {
		if report != nil {
			report(r)
		}
	}
	network, addr := "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(c.entry))
	if c.udp {
		network = "udp"
	}
	if c.kind == "direct" {
		addr = net.JoinHostPort(c.peerIP, strconv.Itoa(l3Echo))
	}
	dial := func() (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, addr)
	}

	// Up: the first echo that comes back.
	start := time.Now()
	var conn net.Conn
	for conn == nil {
		if ctx.Err() != nil {
			r.Status, r.Detail = ctDown, "stopped"
			say("")
			return r
		}
		if time.Since(start) > connectWaitFor(c) {
			r.Status, r.Detail = ctDown, fmt.Sprintf("never carried traffic in %s", connectWaitFor(c))
			if c.tr == "spoof" {
				r.Detail += " — the forged source was dropped: by the provider on the way out, " +
					"or by a strict rp_filter on the kharej"
			}
			say("never came up")
			return r
		}
		if cn, err := dial(); err == nil {
			stopClose := context.AfterFunc(ctx, func() { cn.Close() })
			echoErr := ctEcho(cn, c.udp)
			stopClose()
			if echoErr == nil {
				conn = cn
				break
			}
			cn.Close()
		}
		ctSleep(ctx, time.Second)
	}
	r.Connect = time.Since(start).Seconds()
	say(fmt.Sprintf("up after %.0fs", r.Connect))

	// Staying up: an echo a second over the same connection, and a new one
	// only when that one is lost — which is exactly what a user's connection
	// would see.
	var rtt time.Duration
	var firstLoss time.Duration
	soakStart := time.Now()
	for i := 0; i < connTestSoak && ctx.Err() == nil; i++ {
		ctSleep(ctx, time.Until(soakStart.Add(time.Duration(i)*time.Second)))
		if ctx.Err() != nil {
			break
		}
		r.Tried++
		if conn == nil {
			cn, err := dial()
			if err != nil {
				if firstLoss == 0 {
					firstLoss = time.Since(soakStart)
				}
				say("")
				continue
			}
			conn = cn
		}
		t0 := time.Now()
		active := conn
		stopClose := context.AfterFunc(ctx, func() { active.Close() })
		err := ctEcho(conn, c.udp)
		stopClose()
		if err != nil {
			conn.Close()
			conn = nil
			if firstLoss == 0 {
				firstLoss = time.Since(soakStart)
			}
			say("")
			continue
		}
		r.OK++
		took := time.Since(t0)
		rtt += took
		c.rtts = append(c.rtts, took)
		say("")
	}
	if conn != nil {
		conn.Close()
	}
	if r.OK > 0 {
		r.RTTms = int((rtt / time.Duration(r.OK)).Milliseconds())
	}

	// Speed, over a fresh connection, for the tunnels that carry TCP.
	bulkErr := error(nil)
	if !c.udp && ctx.Err() == nil {
		r.Mbps, bulkErr = ctBulkContext(ctx, dial)
	}

	switch {
	case ctx.Err() != nil:
		r.Status, r.Detail = ctUnstable, "stopped before the test completed"
		if r.OK == 0 {
			r.Status = ctDown
		}
	case r.OK == connTestSoak && r.Tried == connTestSoak && bulkErr == nil:
		r.Status = ctOK
	case r.OK == 0:
		r.Status = ctDown
		r.Detail = fmt.Sprintf("came up, then carried nothing (lost after %.0fs)", firstLoss.Seconds())
	default:
		r.Status = ctUnstable
		if firstLoss > 0 {
			r.Detail = fmt.Sprintf("first loss after %.0fs", firstLoss.Seconds())
		}
		if bulkErr != nil {
			r.Detail = strings.TrimPrefix(r.Detail+"; bulk transfer failed ("+ctShortErr(bulkErr)+")", "; ")
		}
	}
	say("done: " + r.Status)
	return r
}

// connectWaitFor is how long a tunnel is given to come up. The direct ones
// wait for the kharej to have built its interface as well.
func connectWaitFor(c *connTestCase) time.Duration {
	if c.kind == "direct" {
		return connTestConnectWait + 30*time.Second
	}
	return connTestConnectWait
}

// ctEcho sends a small random payload and requires it back unchanged.
func ctEcho(c net.Conn, udp bool) error {
	msg := make([]byte, 512)
	_, _ = rand.Read(msg)
	_ = c.SetDeadline(time.Now().Add(4 * time.Second))
	defer c.SetDeadline(time.Time{})
	if n, err := c.Write(msg); err != nil {
		return err
	} else if n != len(msg) {
		return io.ErrShortWrite
	}
	got := make([]byte, len(msg))
	if udp {
		n, err := c.Read(got)
		if err != nil {
			return err
		}
		got = got[:n]
	} else if _, err := io.ReadFull(c, got); err != nil {
		return err
	}
	if !bytes.Equal(got, msg) {
		return errors.New("the echo came back different")
	}
	return nil
}

// ctBulk sends connTestBulk bytes through a fresh connection and reads them
// back, and reports the speed.
func ctBulk(dial func() (net.Conn, error)) (float64, error) {
	return ctBulkContext(context.Background(), dial)
}

func ctBulkContext(ctx context.Context, dial func() (net.Conn, error)) (float64, error) {
	c, err := dial()
	if err != nil {
		return 0, err
	}
	defer c.Close()
	stopClose := context.AfterFunc(ctx, func() { c.Close() })
	defer stopClose()
	_ = c.SetDeadline(time.Now().Add(40 * time.Second))
	payload := make([]byte, connTestBulk)
	_, _ = rand.Read(payload)
	start := time.Now()
	werr := make(chan error, 1)
	go func() {
		n, err := c.Write(payload)
		if err == nil && n != len(payload) {
			err = io.ErrShortWrite
		}
		werr <- err
	}()
	got := make([]byte, len(payload))
	if n, err := io.ReadFull(c, got); err != nil {
		c.Close()
		<-werr
		return 0, fmt.Errorf("%d of %d KB back: %w", n>>10, len(payload)>>10, err)
	}
	if err := <-werr; err != nil {
		return 0, err
	}
	if !bytes.Equal(got, payload) {
		return 0, errors.New("the transfer came back different")
	}
	secs := time.Since(start).Seconds()
	return float64(2*connTestBulk*8) / secs / 1e6, nil
}

func ctSleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// ctPickPort is a random port free on this server (for TCP, and for UDP as
// well when both is set) and not already given to another part of the test.
// The kharej's echo ports are picked here too; they are high and random, so a
// clash there is unlikely, and a tunnel that meets one reports it.
func ctPickPort(used map[int]bool, both bool) int {
	for i := 0; i < 500; i++ {
		var b [2]byte
		_, _ = rand.Read(b[:])
		p := 20000 + (int(b[0])<<8|int(b[1]))%40000
		if used[p] {
			continue
		}
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err != nil {
			continue
		}
		l.Close()
		if both {
			u, err := net.ListenPacket("udp", fmt.Sprintf(":%d", p))
			if err != nil {
				continue
			}
			u.Close()
		}
		used[p] = true
		return p
	}
	return 0
}

// ctFreeL3Block is a 10.10.N. block no tunnel on this server uses, from the
// top of the range the wizard counts up from, one per direct tunnel under
// test.
func ctFreeL3Block(i int) string {
	skip := i
	for n := 250; n > 0; n-- {
		block := fmt.Sprintf("10.10.%d.", n)
		if l3BlockOwner(block+"1") != "" {
			continue
		}
		if skip == 0 {
			return block
		}
		skip--
	}
	return fmt.Sprintf("10.10.%d.", 250-i)
}

// ctQuiet turns off the kernel tuning in a reverse config: it is for a
// tunnel that stays, and the tunnels here last two minutes.
func ctQuiet(body string) string {
	for _, table := range []string{"[server]\n", "[client]\n"} {
		body = strings.Replace(body, table, table+"skip_optz = true\n", 1)
	}
	return body
}

// ctEngine is one engine process under test.
type ctEngine struct {
	name string
	cmd  *exec.Cmd
	log  string
	done chan struct{}
}

func startCTEngine(dir, name, body string) (*ctEngine, error) {
	bin, err := connTestBinary()
	if err != nil {
		return nil, err
	}
	cfg := filepath.Join(dir, name+".toml")
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		return nil, err
	}
	logPath := filepath.Join(dir, name+".log")
	f, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "-c", cfg)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, fmt.Errorf("starting the %s engine: %w", name, err)
	}
	e := &ctEngine{name: name, cmd: cmd, log: logPath, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		f.Close()
		close(e.done)
	}()
	return e, nil
}

// exited reports whether the engine has stopped on its own, and the last
// thing it said.
func (e *ctEngine) exited() (bool, string) {
	select {
	case <-e.done:
		return true, ctLastLine(e.log)
	default:
		return false, ""
	}
}

// ctStopAll stops engines the way systemd would — asked first, so each takes
// down its interface and firewall rules — and kills what is still there.
func ctStopAll(engines []*ctEngine) {
	for _, e := range engines {
		select {
		case <-e.done:
		default:
			_ = e.cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	deadline := time.NewTimer(connTestStopWait)
	defer deadline.Stop()
	expired := false
	for _, e := range engines {
		if expired {
			select {
			case <-e.done:
				continue
			default:
				_ = syscall.Kill(-e.cmd.Process.Pid, syscall.SIGKILL)
				<-e.done
				continue
			}
		}
		select {
		case <-e.done:
		case <-deadline.C:
			expired = true
			_ = syscall.Kill(-e.cmd.Process.Pid, syscall.SIGKILL)
			<-e.done
		}
	}
}

func ctLastLine(path string) string {
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// The coordinator: one question and one answer per connection, over TCP and
// over UDP, so that a path cutting flows after a few packets — or one that
// carries only one of the two — still lets the two sides talk.
//
//	config <token>  → config <c>  the test, for the kharej to build its end from
//	hello <token>   → ok          the kharej is in; its address is noted
//	result <token>  → wait | done <verdict>
type ctCoordinator struct {
	tok      string
	tcp      net.Listener
	udp      net.PacketConn
	mu       sync.Mutex
	peer     string
	verdict  string
	config   string            // the answer to "config"; see ctConfig
	carriers map[string]string // public helper settings, fetched one small packet at a time
	skipped  map[string]bool   // managed cases unavailable on kharej
	live     func() []ConnTestResult
	joined   chan struct{}
	fetched  chan struct{}
	joinOne  sync.Once
	fetchOn  sync.Once
	sockets  ctEchoes
	slots    chan struct{}
	// spoofArrived is the kharej's count of the Iran server's forged probes.
	spoofArrived chan int
	spoofOnce    sync.Once
	// pmtu is the path MTU the kharej measured.
	pmtu     chan int
	pmtuOnce sync.Once
}

func startCTCoordinator(port int, tok string) (*ctCoordinator, error) {
	c := &ctCoordinator{tok: tok, joined: make(chan struct{}), fetched: make(chan struct{}), slots: make(chan struct{}, 128),
		spoofArrived: make(chan int, 1), pmtu: make(chan int, 1)}
	var err error
	if c.tcp, err = net.Listen("tcp", fmt.Sprintf(":%d", port)); err != nil {
		return nil, err
	}
	if c.udp, err = net.ListenPacket("udp", fmt.Sprintf(":%d", port)); err != nil {
		c.tcp.Close()
		return nil, err
	}
	go c.serveTCP()
	go c.serveUDP()
	return c, nil
}

func (c *ctCoordinator) serveTCP() {
	for {
		conn, err := c.tcp.Accept()
		if err != nil {
			return
		}
		select {
		case c.slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		c.sockets.add(conn)
		go func() {
			defer func() { <-c.slots; c.sockets.remove(conn) }()
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			line, err := bufio.NewReader(io.LimitReader(conn, 512)).ReadString('\n')
			if err != nil {
				return
			}
			host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
			if reply := c.answer(line, host); reply != "" {
				_, _ = conn.Write([]byte(reply + "\n"))
			}
		}()
	}
}

func (c *ctCoordinator) serveUDP() {
	buf := make([]byte, 2048) // a path-MTU probe is up to a full frame
	for {
		n, from, err := c.udp.ReadFrom(buf)
		if err != nil {
			return
		}
		host, _, _ := net.SplitHostPort(from.String())
		if reply := c.answer(string(buf[:n]), host); reply != "" {
			_, _ = c.udp.WriteTo([]byte(reply+"\n"), from)
		}
	}
}

// answer is the coordinator's whole protocol. An empty answer is silence: a
// request without the token gets nothing, not even a refusal.
//
//	spoof <token> <n>  → ok      the kharej counted n forged probes
//	pmtu <token> <n> <pad> → pm <n>  a path-MTU probe of n bytes
//	pmtu <token> <n>   → ok      the path MTU the kharej measured
func (c *ctCoordinator) answer(line, from string) string {
	f := strings.Fields(line)
	if len(f) < 2 || subtle.ConstantTimeCompare([]byte(f[1]), []byte(c.tok)) != 1 {
		return ""
	}
	// A path-MTU probe: its padding is the point, and the answer is small.
	if f[0] == "pmtu" && len(f) == 4 {
		return "pm " + f[2]
	}
	if len(f) > 3 {
		return ""
	}
	if f[0] == "skip" && len(f) == 3 && managedTransport(f[2]) {
		c.mu.Lock()
		if c.skipped == nil {
			c.skipped = make(map[string]bool)
		}
		c.skipped[f[2]] = true
		c.mu.Unlock()
		return "ok"
	}
	if f[0] == "carrier" && len(f) == 3 {
		c.mu.Lock()
		body := c.carriers[f[2]]
		c.mu.Unlock()
		if body != "" {
			return "carrier " + body
		}
		return ""
	}
	if f[0] == "pmtu" {
		n, err := strconv.Atoi(f[len(f)-1])
		if len(f) != 3 || err != nil || n < 0 {
			return ""
		}
		c.pmtuOnce.Do(func() { c.pmtu <- n })
		return "ok"
	}
	if f[0] == "spoof" {
		n, err := strconv.Atoi(f[len(f)-1])
		if len(f) != 3 || err != nil || n < 0 {
			return ""
		}
		// Asked again when an answer was lost; the first count is the count.
		c.spoofOnce.Do(func() { c.spoofArrived <- n })
		return "ok"
	}
	if len(f) != 2 {
		return ""
	}
	switch f[0] {
	case "config":
		c.mu.Lock()
		cfg := c.config
		c.mu.Unlock()
		if cfg == "" {
			return ""
		}
		return "config " + cfg
	case "hello":
		c.mu.Lock()
		if c.peer == "" {
			c.peer = strings.TrimPrefix(from, "::ffff:")
		}
		c.mu.Unlock()
		c.joinOne.Do(func() { close(c.joined) })
		return "ok"
	case "result":
		c.mu.Lock()
		v, live := c.verdict, c.live
		c.mu.Unlock()
		if v == "" {
			if live == nil {
				return "wait"
			}
			raw, _ := json.Marshal(live())
			return "live " + gzipB64(raw)
		}
		c.fetchOn.Do(func() { close(c.fetched) })
		return "done " + v
	}
	return ""
}

func (c *ctCoordinator) peerAddr() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peer
}

// setConfig sets what the kharej is told the test is.
func (c *ctCoordinator) setConfig(cfg string) {
	c.mu.Lock()
	c.config = cfg
	c.mu.Unlock()
}

// setLive hands the coordinator the running test's rows, for the kharej's
// screen.
func (c *ctCoordinator) setLive(rows func() []ConnTestResult) {
	c.mu.Lock()
	c.live = rows
	c.mu.Unlock()
}

// ctVerdict is what the kharej fetches when the test is over.
type ctVerdict struct {
	Results []ConnTestResult `json:"r"`
	Best    ConnTestBest     `json:"b"`
}

func (c *ctCoordinator) publish(results []ConnTestResult, best ConnTestBest) {
	raw, _ := json.Marshal(ctVerdict{Results: results, Best: best})
	c.mu.Lock()
	c.verdict = gzipB64(raw)
	c.mu.Unlock()
}

func (c *ctCoordinator) close() {
	c.tcp.Close()
	c.udp.Close()
	c.sockets.close()
}

// ctAskContext puts one question to the coordinator over TCP and then UDP,
// stopping the dial and any pending reply when the test is cancelled.
func ctAskContext(ctx context.Context, host string, port int, line string) (string, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	var last error
	for _, network := range []string{"tcp", "udp"} {
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
		if err != nil {
			last = err
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		stopClose := context.AfterFunc(ctx, func() { conn.Close() })
		_, err = conn.Write([]byte(line + "\n"))
		if err == nil {
			buf := make([]byte, 8192)
			var n int
			if network == "udp" {
				n, err = conn.Read(buf)
			} else {
				n, err = io.ReadFull(conn, buf)
				if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
					err = nil
				}
			}
			if err == nil && n > 0 {
				stopClose()
				conn.Close()
				return strings.TrimSpace(string(buf[:n])), nil
			}
		}
		stopClose()
		conn.Close()
		last = err
	}
	if last == nil {
		last = errors.New("no answer")
	}
	return "", last
}

// RunConnTestKharej is the kharej's side of a test: it builds its end of
// every tunnel in the link, checks in with the Iran side, waits for the
// verdict and prints it to out. live, when set, is handed the Iran side's rows
// every couple of seconds while the test runs. Everything it started is
// stopped before it returns.
func RunConnTestKharej(ctx context.Context, raw string, out io.Writer, live func([]ConnTestResult)) ([]ConnTestResult, ConnTestBest, error) {
	addr, err := parseConnTestLink(raw)
	if err != nil {
		return nil, ConnTestBest{}, err
	}
	fmt.Fprintf(out, "Fetching the test from %s...\n", addr.Host)
	fetchCtx, cancelFetch := context.WithTimeout(ctx, 3*time.Minute)
	link, err := fetchConnTestLink(fetchCtx, addr)
	cancelFetch()
	if err != nil {
		return nil, ConnTestBest{}, err
	}
	until := time.Unix(link.Until, 0)
	if time.Now().After(until) {
		return nil, ConnTestBest{}, fmt.Errorf("this test link has expired — start a new test on the Iran server")
	}
	ctx, cancel := context.WithDeadline(ctx, until)
	defer cancel()
	root := os.Geteuid() == 0

	dir, err := os.MkdirTemp("", "backpack-conntest-")
	if err != nil {
		return nil, ConnTestBest{}, err
	}
	defer os.RemoveAll(dir)

	// The far ends of the tunnels: what the Iran side's echoes land on.
	echoes, err := ctStartEchoes(ctx, link)
	if err != nil {
		return nil, ConnTestBest{}, err
	}
	defer echoes.close()

	var engines []*ctEngine
	defer func() { ctStopAll(engines) }()
	local := map[string]string{}  // kind/transport → why this side could not run it
	caseOf := map[string]string{} // engine name → kind/transport
	for i, c := range link.Cases {
		name := c.Name
		caseOf[name] = c.Kind + "/" + c.Tr
		var body string
		switch c.Kind {
		case "reverse":
			if connTestNeedsRoot(c.Tr) && !root {
				local[c.Kind+"/"+c.Tr] = "needs root"
				continue
			}
			var spec TunnelSpec
			var err error
			if managedTransport(c.Tr) {
				spec, err = ctManagedClient(ctx, link, c, dir)
			} else {
				spec, err = kharejFromLink(c, LinkApplyOptions{Host: link.Host})
			}
			if err != nil {
				local[c.Kind+"/"+c.Tr] = err.Error()
				if managedTransport(c.Tr) {
					_, _ = ctAskContext(ctx, link.Host, link.Coord, "skip "+link.Tok+" "+c.Tr)
				}
				continue
			}
			spec.Name = name
			body = ctQuiet(spec.Render())
		case "direct":
			if !root {
				local[c.Kind+"/"+c.Tr] = "needs root"
				continue
			}
			form := MirrorForPeer(c)
			form.Name = name
			spec, err := form.ToNewDirectTunnel().spec()
			if err != nil {
				local[c.Kind+"/"+c.Tr] = err.Error()
				continue
			}
			spec.Name, spec.Iface = name, fmt.Sprintf("bpt%d", i)
			if _, body, err = directBodyFromSpec(spec); err != nil {
				local[c.Kind+"/"+c.Tr] = err.Error()
				continue
			}
			echoes.l3(ctx, strings.SplitN(spec.LocalIP, "/", 2)[0], link.L3)
		}
		e, err := startCTEngine(dir, name, body)
		if err != nil {
			local[c.Kind+"/"+c.Tr] = err.Error()
			if managedTransport(c.Tr) {
				_, _ = ctAskContext(ctx, link.Host, link.Coord, "skip "+link.Tok+" "+c.Tr)
			}
			continue
		}
		engines = append(engines, e)
	}
	fmt.Fprintf(out, "Started %d test tunnels to %s (preset %s). Checking in with the Iran server...\n",
		len(engines), link.Host, link.Preset)

	// Checking in: every few seconds until the Iran side hears it.
	for {
		reply, err := ctAskContext(ctx, link.Host, link.Coord, "hello "+link.Tok)
		if err == nil && reply == "ok" {
			break
		}
		if ctx.Err() != nil {
			return nil, ConnTestBest{}, fmt.Errorf("the Iran server's test coordinator (port %d, TCP and UDP) never answered — "+
				"nothing at all gets through from here to there, or the test there has ended", link.Coord)
		}
		ctSleep(ctx, 3*time.Second)
	}
	if link.SpoofSrc != "" {
		if why := ctStrictRPFilter(); why != "" {
			local["spoof/"+ctSpoofToKharej] = why
			fmt.Fprintf(out, "  IP spoofing: %s\n", why)
		}
		if !root {
			local["spoof/"+ctSpoofToIran] = "sending forged probes needs root here"
		}
		ctKharejSpoof(ctx, link, root)
	}
	go ctKharejPMTU(ctx, link)
	fmt.Fprintf(out, "The Iran server has started the test. It takes about %d minutes...\n",
		int((connTestConnectWait+30*time.Second+time.Duration(connTestSoak)*time.Second+time.Minute).Minutes())+1)

	// The verdict, and the rows as they fill until it comes.
	for {
		ctSleep(ctx, 2*time.Second)
		for _, e := range engines {
			// Kept for the verdict rather than printed: a line written here
			// would land in the middle of the live table.
			if gone, why := e.exited(); gone && local[caseOf[e.name]] == "" {
				local[caseOf[e.name]] = "the engine here stopped: " + why
			}
		}
		reply, err := ctAskContext(ctx, link.Host, link.Coord, "result "+link.Tok)
		if err == nil && strings.HasPrefix(reply, "live ") && live != nil {
			if raw, err := unGzipB64(strings.TrimPrefix(reply, "live ")); err == nil {
				var rows []ConnTestResult
				if json.Unmarshal(raw, &rows) == nil {
					live(rows)
				}
			}
			continue
		}
		if err == nil && strings.HasPrefix(reply, "done ") {
			raw, err := unGzipB64(strings.TrimPrefix(reply, "done "))
			if err != nil {
				return nil, ConnTestBest{}, fmt.Errorf("the verdict arrived damaged — read it on the Iran server")
			}
			var v ctVerdict
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, ConnTestBest{}, fmt.Errorf("the verdict arrived damaged — read it on the Iran server")
			}
			for i, r := range v.Results {
				if why := local[r.Kind+"/"+r.Transport]; why != "" && r.Status != ctOK {
					v.Results[i].Detail = strings.TrimPrefix(v.Results[i].Detail+"; kharej: "+why, "; ")
				}
			}
			return v.Results, v.Best, nil
		}
		if ctx.Err() != nil {
			return nil, ConnTestBest{}, fmt.Errorf("the verdict never arrived from the Iran server — read it there")
		}
	}
}

// ctEchoes are the kharej's echo servers.
type ctEchoes struct {
	mu      sync.Mutex
	closers []io.Closer
	closed  bool
}

func ctStartEchoes(ctx context.Context, link ConnTestLink) (*ctEchoes, error) {
	e := &ctEchoes{}
	tl, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(link.TCP)))
	if err != nil {
		return nil, fmt.Errorf("port %d is taken on this server — start a new test on the Iran server: %w", link.TCP, err)
	}
	e.add(tl)
	go ctServeTCPEcho(tl, e)
	ul, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(link.UDP)))
	if err != nil {
		e.close()
		return nil, fmt.Errorf("port %d is taken on this server — start a new test on the Iran server: %w", link.UDP, err)
	}
	e.add(ul)
	go ctServeUDPEcho(ul)
	return e, nil
}

// l3 starts an echo on a direct tunnel's own address, once its interface is
// up to bind it.
func (e *ctEchoes) l3(ctx context.Context, ip string, port int) {
	go func() {
		for ctx.Err() == nil {
			l, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
			if err == nil {
				e.add(l)
				ctServeTCPEcho(l, e)
				return
			}
			ctSleep(ctx, time.Second)
		}
	}()
}

func (e *ctEchoes) add(c io.Closer) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		c.Close()
		return
	}
	e.closers = append(e.closers, c)
}

func (e *ctEchoes) remove(c io.Closer) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, current := range e.closers {
		if current == c {
			e.closers = append(e.closers[:i], e.closers[i+1:]...)
			return
		}
	}
}

func (e *ctEchoes) close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	for _, c := range e.closers {
		c.Close()
	}
	e.closers = nil
}

func ctServeTCPEcho(l net.Listener, owners ...*ctEchoes) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		for _, owner := range owners {
			owner.add(c)
		}
		go func() {
			defer c.Close()
			defer func() {
				for _, owner := range owners {
					owner.remove(c)
				}
			}()
			_, _ = io.Copy(c, c)
		}()
	}
}

func ctServeUDPEcho(l net.PacketConn) {
	buf := make([]byte, 65535)
	for {
		n, from, err := l.ReadFrom(buf)
		if err != nil {
			return
		}
		_, _ = l.WriteTo(buf[:n], from)
	}
}

// ctIPv4 is host as an IPv4 address, resolving a name; empty when there is none.
func ctIPv4(host string) string {
	return ctIPv4Context(context.Background(), host)
}

func ctIPv4Context(ctx context.Context, host string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			return ip.String()
		}
		return ""
	}
	ips, _ := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip.String()
		}
	}
	return ""
}

// ctStrictRPFilter says why this server would drop the spoof carrier's
// packets on arrival, when a strict reverse-path filter would: a forged
// source fails the check and the kernel discards it before the carrier sees
// it. Empty when the filter is loose or off.
func ctStrictRPFilter() string {
	b, err := os.ReadFile("/proc/sys/net/ipv4/conf/all/rp_filter")
	if err != nil || strings.TrimSpace(string(b)) != "1" {
		return ""
	}
	return "rp_filter is strict (1) on this server, which drops forged sources on arrival — " +
		"run: sysctl -w net.ipv4.conf.all.rp_filter=2 (and on the receiving interface), then test again"
}

// ctShortErr is an error without the addresses a net error repeats.
func ctShortErr(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 && strings.Contains(msg[:i], "->") {
		head, _, _ := strings.Cut(msg, ": ")
		return head + ": " + msg[i+2:]
	}
	return msg
}

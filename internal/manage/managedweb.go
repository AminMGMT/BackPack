package manage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"

	"github.com/backpack/backpack/internal/app"
)

// The managed HTTPS carriers (Naive, XHTTP, REALITY) from the panel.
//
// The CLI wizard asks a dozen questions for these, and every one of them has a
// default it offers: the helper's installed path, a generated user, password,
// UUID, path or key pair, a private certificate for the address Kharej dials,
// and — for REALITY — the first cover endpoint that passes a real check from
// this server. The panel takes exactly those defaults, so the form needs no
// more than it already asks plus an optional REALITY cover.
//
// Only the Iran end is built here. The Kharej end needs the credentials and
// the private certificate's public half, and the setup link this tunnel's
// "other side" step shows already carries all of it (see shareLinkOf), so the
// Kharej side is set up from that link, as it is from the CLI.

// What managedFromPanel reaches outside itself for, so a test can supply a
// helper and a certificate without an install or /etc/backpack.
var (
	panelHelperPath    = managedHelperPath
	panelRealityBinary = func() string { return realityBinary("") }
	panelCert          = managedPrivateCert
	panelRealityProbe  = probeRealitySetup
)

// managedForm is what the panel adds to a NewTunnel for a managed carrier.
type managedForm struct {
	chosen string // naive, xhttp or reality
	host   string // the address Kharej dials: names the private certificate
	cover  string // REALITY only: hostname:port, empty to pick one
	// tlsName is XHTTP's TLS name, a DNS name. The kharej dials host and
	// checks the private certificate against this name, so it need not
	// resolve; empty takes host when host is a name.
	tlsName string
}

// managedFromPanel turns s — already built as a plain TCP Iran end whose
// BindAddr is the public HTTPS listener — into a managed carrier.
func managedFromPanel(s *TunnelSpec, f managedForm, probe realitySetupProbe) error {
	if s.Role != "server" {
		return fmt.Errorf("set up the kharej end of a %s tunnel from the Iran server's setup link", f.chosen)
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("the HTTPS transports need Linux")
	}
	public := s.BindAddr
	internal := realityInternalPort(public, s.Ports)
	if internal == "" {
		return fmt.Errorf("no free local port for the tunnel behind %s", public)
	}

	// What the wizard does to the engine half: plain TCP on loopback, behind
	// the helper, with nothing that would reach past it.
	n := *s
	n.Transport = "tcp"
	n.BindAddr = net.JoinHostPort("127.0.0.1", internal)
	n.AcceptUDP, n.SimpleAuth, n.LoadBalance, n.HealthFailover = false, false, false, false
	n.FallbackAddrs, n.FallbackTransports = nil, nil
	n.FallbackDwell, n.SOMark = 0, 0
	n.Proxy, n.LocalAddr, n.Interface, n.EdgeIP = "", "", "", ""
	n.ACMEDomain, n.ACMEEmail = "", ""
	n.ProxyProtocol = false
	clearManagedTransport(&n)

	host := strings.Trim(strings.TrimSpace(f.host), "[]")
	switch f.chosen {
	case "naive":
		binary, err := managedHelper("sing-box")
		if err != nil {
			return err
		}
		cert, key, err := panelCert(n.Name, host)
		if err != nil {
			return err
		}
		n.NaiveServer.Binary, n.NaiveServer.Listen = binary, public
		n.NaiveServer.Username, n.NaiveServer.Password = "backpack", randomToken(32)
		n.NaiveServer.Certificate, n.NaiveServer.Key = cert, key
	case "xhttp":
		binary, err := managedHelper("xray")
		if err != nil {
			return err
		}
		name := strings.ToLower(strings.TrimSpace(f.tlsName))
		if name == "" && net.ParseIP(host) == nil {
			name = host
		}
		if name == "" || net.ParseIP(name) != nil {
			return fmt.Errorf("XHTTP needs a TLS name (a domain, such as cdn.example.com); it need not point here")
		}
		cert, key, err := panelCert(n.Name, name)
		if err != nil {
			return err
		}
		id, err := managedUUID()
		if err != nil {
			return err
		}
		x := &n.XrayServer
		x.Binary, x.Listen, x.Mode = binary, public, "xhttp"
		x.UUID, x.ServerName, x.Path = id, name, "/"+randomToken(24)
		x.Certificate, x.Key = cert, key
	case "reality":
		binary := panelRealityBinary()
		if !executable(binary) {
			return helperMissing("xray", binary)
		}
		target, sni, err := realityCoverFor(f.cover, binary, probe)
		if err != nil {
			return err
		}
		id, err := managedUUID()
		if err != nil {
			return err
		}
		private, _, err := managedRealityKey("")
		if err != nil {
			return err
		}
		var short [8]byte
		if _, err := rand.Read(short[:]); err != nil {
			return err
		}
		x := &n.XrayServer
		x.Binary, x.Listen, x.Mode = binary, public, "reality"
		x.Target, x.ServerName = target, sni
		x.UUID, x.PrivateKey, x.ShortID = id, private, hex.EncodeToString(short[:])
	default:
		return fmt.Errorf("%q is not an HTTPS transport", f.chosen)
	}

	if err := validateManagedSpec(n); err != nil {
		return fmt.Errorf("HTTPS configuration: %w", err)
	}
	if !fileExists(app.ConfigPath(n.Name)) {
		for _, addr := range []string{public, n.BindAddr} {
			if busy := reverseBusyPorts(n.Ports, addr, "tcp"); len(busy) > 0 {
				return fmt.Errorf("the HTTPS ports clash with the forwarded ports: %s", strings.Join(busy, ", "))
			}
			if portHeld(addr) {
				return fmt.Errorf("%s is already in use on this server", addr)
			}
		}
	}
	*s = n
	return nil
}

// managedPrivateCert makes the private certificate Kharej is told to trust
// through the setup link. It names the address Kharej dials.
func managedPrivateCert(name, host string) (string, string, error) {
	if host == "" {
		return "", "", fmt.Errorf("this server's public address is unknown, so no certificate can name it")
	}
	cert, key, err := EnsureSelfSignedCert(name+"-https", host)
	if err != nil {
		return "", "", fmt.Errorf("could not make the HTTPS certificate: %w", err)
	}
	return cert, key, nil
}

// realityCoverFor checks the cover the form named, or tries the suggestions
// and takes the first that passes, in their order.
func realityCoverFor(cover, binary string, probe realitySetupProbe) (string, string, error) {
	cover = strings.TrimSpace(cover)
	if cover != "" {
		host, port, err := net.SplitHostPort(cover)
		if err != nil || host == "" || !validPort(port) || net.ParseIP(host) != nil {
			return "", "", fmt.Errorf("the REALITY cover must be a hostname and port, such as www.microsoft.com:443")
		}
		ctx, cancel := context.WithTimeout(context.Background(), connTestCoverWait)
		defer cancel()
		if err := probe(ctx, cover, host, binary); err != nil {
			return "", "", fmt.Errorf("the REALITY cover %s did not pass: %w", cover, err)
		}
		return cover, host, nil
	}
	var reasons []string
	for _, r := range scanRealityCovers(context.Background(), realityCoverSuggestions, binary, probe) {
		if r.err == nil {
			host, _, _ := net.SplitHostPort(r.target)
			return r.target, host, nil
		}
		reasons = append(reasons, r.target+": "+r.err.Error())
	}
	return "", "", fmt.Errorf("no REALITY cover passed from this server (%s) — name one that does",
		strings.Join(reasons, "; "))
}

// managedHelper is the installed helper's path, or why it cannot be used.
func managedHelper(tool string) (string, error) {
	path := panelHelperPath(tool)
	if !executable(path) {
		return "", helperMissing(tool, path)
	}
	return path, nil
}

func helperMissing(tool, path string) error {
	return fmt.Errorf("the %s helper is not installed at %s — install it with BP_HELPERS=naive,xray bash install.sh", tool, path)
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0111 != 0
}

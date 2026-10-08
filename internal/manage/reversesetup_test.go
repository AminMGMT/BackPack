package manage

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tui"
)

func TestManagedHTTPSChoicesReachBothReverseWizards(t *testing.T) {
	for i, want := range []string{"naive", "xhttp", "reality"} {
		restore := tui.SetInput(strings.NewReader(fmt.Sprintf("%d\n%d\n", len(transportGroups)+1, i+1)))
		var got string
		capture(t, func() { got = chooseTransport() })
		restore()
		if got != want {
			t.Fatalf("HTTPS choice %d selected %q, want %s", i+1, got, want)
		}
		if transportLabel(got) == strings.ToUpper(got) {
			t.Fatalf("missing display label for %s", got)
		}
	}
}

func managedWizardFixture(t *testing.T) (binary, cert, key string) {
	t.Helper()
	dir := t.TempDir()
	binary, cert, key = filepath.Join(dir, "helper"), filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(binary, []byte("fixture: configuration validation only\n"), 0700); err != nil {
		t.Fatal(err)
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{SerialNumber: big.NewInt(11), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{cert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw})} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestManagedHTTPSWizardProducesValidRoleConfigurations(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("managed helpers require Linux")
	}
	binary, cert, key := managedWizardFixture(t)
	_, pub, err := managedRealityKey("")
	if err != nil {
		t.Fatal(err)
	}
	id := "6bf7a33e-7833-4e72-9219-506585657345"
	port := func() string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		return addrPort(ln.Addr().String())
	}
	for _, role := range []string{"server", "client"} {
		for _, chosen := range []string{"naive", "xhttp", "reality"} {
			t.Run(role+"/"+chosen, func(t *testing.T) {
				internal, outer, entry := port(), port(), port()
				s := TunnelSpec{Name: "https-menu-proof", Role: role, Transport: "tcp", Token: "shared-token", Ports: []string{"127.0.0.1:" + entry + "=127.0.0.1:8081"}}
				ApplyPreset(&s, PresetBalance)
				s.AcceptUDP = true
				s.FallbackAddrs = []string{"203.0.113.1:443"}
				s.Proxy = "socks5://127.0.0.1:1080"
				public := "127.0.0.1:" + outer
				var input []string
				if chosen == "naive" {
					input = []string{internal, binary, "user", "private-password"}
					if role == "server" {
						input = append(input, "1", cert, key)
					} else {
						input = append(input, cert)
					}
				} else if chosen == "xhttp" {
					input = []string{internal, id, "localhost", "/private-path", binary, ""}
					if role == "server" {
						input = append(input, "1", cert, key)
					} else {
						input = append(input, cert)
					}
				} else {
					input = []string{internal, id, "cover.example.com", "0123456789abcdef", binary}
					if role == "server" {
						input = append(input, "cover.example.com:443", "")
					} else {
						input = append(input, pub)
					}
				}
				restore := tui.SetInput(strings.NewReader(strings.Join(input, "\n") + "\n"))
				var ok bool
				out := capture(t, func() { ok = setupManagedCarrier(&s, chosen, public, "localhost") })
				restore()
				if !ok {
					t.Fatalf("wizard rejected valid answers:\n%s", out)
				}
				if s.Transport != "tcp" || selectedTransport(s) != chosen || managedEndpoint(s) != public || s.AcceptUDP || len(s.FallbackAddrs) != 0 || s.Proxy != "" {
					t.Fatalf("wizard left incompatible or incorrect settings: %s", s.Render())
				}
				var cfg config.Config
				if _, err := toml.Decode(s.Render(), &cfg); err != nil {
					t.Fatal(err)
				}
				if role == "server" && cfg.Server.BindAddr != "127.0.0.1:"+internal || role == "client" && cfg.Client.RemoteAddr != "127.0.0.1:"+internal {
					t.Fatal("internal target was not kept private")
				}
				if err := validateManagedSpec(s); err != nil {
					t.Fatal(err)
				}
				raw := pendingReverseLink(s, "localhost", linkExtras{})
				if role == "server" {
					link, err := DecodeShareLink(raw)
					if err != nil || link.Tr != chosen || link.Port != outer || link.InnerPort != internal {
						t.Fatalf("managed setup link: %v %+v", err, link)
					}
					peer, err := kharejFromLink(link, LinkApplyOptions{})
					if err != nil || peer.RemoteAddr != "127.0.0.1:"+internal || managedEndpoint(peer) != "localhost:"+outer || selectedTransport(peer) != chosen {
						t.Fatalf("managed peer: %v %s", err, peer.Render())
					}
					_, expectedPublic, _ := managedRealityKey(s.XrayServer.PrivateKey)
					if peer.XrayClient.PublicKey != expectedPublic && chosen == "reality" {
						t.Fatal("REALITY public key changed")
					}
					if link.HelperCA == "" && chosen != "reality" {
						t.Fatal("private certificate trust was omitted")
					}
					if link.HelperPublicKey == s.XrayServer.PrivateKey && chosen == "reality" || peer.XrayServer.PrivateKey != "" || strings.Contains(link.HelperCA, "PRIVATE KEY") {
						t.Fatal("private server key escaped")
					}
				} else if raw != "" {
					t.Fatal("Kharej cannot generate Iran private settings")
				}
				summary := capture(t, func() { summariseReverse(s, "localhost", "") })
				if !strings.Contains(summary, public) || !strings.Contains(summary, transportLabel(chosen)) || strings.Contains(summary, "private-password") || s.XrayServer.PrivateKey != "" && strings.Contains(summary, s.XrayServer.PrivateKey) {
					t.Fatalf("incorrect or secret-bearing summary:\n%s", summary)
				}
			})
		}
	}
}

func TestManagedHTTPSRefusalKeepsTheCurrentSpec(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux helper wizard")
	}
	for _, input := range []string{"443\n", "3080\n/nonexistent/helper\nuser\npassword\n\n"} {
		s := TunnelSpec{Name: "https-menu-refusal", Role: "client", Transport: "wss", RemoteAddr: "203.0.113.1:443", Token: "keep-token", FallbackAddrs: []string{"203.0.113.2:443"}}
		before := s
		restore := tui.SetInput(strings.NewReader(input))
		var ok bool
		capture(t, func() { ok = setupManagedCarrier(&s, "naive", "203.0.113.1:443", "203.0.113.1") })
		restore()
		if ok || !reflect.DeepEqual(before, s) {
			t.Fatal("invalid HTTPS answers changed the existing spec")
		}
	}
}

func TestManagedHTTPSClientWizardCanBeCancelledBeforeSaving(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux helper wizard")
	}
	binary, cert, _ := managedWizardFixture(t)
	name := "https-menu-cancel-" + randomToken(10)
	input := []string{"4", "2", "2", "127.0.0.1", "8443", name, "shared-token", "3080", "6bf7a33e-7833-4e72-9219-506585657345", "localhost", "/private-path", binary, "", cert, "1", "n", "n"}
	restore := tui.SetInput(strings.NewReader(strings.Join(input, "\n") + "\n"))
	defer restore()
	out := capture(t, SetupClient)
	if !strings.Contains(out, "Reverse XHTTP / TLS (Kharej)") || !strings.Contains(out, "127.0.0.1:8443") || strings.Contains(out, "Optional Connection Settings") || !strings.Contains(out, "How Do You Want To Set Up This Side?") {
		t.Fatalf("incorrect managed wizard flow:\n%s", out)
	}
	if _, err := os.Stat("/etc/backpack/" + name + ".toml"); !os.IsNotExist(err) {
		t.Fatal("cancelled wizard wrote a tunnel config")
	}
}

func TestManagedHTTPSInputLossDoesNotReplaceTheSpec(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux helper wizard")
	}
	binary, _, _ := managedWizardFixture(t)
	_, pub, err := managedRealityKey("")
	if err != nil {
		t.Fatal(err)
	}
	s := TunnelSpec{Name: "https-input-loss", Role: "client", Transport: "tcp", RemoteAddr: "127.0.0.1:3080"}
	s.XrayClient = config.XrayClientConfig{Binary: binary, Mode: "reality", Server: "203.0.113.1:443", UUID: "6bf7a33e-7833-4e72-9219-506585657345", ServerName: "cover.example.com", ShortID: "0123456789abcdef", PublicKey: pub}
	before := s
	// All defaults would be valid, but input ends before the last answer.
	restore := tui.SetInput(strings.NewReader("\n\n\n\n\n"))
	defer restore()
	stopped := make(chan struct{})
	defer tui.OnInputEnd(func() { close(stopped); runtime.Goexit() })()
	go func() { setupManagedCarrier(&s, "reality", s.XrayClient.Server, "cover.example.com") }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("wizard did not stop after input loss")
	}
	if !reflect.DeepEqual(before, s) {
		t.Fatal("input loss changed the managed spec")
	}
}

// The link the Iran summary shows, before anything is written, builds the
// kharej that matches it: same transport, token and port, the Iran address to
// dial, and every paired setting as the Iran server has it — not as the
// kharej's own preset would have it.
func TestTheReverseLinkBuildsTheMatchingKharej(t *testing.T) {
	for _, tr := range []string{"tcp", "tcpmux", "stealth", "pck", "udp", "kcp", "quic", "ws", "wsmux", "wss", "wssmux"} {
		t.Run(tr, func(t *testing.T) {
			s := TunnelSpec{
				Role: "server", Transport: tr, Name: "germany",
				BindAddr: "0.0.0.0:443", Token: "the-token", Ports: []string{"443", "8080=2096"},
				AcceptUDP: true,
			}
			ApplyPreset(&s, PresetBalance)
			s.MSS = 1300
			if isMux(tr) {
				s.MuxVersion = 1
			}
			if tr == "kcp" {
				// Error correction turned off by hand: the kharej must not
				// switch it back on from its own preset.
				s.KCPDataShards, s.KCPParityShards = 0, 0
			}
			if needsTLS(tr) {
				s.SimpleAuth = true
				s.TLSCert, s.TLSKey = "/etc/backpack/c.pem", "/etc/backpack/k.pem"
			}

			raw := pendingReverseLink(s, "203.0.113.9", linkExtras{})
			if !strings.HasPrefix(raw, shareScheme) {
				t.Fatalf("no setup link before the tunnel exists: %q", raw)
			}
			link, err := DecodeShareLink(raw)
			if err != nil {
				t.Fatalf("the summary's link does not decode: %v", err)
			}
			k := reverseClientFromLink(link, link.Host)

			if k.Role != "client" || k.Transport != tr || k.Token != "the-token" ||
				k.RemoteAddr != "203.0.113.9:443" || k.Preset != PresetBalance || k.MSS != 1300 {
				t.Fatalf("the kharej built from the link does not match: %+v", k)
			}
			if want := "germany-kharej"; k.Name != want {
				t.Errorf("the kharej's suggested name is %q", k.Name)
			}
			if needsTLS(tr) && !k.SimpleAuth {
				t.Error("simple auth did not cross, and the server would refuse the kharej's proof")
			}
			if isMux(tr) && k.MuxVersion != 1 {
				t.Errorf("mux version %d, want the server's 1", k.MuxVersion)
			}
			if tr == "kcp" && (k.KCPDataShards != 0 || k.KCPParityShards != 0) {
				t.Errorf("kcp error correction %d/%d on the kharej, off on Iran", k.KCPDataShards, k.KCPParityShards)
			}
		})
	}
}

// The Iran default name "server-443" suggests the kharej's own default,
// "client-443", not "server-443-kharej".
func TestTheKharejFromAServerNamedLinkIsAClient(t *testing.T) {
	s := TunnelSpec{Role: "server", Transport: "tcp", Name: "server-443", BindAddr: "0.0.0.0:443",
		Token: "t", Ports: []string{"443"}}
	ApplyPreset(&s, PresetTurbo)
	link, err := DecodeShareLink(pendingReverseLink(s, "203.0.113.9", linkExtras{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := reverseClientFromLink(link, link.Host).Name; got != "client-443" {
		t.Fatalf("suggested kharej name %q, want client-443", got)
	}
}

// The Iran summary is one short screen with the link under it.
func TestTheReverseSummaryIsShortAndCarriesTheLink(t *testing.T) {
	s := TunnelSpec{
		Role: "server", Transport: "tcp", Name: "germany", BindAddr: "0.0.0.0:443",
		Token: "the-token", Ports: []string{"443", "8080=2096"}, AcceptUDP: true,
	}
	ApplyPreset(&s, PresetTurbo)
	link := pendingReverseLink(s, "203.0.113.9", linkExtras{})
	out := capture(t, func() { summariseReverse(s, "203.0.113.9", link) })
	for _, want := range []string{
		"Reverse TCP", "0.0.0.0:443", "203.0.113.9:443", "443, 8080=2096  (TCP + UDP)",
		"443 → 127.0.0.1:443", "8080 → 127.0.0.1:2096", "Turbo",
		"Setup Link (sudo backpack → Setup Kharej → Reverse → Setup Link):",
		"Install BackPack And Set Up This Tunnel (Kharej Without BackPack, As Root):",
		"install.sh) link apply 'backpack://", link,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the reverse summary is missing %q:\n%s", want, out)
		}
	}
}

// The Iran wizard asks in the operator's order, and the kharej offers the
// link before anything else.
func TestTheReverseWizardOrder(t *testing.T) {
	src, err := os.ReadFile("reversesetup.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(src)
	for _, tc := range []struct {
		fn    string
		steps []string
	}{
		{"func SetupServer", []string{
			"chooseTransport()",
			`"Iran IP Or Domain (What Kharej Dials)"`,
			`tui.Prompt("Tunnel Port: ")`,
			`"Forwarded Ports (e.g. 443, 8080=127.0.0.1:2096): "`,
			`tui.PromptDefault("Tunnel Name"`,
			`tui.PromptDefault("Security Token", randomToken(64))`,
			`tui.Confirm("Carry UDP As Well As TCP On Those Ports"`,
			"setupServerTLS(&s)",
			"askPck(&s)",
			"askProxyProtocol(&s)",
			"choosePreset(s.Transport)",
			`tui.Confirm("Fine-Tune The Advanced Settings"`,
			"summariseReverse(s, host, link)",
			`tui.Confirm("Create This Tunnel"`,
		}},
		{"func setupClientFromLink", []string{
			`tui.Prompt("Setup Link: ")`,
			`tui.PromptDefault("Tunnel Name"`,
			`"Edge IP (Optional, For A CDN)"`,
			"askPck(&s)",
			"askConnectionOptions(&s, link.Port)",
			`tui.Confirm("Create This Tunnel"`,
		}},
		{"func SetupClient", []string{
			"chooseTransport()",
			`"How Do You Want To Set Up This Side?"`,
			`tui.Prompt("Iran IP Or Domain: ")`,
			`tui.Prompt("Tunnel Port: ")`,
			`tui.PromptDefault("Tunnel Name"`,
			`tui.Prompt("Security Token (From The Iran Server): ")`,
			"askConnectionOptions(&s, remotePort)",
			"choosePreset(s.Transport)",
			`tui.Confirm("Create This Tunnel"`,
		}},
	} {
		fn := body[strings.Index(body, tc.fn):]
		fn = fn[:strings.Index(fn, "\n}\n")]
		at := -1
		for _, step := range tc.steps {
			idx := strings.Index(fn, step)
			if idx < 0 {
				t.Fatalf("%s: step %q is missing", tc.fn, step)
			}
			if idx < at {
				t.Fatalf("%s: %q is asked out of order", tc.fn, step)
			}
			at = idx
		}
	}
}

// A forwarded port something here already holds, or the tunnel's own port, is
// refused before anything is written — the direct wizard's rule, which the
// reverse one did not have.
func TestTheReverseWizardRefusesAPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, held, _ := net.SplitHostPort(ln.Addr().String())

	free, _ := net.Listen("tcp", "127.0.0.1:0")
	_, open, _ := net.SplitHostPort(free.Addr().String())
	free.Close()

	got := reverseBusyPorts([]string{"127.0.0.1:" + held + "=2096", "127.0.0.1:" + open + "=2097"},
		"0.0.0.0:9000", "tcp")
	if len(got) != 1 || got[0] != held {
		t.Fatalf("busy = %v, want only the held port %s", got, held)
	}

	for _, tc := range []struct {
		transport string
		want      bool
	}{{"tcp", true}, {"wss", true}, {"kcp", false}, {"quic", false}} {
		got := reverseBusyPorts([]string{"9000=127.0.0.1:2096"}, "0.0.0.0:9000", tc.transport)
		if (len(got) == 1) != tc.want {
			t.Errorf("%s: forwarding the tunnel's own port 9000 gave %v", tc.transport, got)
		}
	}

	src, _ := os.ReadFile("reversesetup.go")
	if !strings.Contains(string(src), "reverseBusyPorts(s.Ports, s.BindAddr, s.Transport)") {
		t.Error("the reverse wizard never asks whether its ports are free")
	}
}

// A name the wizard refuses is asked for again — and once the input is gone
// (an SSH session that dropped) every answer is the same empty string, so the
// question used to repeat for ever. It stops instead.
func TestAWizardStopsWhenTheInputEnds(t *testing.T) {
	restore := tui.SetInput(strings.NewReader(""))
	defer restore()
	stopped := make(chan struct{})
	defer tui.OnInputEnd(func() { close(stopped); runtime.Goexit() })()

	go uniqueName("not a valid name!")
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the name question repeats for ever once the input is gone")
	}
}

// "Setup from a link" (the Manage menu, and the panel's paste box) builds the
// kharej through PeerForm rather than through the wizard. It dropped what
// v1.8.4 added to the link — simple auth, the smux version, kcp's exact error
// correction — and when the link carried an MSS it sent a Fine Tune drawer
// built in code, whose every zero overwrote the preset: heartbeat off, Nagle
// on, kcp FEC off. The kharej came up unable to talk to its server.
func TestSetupFromALinkKeepsThePairedReverseSettings(t *testing.T) {
	for _, tr := range []string{"wss", "wssmux", "kcp"} {
		t.Run(tr, func(t *testing.T) {
			srv := TunnelSpec{Role: "server", Transport: tr, Name: "germany",
				BindAddr: "0.0.0.0:443", Token: "tok", Ports: []string{"443"}}
			ApplyPreset(&srv, PresetBalance)
			srv.MSS = 1300
			if isMux(tr) {
				srv.MuxVersion = 1
			}
			if needsTLS(tr) {
				srv.SimpleAuth = true
				srv.TLSCert, srv.TLSKey = "/etc/backpack/c.pem", "/etc/backpack/k.pem"
			}
			if tr == "kcp" {
				srv.KCPDataShards, srv.KCPParityShards = 0, 0
			}
			link, err := DecodeShareLink(pendingReverseLink(srv, "203.0.113.9", linkExtras{}))
			if err != nil {
				t.Fatal(err)
			}
			n := MirrorForPeer(link).ToNewTunnel()

			want := reverseClientFromLink(link, link.Host) // what the wizard builds
			got := TunnelSpec{Role: "client", Transport: n.Transport}
			ApplyPreset(&got, n.Preset)
			if n.Tune != nil {
				n.Tune.apply(&got)
			}
			if n.Conn != nil {
				got.SimpleAuth = n.Conn.SimpleAuth && needsTLS(got.Transport)
			}
			if got.SimpleAuth != want.SimpleAuth || got.MuxVersion != want.MuxVersion ||
				got.MSS != want.MSS || got.Heartbeat != want.Heartbeat || got.Nodelay != want.Nodelay ||
				got.KCPDataShards != want.KCPDataShards || got.KCPParityShards != want.KCPParityShards {
				t.Fatalf("from a link: auth %v mux %d mss %d hb %d nodelay %v fec %d/%d\n"+
					"the wizard:  auth %v mux %d mss %d hb %d nodelay %v fec %d/%d",
					got.SimpleAuth, got.MuxVersion, got.MSS, got.Heartbeat, got.Nodelay, got.KCPDataShards, got.KCPParityShards,
					want.SimpleAuth, want.MuxVersion, want.MSS, want.Heartbeat, want.Nodelay, want.KCPDataShards, want.KCPParityShards)
			}
		})
	}
}

// The fleet sends that form to the managed server as JSON. A drawer that
// crosses the wire must still say which keys it answers, or the far end reads
// every zero as an answer again.
func TestAFineTuneDrawerCrossesTheWireWithOnlyItsAnswers(t *testing.T) {
	in := FineTune{MSS: 1300, sent: map[string]bool{"mss": true}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out FineTune
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	s := TunnelSpec{Role: "client", Transport: "wss"}
	ApplyPreset(&s, PresetBalance)
	hb, nd := s.Heartbeat, s.Nodelay
	out.apply(&s)
	if s.MSS != 1300 || s.Heartbeat != hb || s.Nodelay != nd {
		t.Fatalf("after the wire: mss %d heartbeat %d (want %d) nodelay %v (want %v); sent %s",
			s.MSS, s.Heartbeat, hb, s.Nodelay, nd, raw)
	}
	// A drawer built with no marks still means every field, as before.
	if full, _ := json.Marshal(FineTune{}); !strings.Contains(string(full), `"heartbeat"`) {
		t.Errorf("an unmarked drawer lost its fields on the wire: %s", full)
	}
}

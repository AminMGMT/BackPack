package manage

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A kharej built from the Iran server's setup link, run for real.
//
// Reported on v1.8.4: a reverse tunnel set up with the setup link did not work,
// and the same tunnel set up by hand did. The unit tests of the link compare
// fields; this compares tunnels. The Iran side is built as the wizard builds
// it, its link is made as the wizard makes it, the kharej is built from that
// link both ways a kharej can be — the wizard's Setup Link answer and Manage →
// Set up from a link (the panel's paste box and a managed far end use the same
// form) — and the path `backpack link apply` and the one-line install take —
// and then both ends run as the real binary, with the defaults and the
// checks a real start applies, and bytes have to cross.
func TestAKharejMadeFromTheSetupLinkCarriesTraffic(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the engine; skipped under -short")
	}
	bin := filepath.Join(t.TempDir(), "backpack")
	build := exec.Command("go", "build", "-o", bin, "../..")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building the engine: %v", err)
	}
	cert, key := testCertPair(t)

	transports := []string{"tcp", "tcpmux", "ws", "wss", "wsmux", "wssmux", "kcp", "quic", "udp"}
	for _, tr := range transports {
		presets := []string{PresetBalance, PresetTurbo, PresetAggressive}
		if tr == "kcp" {
			presets = append(presets, PresetThroughput) // offered for kcp only
		}
		variants := map[string]func(*TunnelSpec){}
		for _, p := range presets {
			p := p
			variants[p] = func(s *TunnelSpec) { ApplyPreset(s, p) }
		}
		// What the Iran operator can answer other than the default. Fine-Tune
		// clears the preset (the link then names none), so each of those is a
		// kharej that has to manage without the Iran side's numbers.
		fine := func(change func(*TunnelSpec)) func(*TunnelSpec) {
			return func(s *TunnelSpec) {
				ApplyPreset(s, PresetTurbo)
				change(s)
				s.Preset = ""
			}
		}
		variants["fine-tuned, heartbeat off"] = fine(func(s *TunnelSpec) { s.Heartbeat = 0 })
		variants["fine-tuned, slow heartbeat"] = fine(func(s *TunnelSpec) { s.Heartbeat = 30 })
		variants["fine-tuned, short keepalive"] = fine(func(s *TunnelSpec) { s.KeepAlive = 5 })
		variants["fine-tuned, small channel"] = fine(func(s *TunnelSpec) { s.ChannelSize = 16 })
		variants["udp as well"] = func(s *TunnelSpec) { ApplyPreset(s, PresetTurbo); s.AcceptUDP = tr != "udp" }
		if isMuxTransport(tr) {
			variants["fine-tuned, mux v1"] = fine(func(s *TunnelSpec) { s.MuxVersion = 1 })
			variants["fine-tuned, mux buffers"] = fine(func(s *TunnelSpec) {
				s.MuxFrameSize, s.MuxRecvBuffer, s.MuxStreamBuffer = 16384, 1<<20, 1<<18
			})
		}
		if tr == "kcp" {
			variants["fine-tuned, kcp"] = fine(func(s *TunnelSpec) {
				s.KCPMTU, s.KCPInterval, s.KCPSndWnd, s.KCPRcvWnd = 1200, 40, 256, 256
				s.KCPDataShards, s.KCPParityShards = 0, 0
			})
		}
		if needsTLS(tr) {
			variants["simple auth"] = func(s *TunnelSpec) { ApplyPreset(s, PresetTurbo); s.SimpleAuth = true }
		}
		for name, variant := range variants {
			for _, path := range []string{"wizard", "set up from a link", "link apply"} {
				tr, name, variant, path := tr, name, variant, path
				t.Run(tr+"/"+name+"/"+path, func(t *testing.T) {
					t.Parallel()
					// Hundreds of engines pick free ports at once, and now and
					// then two pick the same one. That is this test's collision,
					// not the tunnel's, and it is retried with new ports.
					for attempt := 1; ; attempt++ {
						msg, clash := runLinkedPair(t, bin, tr, variant, path, cert, key)
						if msg == "" {
							return
						}
						if !clash || attempt == 3 {
							t.Fatal(msg)
						}
					}
				})
			}
		}
	}
}

// isMuxTransport reports whether a transport carries smux, whose settings the
// Fine-Tune drawer offers.
func isMuxTransport(tr string) bool {
	return strings.HasSuffix(tr, "mux") || tr == "kcp"
}

// runLinkedPair builds and runs one pair. It returns what went wrong, if
// anything, and whether that was a port another test took first.
func runLinkedPair(t *testing.T, bin, transport string, variant func(*TunnelSpec), path, cert, key string) (failure string, portClash bool) {
	return runLinkedPairVia(t, bin, transport, variant, path, cert, key, "127.0.0.1", linkExtras{})
}

// runLinkedPairVia is runLinkedPair with the address the link names and what
// else it carries chosen by the caller.
func runLinkedPairVia(t *testing.T, bin, transport string, variant func(*TunnelSpec), path, cert, key, host string, extras linkExtras) (failure string, portClash bool) {
	dir := t.TempDir()
	tunnelPort, entryPort := freeTestPort(t), freeTestPort(t)
	backend := startTestEcho(t, transport == "udp")

	iran := TunnelSpec{
		Role:      "server",
		Transport: transport,
		Name:      fmt.Sprintf("server-%d", tunnelPort),
		BindAddr:  fmt.Sprintf("127.0.0.1:%d", tunnelPort),
		Ports:     []string{fmt.Sprintf("%d=%s", entryPort, backend)},
		Token:     randomToken(64),
	}
	if needsTLS(transport) {
		iran.TLSCert, iran.TLSKey = cert, key
	}
	variant(&iran)

	raw := pendingReverseLink(iran, host, extras)
	if raw == "" {
		t.Fatal("the Iran side produced no setup link")
	}
	link, err := DecodeShareLink(raw)
	if err != nil {
		t.Fatalf("the link does not decode: %v", err)
	}

	var kharej TunnelSpec
	switch path {
	case "wizard":
		kharej = reverseClientFromLink(link, strings.Trim(link.Host, "[]"))
	case "link apply":
		kharej, err = kharejFromLink(link, LinkApplyOptions{})
		if err != nil {
			t.Fatalf("link apply refused the link: %v", err)
		}
	default:
		kharej, err = specFromNew(MirrorForPeer(link).ToNewTunnel())
		if err != nil {
			t.Fatalf("the form from the link was refused: %v", err)
		}
	}

	srvFile, cliFile := filepath.Join(dir, "iran.toml"), filepath.Join(dir, "kharej.toml")
	writeTestFile(t, srvFile, iran.Render())
	writeTestFile(t, cliFile, kharej.Render())
	startTestEngine(t, bin, srvFile, filepath.Join(dir, "iran.log"))
	time.Sleep(500 * time.Millisecond)
	startTestEngine(t, bin, cliFile, filepath.Join(dir, "kharej.log"))

	entry := fmt.Sprintf("127.0.0.1:%d", entryPort)
	clashed := func() bool {
		srvLog, _ := os.ReadFile(filepath.Join(dir, "iran.log"))
		return strings.Contains(string(srvLog), "already listening") || strings.Contains(string(srvLog), "address already in use")
	}
	if err := awaitTestEcho(entry, transport == "udp", 20*time.Second); err != nil {
		srvLog, _ := os.ReadFile(filepath.Join(dir, "iran.log"))
		cliLog, _ := os.ReadFile(filepath.Join(dir, "kharej.log"))
		return fmt.Sprintf("the tunnel never carried traffic: %v\n--- iran config\n%s\n--- kharej config\n%s\n--- iran log\n%s\n--- kharej log\n%s",
			err, iran.Render(), kharej.Render(), tail(srvLog), tail(cliLog)), clashed()
	}
	// Up is not the same as staying up: a kharej whose idea of the heartbeat or
	// the keepalive differs from the server's comes up and then drops.
	time.Sleep(25 * time.Second)
	if err := awaitTestEcho(entry, transport == "udp", 3*time.Second); err != nil {
		srvLog, _ := os.ReadFile(filepath.Join(dir, "iran.log"))
		cliLog, _ := os.ReadFile(filepath.Join(dir, "kharej.log"))
		return fmt.Sprintf("the tunnel came up and then stopped carrying traffic: %v\n--- iran config\n%s\n--- kharej config\n%s\n--- iran log\n%s\n--- kharej log\n%s",
			err, iran.Render(), kharej.Render(), tail(srvLog), tail(cliLog)), clashed()
	}
	return "", false
}

func tail(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > 15 {
		lines = lines[len(lines)-15:]
	}
	return strings.Join(lines, "\n")
}

func freeTestPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	// The udp transport and kcp/quic need the same number free on UDP too.
	return l.Addr().(*net.TCPAddr).Port
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// startTestEcho is the service behind the forwarded port: TCP, or UDP for the
// udp transport, whose forwarded ports are datagram ports.
func startTestEcho(t *testing.T, udp bool) string {
	t.Helper()
	if udp {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pc.Close() })
		go func() {
			buf := make([]byte, 65535)
			for {
				n, from, err := pc.ReadFrom(buf)
				if err != nil {
					return
				}
				_, _ = pc.WriteTo(buf[:n], from)
			}
		}()
		return pc.LocalAddr().String()
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return l.Addr().String()
}

func startTestEngine(t *testing.T, bin, cfg, logPath string) {
	t.Helper()
	logf, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-c", cfg)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		logf.Close()
	})
}

// awaitTestEcho waits until a payload sent into the forwarded port comes back.
func awaitTestEcho(addr string, udp bool, within time.Duration) error {
	payload := make([]byte, 32<<10)
	if udp {
		payload = payload[:512]
	}
	_, _ = rand.Read(payload)
	network := "tcp"
	if udp {
		network = "udp"
	}
	deadline := time.Now().Add(within)
	var last error
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout(network, addr, 2*time.Second)
		if err != nil {
			last = err
			time.Sleep(250 * time.Millisecond)
			continue
		}
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err = c.Write(payload); err == nil {
			got := make([]byte, len(payload))
			if _, err = io.ReadFull(c, got); err == nil && string(got) == string(payload) {
				c.Close()
				return nil
			}
		}
		last = err
		c.Close()
		time.Sleep(250 * time.Millisecond)
	}
	return last
}

// testCertPair writes a self-signed certificate for the TLS transports.
func testCertPair(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	writeTestFile(t, certPath, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	writeTestFile(t, keyPath, string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})))
	return certPath, keyPath
}

// A link carries the Iran server's other addresses, and a kharej built from it
// fails over to them. Here the main address answers nothing — 127.0.0.254 is
// on the loopback, where no tunnel listens — and the backup is the real one:
// the tunnel has to come up through it.
func TestAKharejFromALinkFailsOverToTheLinksBackupAddress(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the engine; skipped under -short")
	}
	bin := filepath.Join(t.TempDir(), "backpack")
	build := exec.Command("go", "build", "-o", bin, "../..")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building the engine: %v", err)
	}
	cert, key := testCertPair(t)
	for _, tr := range []string{"tcp", "tcpmux", "ws", "wss", "wsmux", "kcp", "quic", "udp"} {
		tr := tr
		t.Run(tr, func(t *testing.T) {
			t.Parallel()
			turbo := func(s *TunnelSpec) { ApplyPreset(s, PresetTurbo) }
			for attempt := 1; ; attempt++ {
				msg, clash := runLinkedPairVia(t, bin, tr, turbo, "link apply", cert, key,
					"127.0.0.254", linkExtras{hosts: []string{"127.0.0.1"}})
				if msg == "" {
					return
				}
				if !clash || attempt == 3 {
					t.Fatal(msg)
				}
			}
		})
	}
}

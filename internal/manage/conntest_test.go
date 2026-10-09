package manage

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
)

// The link an operator copies is short: where the coordinator is and the
// secret, nothing else.
func TestATestLinkIsShortAndIsNotTakenForASetupLink(t *testing.T) {
	for _, host := range []string{"94.139.180.179", "iran.example.com"} {
		in := ConnTestLink{Host: host, Coord: 40123, Tok: ctNewSecret()}
		raw := in.Short()
		if host == "94.139.180.179" && len(raw) > 45 {
			t.Errorf("the link is %d characters: %s", len(raw), raw)
		}
		if !IsConnTestLink("here it is: " + raw + " — paste it on the kharej") {
			t.Errorf("%s: a test link inside a message was not recognised", raw)
		}
		a, err := parseConnTestLink(raw)
		if err != nil {
			t.Fatal(err)
		}
		if a.Host != host || a.Coord != 40123 || a.Tok != in.Tok {
			t.Errorf("round trip changed the link: %+v", a)
		}
		if _, err := DecodeShareLink(raw); err == nil || !strings.Contains(err.Error(), "connection-test link") {
			t.Errorf("pasted into Set up from a link, a test link gave %v; it should say what it is", err)
		}
	}

	setup, err := ShareLink{Kind: "reverse", From: "iran", Tok: "x", Tr: "tcp", Port: "443"}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if IsConnTestLink(setup) {
		t.Error("a setup link was taken for a test link")
	}
	if _, err := parseConnTestLink(setup); err == nil {
		t.Error("a setup link parsed as a test link")
	}
	if IsConnTestLink("backpack://t.AAAA") {
		t.Error("a cut-short test link was accepted")
	}
}

// What the kharej fetches has to fit in one packet — the path this was made
// for cuts a flow after a few — and carries no tunnel's token: the kharej
// derives each from the secret, and has to arrive at the Iran side's.
func TestTheTestSettingsFitOnePacketAndCarryNoToken(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the Iran side's engines")
	}
	defer func(prev func() (string, error)) { connTestBinary = prev }(connTestBinary)
	connTestBinary = func() (string, error) { return "/bin/true", nil }
	previousHelper := connTestHelperBinary
	connTestHelperBinary = func(string) (string, error) { return "/bin/true", nil }
	defer func() { connTestHelperBinary = previousHelper }()
	s, link, err := StartConnTestIran(ConnTestOptions{Host: "127.0.0.1", Direct: true, SpoofSrc: ConnTestSpoofSource, RealityTarget: "example.com:443"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reply := s.coord.answer("config "+s.link.Tok, "1.1.1.1")
	if !strings.HasPrefix(reply, "config ") {
		t.Fatalf("config: %q", reply)
	}
	if len(reply) > 1200 {
		t.Errorf("the settings are %d bytes; they have to fit one packet", len(reply))
	}
	for _, c := range s.link.Cases {
		if strings.Contains(reply, c.Tok) {
			t.Errorf("the %s token travels in the settings", c.Tr)
		}
		if got := ctCaseToken(s.link.Tok, c.Kind, c.Tr); got != c.Tok {
			t.Errorf("%s/%s: the kharej would derive %q, the Iran side has %q", c.Kind, c.Tr, got, c.Tok)
		}
		if managedTransport(c.Tr) {
			reply := s.coord.answer("carrier "+s.link.Tok+" "+c.Tr, "1.1.1.1")
			if !strings.HasPrefix(reply, "carrier ") || len(reply) > 1200 {
				t.Errorf("%s settings are missing or exceed a packet: %d bytes", c.Tr, len(reply))
			}
			spec, err := ctManagedClient(context.Background(), s.link, c, t.TempDir())
			if err != nil || selectedTransport(spec) != c.Tr || spec.Transport != "tcp" {
				t.Fatalf("%s client: %v, %s", c.Tr, err, selectedTransport(spec))
			}
			if spec.XrayServer.PrivateKey != "" || spec.NaiveServer.Key != "" {
				t.Error("private server settings escaped into the client")
			}
		}
	}
	a, _ := parseConnTestLink(link)
	if a.Tok != s.link.Tok {
		t.Error("the link does not carry the coordinator's secret")
	}
}

// The coordinator answers only a question carrying the token, and says
// nothing at all otherwise — it is a port open on the Iran server's public
// address for the length of the test.
func TestTheCoordinatorAnswersOnlyWithTheToken(t *testing.T) {
	c, err := startCTCoordinator(ctPickPort(map[int]bool{}, true), "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if got := c.answer("hello wrong", "1.1.1.1"); got != "" {
		t.Errorf("a wrong token was answered %q", got)
	}
	if got := c.answer("result secret", "1.1.1.1"); got != "wait" {
		t.Errorf("before the verdict: %q, want wait", got)
	}
	if got := c.answer("hello secret", "::ffff:5.6.7.8"); got != "ok" {
		t.Errorf("hello: %q", got)
	}
	select {
	case <-c.joined:
	default:
		t.Error("a hello did not mark the kharej as joined")
	}
	if c.peerAddr() != "5.6.7.8" {
		t.Errorf("the kharej's address is %q", c.peerAddr())
	}
	if got := c.answer("spoof wrong 42", "5.6.7.8"); got != "" {
		t.Errorf("a spoof count with the wrong token was answered %q", got)
	}
	if got := c.answer("spoof secret 42", "5.6.7.8"); got != "ok" {
		t.Errorf("spoof count: %q", got)
	}
	if got := c.answer("spoof secret 7", "5.6.7.8"); got != "ok" {
		t.Errorf("a repeated spoof count: %q", got)
	}
	if n := <-c.spoofArrived; n != 42 {
		t.Errorf("the kharej's count arrived as %d; the first one, 42, is the count", n)
	}
	c.publish([]ConnTestResult{{Kind: "reverse", Transport: "tcp", Status: ctOK}}, ConnTestBest{})
	if got := c.answer("result secret", "5.6.7.8"); !strings.HasPrefix(got, "done ") {
		t.Errorf("after the verdict: %q", got)
	}
}

// The whole test, both sides, over loopback with the real engine: every
// reverse transport has to come up and pass, and the kharej has to receive
// the same verdict the Iran side printed.
func TestAConnectionTestOverLoopbackPassesEveryReverseTransport(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the engine; skipped under -short")
	}
	bin := filepath.Join(t.TempDir(), "backpack")
	build := exec.Command("go", "build", "-o", bin, "../..")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building the engine: %v", err)
	}
	defer func(prev func() (string, error), soak int) {
		connTestBinary, connTestSoak = prev, soak
	}(connTestBinary, connTestSoak)
	connTestBinary = func() (string, error) { return bin, nil }
	if root := os.Getenv("BP_CONNTEST_HELPERS_ROOT"); root != "" {
		previous := connTestHelperBinary
		connTestHelperBinary = func(tool string) (string, error) {
			path := filepath.Join(root, tool)
			if _, err := os.Stat(path); err != nil {
				return "", err
			}
			return path, nil
		}
		defer func() { connTestHelperBinary = previous }()
	}
	connTestSoak = 8
	cover := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	cover.EnableHTTP2 = true
	cover.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	cover.StartTLS()
	defer cover.Close()
	_, coverPort, _ := net.SplitHostPort(cover.Listener.Addr().String())

	iran, link, err := StartConnTestIran(ConnTestOptions{Host: "127.0.0.1", RealityTarget: "localhost:" + coverPort})
	if err != nil {
		t.Fatal(err)
	}
	defer iran.Close()

	// Production Setup Links must build the same helper peer as Connection Test.
	for _, c := range iran.cases {
		if !managedTransport(c.tr) || c.skip != "" {
			continue
		}
		var cfg config.Config
		if _, err := toml.DecodeFile(filepath.Join(iran.dir, c.name+".toml"), &cfg); err != nil {
			t.Fatal(err)
		}
		raw, err := shareLinkOf(c.name, "127.0.0.1", cfg)
		if err != nil {
			t.Fatal(err)
		}
		paired, err := DecodeShareLink(raw)
		if err != nil {
			t.Fatal(err)
		}
		production, err := kharejFromLink(paired, LinkApplyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		testPeer, err := ctManagedClient(context.Background(), iran.link, c.link, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// The test's trust file is disposable; persistent setup uses its hash.
		production.NaiveClient.CAFile = testPeer.NaiveClient.CAFile
		production.XrayClient.CAFile = testPeer.XrayClient.CAFile
		// An isolated fixture changes only the local executable path; Setup
		// Links deliberately use the installed helper path on their own host.
		if os.Getenv("BP_CONNTEST_HELPERS_ROOT") != "" {
			production.NaiveClient.Binary = testPeer.NaiveClient.Binary
			production.XrayClient.Binary = testPeer.XrayClient.Binary
		}
		if production.NaiveClient != testPeer.NaiveClient || production.XrayClient != testPeer.XrayClient || production.RemoteAddr != testPeer.RemoteAddr || production.Token != testPeer.Token {
			t.Fatalf("%s production Setup Link differs from the tested helper peer", c.tr)
		}
		if err := validateManagedSpec(production); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var kharej []ConnTestResult
	var kharejErr error
	var out bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		kharej, _, kharejErr = RunConnTestKharej(ctx, link, &out, nil)
	}()

	select {
	case <-iran.Joined():
	case <-ctx.Done():
		t.Fatal("the kharej never checked in")
	}
	results := iran.Run(ctx, nil)
	wg.Wait()

	if kharejErr != nil {
		t.Fatalf("the kharej side: %v\n%s", kharejErr, out.String())
	}
	if len(results) != len(connTestReverse) {
		t.Fatalf("%d results for %d transports", len(results), len(connTestReverse))
	}
	for _, r := range results {
		if r.Status == ctSkipped && managedTransport(r.Transport) && os.Getenv("BP_CONNTEST_HELPERS") != "1" {
			t.Logf("%s skipped: %s", r.Transport, r.Detail)
			continue
		}
		if r.Status == ctSkipped && connTestNeedsRoot(r.Transport) && os.Geteuid() != 0 {
			continue
		}
		if r.Status != ctOK {
			t.Errorf("%s %s: %s (%s) — %d/%d echoes", r.Kind, r.Transport, r.Status, r.Detail, r.OK, r.Tried)
		}
	}
	if len(kharej) != len(results) {
		t.Errorf("the kharej received %d results, the Iran side had %d", len(kharej), len(results))
	}
	t.Log("\n" + ConnTestTable(results))
}

func TestConnTestUnavailableHelpersAreSkipped(t *testing.T) {
	previous := connTestHelperBinary
	connTestHelperBinary = func(tool string) (string, error) { return "", fmt.Errorf("%s is unavailable", tool) }
	defer func() { connTestHelperBinary = previous }()
	s := &ConnTestIran{}
	for _, tr := range []string{"naive", "xhttp", "reality"} {
		c := &connTestCase{kind: "reverse", tr: tr}
		s.startManagedCase(c, map[int]bool{}, ConnTestOptions{})
		if !strings.Contains(c.skip, "unavailable") {
			t.Errorf("%s did not explain the missing helper: %q", tr, c.skip)
		}
	}
}

func TestConnTestCancellationDuringStartupCleansEarlierEngines(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	previous, helper := connTestBinary, connTestHelperBinary
	connTestBinary = func() (string, error) { return "/bin/true", nil }
	connTestHelperBinary = func(string) (string, error) { cancel(); return "", errors.New("unavailable") }
	defer func() { connTestBinary, connTestHelperBinary = previous, helper }()
	s, link, err := StartConnTestIran(ConnTestOptions{Context: ctx, Host: "127.0.0.1"})
	if s != nil || link != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled startup returned a live test: %v %q %v", s, link, err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("cancelled startup left temporary files: %v %v", files, err)
	}
}

func TestConnTestCancellationCannotPassAPartialSoak(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	echoes := &ctEchoes{}
	echoes.add(l)
	defer echoes.close()
	go ctServeTCPEcho(l, echoes)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &connTestCase{kind: "reverse", tr: "tcp", entry: l.Addr().(*net.TCPAddr).Port}
	r := ctProbe(ctx, c, 0, func(r ConnTestResult) {
		if r.OK == 1 {
			cancel()
		}
	})
	if r.Status != ctUnstable || r.Tried != 1 || !strings.Contains(r.Detail, "stopped") {
		t.Fatalf("partial soak incorrectly passed: %+v", r)
	}
}

func TestConnTestBulkRejectsCorruptionAndStopsOnCancellation(t *testing.T) {
	t.Run("corruption", func(t *testing.T) {
		client, peer := net.Pipe()
		defer peer.Close()
		go func() {
			buf := make([]byte, connTestBulk)
			if _, err := io.ReadFull(peer, buf); err == nil {
				buf[len(buf)/2] ^= 1
				_, _ = peer.Write(buf)
			}
		}()
		if speed, err := ctBulk(func() (net.Conn, error) { return client, nil }); err == nil || speed != 0 {
			t.Fatalf("corrupt transfer returned %f, %v", speed, err)
		}
	})
	t.Run("cancelled writer", func(t *testing.T) {
		client, peer := net.Pipe()
		defer peer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		if _, err := ctBulkContext(ctx, func() (net.Conn, error) { return client, nil }); err == nil {
			t.Fatal("blocked transfer passed")
		}
		if time.Since(start) > time.Second {
			t.Fatal("cancellation waited for the transfer timeout")
		}
	})
}

type ctShortWriter struct{ net.Conn }

func (c ctShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestConnTestEchoRejectsShortWrites(t *testing.T) {
	c, peer := net.Pipe()
	defer c.Close()
	defer peer.Close()
	if err := ctEcho(ctShortWriter{c}, false); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write: %v", err)
	}
}

func TestConnTestEchoCloseOwnsActiveAndLateSockets(t *testing.T) {
	e := &ctEchoes{}
	a, peer := net.Pipe()
	defer peer.Close()
	e.add(a)
	e.close()
	if _, err := peer.Write([]byte("x")); err == nil {
		t.Fatal("active socket remained open")
	}
	late, other := net.Pipe()
	defer other.Close()
	e.add(late)
	if _, err := other.Write([]byte("x")); err == nil {
		t.Fatal("late socket remained open")
	}
}

func TestConnTestCoordinatorCancellationAndManagedSkip(t *testing.T) {
	c, err := startCTCoordinator(ctPickPort(map[int]bool{}, true), "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if c.answer("skip wrong naive", "127.0.0.1") != "" || c.answer("carrier wrong naive", "127.0.0.1") != "" {
		t.Fatal("managed commands accepted the wrong token")
	}
	if c.answer("skip secret naive", "127.0.0.1") != "ok" || !c.skipped["naive"] {
		t.Fatal("managed unavailability was not recorded")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		peer, err := l.Accept()
		if err == nil {
			defer peer.Close()
			_, _ = io.Copy(io.Discard, peer)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := ctAskContext(ctx, "127.0.0.1", l.Addr().(*net.TCPAddr).Port, "hello x"); err == nil {
		t.Fatal("silent coordinator answered")
	}
	if time.Since(start) > time.Second {
		t.Fatal("coordinator ignored cancellation")
	}
	<-done
}

func TestConnTestCoordinatorCloseReleasesPartialRequests(t *testing.T) {
	c, err := startCTCoordinator(ctPickPort(map[int]bool{}, true), "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	peer, err := net.Dial("tcp", c.tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	_, _ = peer.Write([]byte("hello"))
	c.close()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	_, err = peer.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("partial request survived coordinator shutdown")
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("coordinator left a request waiting for its timeout")
	}
}

func TestConnTestStopsAllUnresponsiveEnginesWithinOneDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real shell processes")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "stubborn")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntrap '' TERM\necho ready\nwhile :; do sleep 30; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	previous, wait := connTestBinary, connTestStopWait
	connTestBinary = func() (string, error) { return bin, nil }
	connTestStopWait = 100 * time.Millisecond
	defer func() { connTestBinary, connTestStopWait = previous, wait }()
	var engines []*ctEngine
	defer func() { ctStopAll(engines) }()
	for i := 0; i < 3; i++ {
		e, err := startCTEngine(dir, fmt.Sprintf("stubborn-%d", i), "")
		if err != nil {
			t.Fatal(err)
		}
		engines = append(engines, e)
		until := time.Now().Add(time.Second)
		for ctLastLine(e.log) != "ready" && time.Now().Before(until) {
			time.Sleep(10 * time.Millisecond)
		}
		if ctLastLine(e.log) != "ready" {
			t.Fatal("stubborn engine did not initialize")
		}
	}
	start := time.Now()
	ctStopAll(engines)
	if time.Since(start) > time.Second {
		t.Fatal("cleanup waited per engine instead of sharing the deadline")
	}
	engines = nil
}

func TestConnTestRealityCoverVerifiesTLSAndHTTP2(t *testing.T) {
	for _, tc := range []struct {
		name              string
		version           uint16
		h2, trusted, pass bool
	}{
		{"compatible", tls.VersionTLS13, true, true, true},
		{"TLS12", tls.VersionTLS12, true, true, false},
		{"HTTP1", tls.VersionTLS13, false, true, false},
		{"untrusted", tls.VersionTLS13, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
			server.EnableHTTP2 = tc.h2
			server.TLS = &tls.Config{MinVersion: tc.version, MaxVersion: tc.version}
			server.StartTLS()
			defer server.Close()
			var roots *x509.CertPool
			if tc.trusted {
				roots = x509.NewCertPool()
				roots.AddCert(server.Certificate())
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := ctProbeRealityCover(ctx, server.Listener.Addr().String(), roots)
			if (err == nil) != tc.pass {
				t.Fatalf("compatible=%v, error=%v", tc.pass, err)
			}
		})
	}
}

func TestConnTestRealityCoverFallbackAndCancellation(t *testing.T) {
	previousWait := connTestCoverWait
	connTestCoverWait = 100 * time.Millisecond
	defer func() { connTestCoverWait = previousWait }()
	tried := []string{}
	target, err := ctFindRealityCover(context.Background(), []string{"blocked", "compatible", "unused"}, func(ctx context.Context, target string) error {
		tried = append(tried, target)
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("probe has no deadline")
		}
		if target == "blocked" {
			return errors.New("blocked")
		}
		return nil
	})
	if err != nil || target != "compatible" || strings.Join(tried, ",") != "blocked,compatible" {
		t.Fatalf("selection: %q %v %v", target, tried, err)
	}
	if target, err := ctFindRealityCover(context.Background(), []string{"blocked"}, func(context.Context, string) error { return errors.New("blocked") }); err == nil || target != "" {
		t.Fatal("unreachable cover was selected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	target, err = ctFindRealityCover(ctx, []string{"cancelled", "unused"}, func(context.Context, string) error { cancel(); return nil })
	if target != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled selection succeeded: %q %v", target, err)
	}
	if target, err := ctFindRealityCover(context.Background(), []string{"expired"}, func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return nil
	}); err == nil || target != "" {
		t.Fatal("expired cover probe was accepted")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			_, _ = io.Copy(io.Discard, c)
		}
	}()
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := ctProbeRealityCover(ctx, ln.Addr().String(), nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled handshake: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled probe left its connection open")
	}
}

// The real pinned helper must reject a cover that ordinary TLS accepts, fall
// back to a working cover and release every process and socket on cancellation.
func TestRealityCoverSelectionAuthenticatesWithThePinnedHelper(t *testing.T) {
	binary, err := connTestHelperBinary("xray")
	if override := os.Getenv("BP_REALITY_TEST_BINARY"); override != "" {
		binary, err = override, nil
	}
	if err != nil {
		if os.Getenv("BP_CONNTEST_HELPERS") == "1" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	small := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	small.EnableHTTP2 = true
	small.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	small.StartTLS()
	defer small.Close()
	pair := small.TLS.Certificates[0]
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	leaf.ExtraExtensions = []pkix.Extension{{Id: []int{1, 3, 6, 1, 4, 1, 55555, 1}, Value: make([]byte, 9000)}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, pair.PrivateKey.(crypto.Signer).Public(), pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	pair.Certificate = [][]byte{der}
	large := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	large.EnableHTTP2 = true
	large.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}
	large.StartTLS()
	defer large.Close()
	roots := x509.NewCertPool()
	largeLeaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(largeLeaf)
	roots.AddCert(small.Certificate())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := ctProbeRealityCover(ctx, large.Listener.Addr().String(), roots); err != nil {
		t.Fatalf("large cover must pass ordinary TLS verification: %v", err)
	}
	target := func(s *httptest.Server) string {
		_, port, _ := net.SplitHostPort(s.Listener.Addr().String())
		return net.JoinHostPort("localhost", port)
	}
	previousWait := connTestCoverWait
	connTestCoverWait = 3 * time.Second
	defer func() { connTestCoverWait = previousWait }()
	selected, err := ctFindRealityCover(ctx, []string{target(large), target(small)}, func(ctx context.Context, target string) error {
		return ctProbeRealityTransport(ctx, target, binary)
	})
	if err != nil || selected != target(small) {
		t.Fatalf("TLS-compatible but REALITY-incompatible cover was selected: %q %v", selected, err)
	}
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		peer, err := silent.Accept()
		if err != nil {
			return
		}
		defer peer.Close()
		_, _ = io.Copy(io.Discard, peer)
	}()
	attempt, stop := context.WithTimeout(ctx, 750*time.Millisecond)
	defer stop()
	start := time.Now()
	_, port, _ := net.SplitHostPort(silent.Addr().String())
	if err := ctProbeRealityTransport(attempt, "localhost:"+port, binary); err == nil {
		t.Fatal("silent cover passed authenticated probe")
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("cancelled helper probe waited beyond shutdown budget")
	}
	silent.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled helper probe retained its cover connection")
	}
}

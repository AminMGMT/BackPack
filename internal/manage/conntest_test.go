package manage

import (
	"bytes"
	"context"
	"crypto/tls"
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

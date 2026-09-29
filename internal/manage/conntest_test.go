package manage

import (
	"bytes"
	"context"
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
	s, link, err := StartConnTestIran(ConnTestOptions{Host: "94.139.180.179", Direct: true, SpoofSrc: ConnTestSpoofSource})
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

	iran, link, err := StartConnTestIran(ConnTestOptions{Host: "127.0.0.1"})
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

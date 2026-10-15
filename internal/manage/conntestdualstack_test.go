package manage

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestConnectionTestSelectionPinsTheRequestedIPFamily(t *testing.T) {
	for _, tt := range []struct {
		mode, host, v4, v6 string
		want               []string
	}{
		{"manual", "[::1]", "", "", []string{"::1"}},
		{"", "127.0.0.1", "", "", []string{"127.0.0.1"}},
		{"ipv4", "", "127.0.0.1", "", []string{"127.0.0.1"}},
		{"ipv6", "", "", "::1", []string{"::1"}},
		{"both", "", "127.0.0.1", "::1", []string{"127.0.0.1", "::1"}},
	} {
		got, err := ConnTestHosts(context.Background(), tt.mode, tt.host, tt.v4, tt.v6)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%+v: %v %v", tt, got, err)
		}
	}
	for _, tt := range []struct{ mode, v4, v6 string }{
		{"both", "127.0.0.1", ""}, {"ipv6", "", "::ffff:127.0.0.1"}, {"ipv6", "", "127.0.0.1"}, {"ipv4", "::1", ""}, {"invalid", "", ""},
	} {
		if _, err := ConnTestHosts(context.Background(), tt.mode, "", tt.v4, tt.v6); err == nil {
			t.Errorf("invalid family selection accepted: %+v", tt)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ConnTestHosts(ctx, "manual", "::1", "", ""); err == nil {
		t.Fatal("cancelled selection resolved anyway")
	}
}

func TestConnectionTestDiscoveryDoesNotAdvertiseLocalOrReservedAddresses(t *testing.T) {
	for _, ip := range []string{"::1", "fe80::1", "fc00::1", "ff02::1", "::", "2001:db8::1", "127.0.0.1", "10.0.0.1", "100.64.1.1", "169.254.1.1", "192.0.2.1", "198.18.0.1", "224.0.0.1", "255.255.255.255", "::ffff:192.168.1.1"} {
		if ctPublicAddress(netip.MustParseAddr(ip)) {
			t.Errorf("advertised %s", ip)
		}
	}
	for _, ip := range []string{"2a01:4f8::1", "2606:4700:4700::1111", "8.8.8.8"} {
		if !ctPublicAddress(netip.MustParseAddr(ip)) {
			t.Errorf("public address excluded: %s", ip)
		}
	}
}

func TestConnectionTestLinksKeepIPv6AndBothPathsIntact(t *testing.T) {
	for _, host := range []string{"::1", "::10", "2001:db8::1"} {
		link := ConnTestLink{Host: host, Coord: 40001, Tok: ctNewSecret()}
		a, err := parseConnTestLink(link.Short())
		if err != nil || a.Host != host {
			t.Fatalf("IPv6 round trip %s: %+v %v", host, a, err)
		}
	}
	routes := []connTestAddr{{Host: "127.0.0.1", Coord: 40001, Tok: ctNewSecret()}, {Host: "::1", Coord: 40002, Tok: ctNewSecret()}}
	raw := ctSuiteLink(routes)
	a, err := parseConnTestLink(raw)
	if err != nil || !reflect.DeepEqual(a.Routes, routes) || !IsConnTestLink(raw) {
		t.Fatalf("two-path link changed: %+v %v", a, err)
	}
	bad := append([]connTestAddr(nil), routes...)
	bad[1].Host = "127.0.0.2"
	if _, err := parseConnTestLink(ctSuiteLink(bad)); err == nil {
		t.Fatal("two IPv4 paths passed as a dual-family test")
	}
	bad[1] = routes[1]
	bad[1].Tok = "invalid"
	if _, err := parseConnTestLink(ctSuiteLink(bad)); err == nil {
		t.Fatal("damaged secret accepted")
	}
	if _, err := parseConnTestLink(connTestScheme + base64.RawURLEncoding.EncodeToString(append([]byte{2}, bytes.Repeat([]byte{'x'}, 4096)...))); err == nil {
		t.Fatal("oversized plan accepted")
	}
}

func ctRequireIPv6(t *testing.T) {
	t.Helper()
	l, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 loopback unavailable: ", err)
	}
	l.Close()
}

func TestUnjoinedFamiliesShareOneJoinDeadlineAndKeepHonestErrors(t *testing.T) {
	ctRequireIPv6(t)
	previous, wait := connTestReverse, connTestJoinWait
	connTestReverse = nil
	connTestJoinWait = 30 * time.Millisecond
	defer func() { connTestReverse, connTestJoinWait = previous, wait }()
	suite, _, err := StartConnTestSuite(ConnTestOptions{}, []string{"127.0.0.1", "::1"})
	if err != nil {
		t.Fatal(err)
	}
	defer suite.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	paths := suite.Run(ctx, nil)
	for _, p := range paths {
		if p.State != "unavailable" || !strings.Contains(p.Error, "did not join") || len(p.Rows) != 0 {
			t.Fatalf("invented a peer reachability check: %+v", p)
		}
	}
	suite.paths[0].Error = "preparation failed on Iran"
	if !suite.unavailable(0) || suite.Paths()[0].Error != "preparation failed on Iran" {
		t.Fatal("a repeated peer report overwrote the original preparation error")
	}
}

func TestAnUnreachablePathDoesNotHoldTheOtherFamilyForTheJoinTimeout(t *testing.T) {
	ctRequireIPv6(t)
	previous, wait := connTestReverse, connTestPathCheckWait
	connTestReverse = nil
	connTestPathCheckWait = 300 * time.Millisecond
	defer func() { connTestReverse, connTestPathCheckWait = previous, wait }()
	for _, failed := range []int{0, 1} {
		t.Run([]string{"IPv4 unavailable", "IPv6 unavailable"}[failed], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			suite, raw, err := StartConnTestSuite(ConnTestOptions{Context: ctx}, []string{"127.0.0.1", "::1"})
			if err != nil {
				t.Fatal(err)
			}
			defer suite.Close()
			a, err := parseConnTestLink(raw)
			if err != nil {
				t.Fatal(err)
			}
			suite.tests[failed].coord.close() // simulate a path whose control port cannot be reached
			type response struct {
				paths []ConnTestPath
				err   error
			}
			peer := make(chan response, 1)
			go func() { p, e := runConnTestSuiteKharej(ctx, a.Routes, io.Discard, nil); peer <- response{p, e} }()
			paths := suite.Run(ctx, nil)
			p := <-peer
			if p.err != nil || ctx.Err() != nil || paths[failed].State != "unavailable" || paths[1-failed].State != "done" {
				t.Fatalf("Iran %+v; peer %+v %v; context %v", paths, p.paths, p.err, ctx.Err())
			}
			if len(paths[failed].Rows) != 0 || !strings.Contains(paths[failed].Error, "not measured") {
				t.Fatalf("unreachable control path reported a protocol verdict: %+v", paths[failed])
			}
		})
	}
}

func TestIPv6RawCarriersAreSkippedBeforeStartingAnEngine(t *testing.T) {
	ctRequireIPv6(t)
	previous := connTestReverse
	connTestReverse = []string{"pck"}
	defer func() { connTestReverse = previous }()
	s, _, err := StartConnTestIran(ConnTestOptions{Host: "::1", Direct: true, SpoofSrc: ConnTestSpoofSource})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, c := range s.cases {
		raw := c.kind == "spoof" || c.tr == "pck" || c.tr == "sni" || c.tr == "xdi"
		if raw && !strings.Contains(c.skip, "IPv6 is not supported") {
			t.Errorf("%s/%s not explicitly unsupported: %s", c.kind, c.tr, c.skip)
		}
		if !raw && os.Geteuid() == 0 && c.skip != "" {
			t.Errorf("supported IPv6 carrier %s skipped: %s", c.tr, c.skip)
		}
	}
	if s.link.SpoofSrc != "" {
		t.Fatal("IPv6 peer was told to start an IPv4 spoof sender")
	}
}

func TestBothFamiliesCarryRealTrafficThroughOneLinkInSequence(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the real tunnel engine")
	}
	ctRequireIPv6(t)
	bin := filepath.Join(t.TempDir(), "backpack")
	build := exec.Command("go", "build", "-o", bin, "../..")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}
	previous, soak, binary := connTestReverse, connTestSoak, connTestBinary
	connTestReverse = []string{"tcp", "ws", "quic", "udp"}
	connTestSoak = 2
	connTestBinary = func() (string, error) { return bin, nil }
	defer func() { connTestReverse, connTestSoak, connTestBinary = previous, soak, binary }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	suite, raw, err := StartConnTestSuite(ConnTestOptions{Context: ctx}, []string{"127.0.0.1", "::1"})
	if err != nil {
		t.Fatal(err)
	}
	defer suite.Close()
	a, err := parseConnTestLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	type response struct {
		paths []ConnTestPath
		err   error
	}
	peer := make(chan response, 1)
	go func() { p, e := runConnTestSuiteKharej(ctx, a.Routes, io.Discard, nil); peer <- response{p, e} }()
	advanced := false
	paths := suite.Run(ctx, func(paths []ConnTestPath) {
		if paths[1].State == "running" {
			advanced = true
			if paths[0].State != "done" {
				t.Error("IPv6 measured before IPv4 finished")
			}
		}
	})
	p := <-peer
	if p.err != nil || !advanced {
		t.Fatalf("peer: %v; advanced %v; Iran %+v", p.err, advanced, paths)
	}
	for i, path := range paths {
		if path.State != "done" || path.Best.Host != path.Host || path.Best.Family != path.Family {
			t.Errorf("path lost its identity: %+v", path)
		}
		if path.Best.PathMTU != 1500 {
			t.Errorf("%s MTU = %d", path.Family, path.Best.PathMTU)
		}
		for _, r := range path.Rows {
			if r.Status != ctOK || r.OK != 2 {
				t.Errorf("%s: %+v", path.Family, r)
			}
		}
		if !reflect.DeepEqual(path.Rows, p.paths[i].Rows) {
			t.Errorf("%s verdict differs across servers", path.Family)
		}
	}
	if ConnTestSuiteBest(paths).Transport == "" {
		t.Error("no winning IP path")
	}
}

func TestStoppingASuiteTellsBothPeersAndCleansEveryPath(t *testing.T) {
	ctRequireIPv6(t)
	previous := connTestReverse
	connTestReverse = nil
	defer func() { connTestReverse = previous }()
	suite, raw, err := StartConnTestSuite(ConnTestOptions{}, []string{"127.0.0.1", "::1"})
	if err != nil {
		t.Fatal(err)
	}
	defer suite.Close()
	a, err := parseConnTestLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	finished := make(chan []ConnTestPath, 1)
	go func() { finished <- suite.Run(ctx, nil) }()
	check, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	for _, route := range a.Routes {
		for {
			_, err := fetchConnTestLink(check, route)
			if errors.Is(err, errConnTestStopped) {
				break
			}
			if check.Err() != nil {
				t.Fatalf("peer never heard cancellation: %v", err)
			}
			ctSleep(check, 10*time.Millisecond)
		}
	}
	select {
	case paths := <-finished:
		for _, p := range paths {
			if p.State != "stopped" {
				t.Errorf("path survived stop: %+v", p)
			}
		}
	case <-check.Done():
		t.Fatal("suite did not stop after both peers acknowledged cancellation")
	}
	suite.Close()
	for _, test := range suite.tests {
		if _, err := os.Stat(test.dir); !os.IsNotExist(err) {
			t.Errorf("test directory survived stop: %s %v", test.dir, err)
		}
	}
}

func TestBackgroundProbesCannotAcknowledgeThePeerHasStopped(t *testing.T) {
	c, err := startCTCoordinator(0, "tok")
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	c.mu.Lock()
	c.stopped = true
	c.mu.Unlock()
	port := c.tcp.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if reply, err := ctAskContext(ctx, "127.0.0.1", port, "pmtu tok 1280 pad"); err != nil || reply != "stopped" {
		t.Fatalf("probe: %q %v", reply, err)
	}
	select {
	case <-c.fetched:
		t.Fatal("background PMTU reply acknowledged the main worker's shutdown")
	default:
	}
	if reply, err := ctAskContext(ctx, "127.0.0.1", port, "result tok"); err != nil || reply != "stopped" {
		t.Fatalf("worker: %q %v", reply, err)
	}
	select {
	case <-c.fetched:
	case <-ctx.Done():
		t.Fatal("worker's stop reply was not acknowledged")
	}
}

func TestStoppingBetweenPathsNeverStartsTheRemainingFamily(t *testing.T) {
	ctRequireIPv6(t)
	previous := connTestReverse
	connTestReverse = nil
	defer func() { connTestReverse = previous }()
	iranCtx, cancelIran := context.WithCancel(context.Background())
	defer cancelIran()
	suite, raw, err := StartConnTestSuite(ConnTestOptions{Context: iranCtx}, []string{"127.0.0.1", "::1"})
	if err != nil {
		t.Fatal(err)
	}
	defer suite.Close()
	a, err := parseConnTestLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan []ConnTestPath, 1)
	go func() { finished <- suite.Run(iranCtx, nil) }()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	paths, err := runConnTestSuiteKharej(ctx, a.Routes, io.Discard, func(i int, p ConnTestPath) {
		if i == 0 && p.State == "done" {
			cancelIran()
		}
	})
	if !errors.Is(err, errConnTestStopped) || paths[1].State != "stopped" || len(paths[1].Rows) != 0 {
		t.Fatalf("queued family ran after stop: %+v %v", paths, err)
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("Iran did not finish cancelling both paths")
	}
}

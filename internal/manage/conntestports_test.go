package manage

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestConnectionTestChoosesDirectPortsOnKharej(t *testing.T) {
	c, err := startCTCoordinator(ctPickPort(map[int]bool{}, true), "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	occupied, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	occupiedPort := occupied.LocalAddr().(*net.UDPAddr).Port
	link := ConnTestLink{Host: "127.0.0.1", Coord: c.tcp.Addr().(*net.TCPAddr).Port, Tok: c.tok, DirectPorts: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	used := map[int]bool{occupiedPort: true}
	ports := map[string]bool{}
	for _, tr := range []string{"udp", "quic", "pck", "sni"} {
		port, err := ctKharejDirectPort(ctx, link, tr, used)
		if err != nil {
			t.Fatal(err)
		}
		if port == strconv.Itoa(occupiedPort) || ports[port] {
			t.Fatalf("unusable or duplicate port %s", port)
		}
		ports[port] = true
		if got := c.directPort(tr, "9000"); got != port {
			t.Fatalf("Iran endpoint %s differs from Kharej %s", got, port)
		}
		tcp, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			t.Fatal(err)
		}
		tcp.Close()
		udp, err := net.ListenPacket("udp", "127.0.0.1:"+port)
		if err != nil {
			t.Fatal(err)
		}
		udp.Close()
	}
	// Old Kharej versions send no port update; their original endpoint stays.
	if got := c.directPort("xdi", "9000"); got != "9000" {
		t.Fatal(got)
	}
	if got := (&ctCoordinator{}).directPort("udp", "9001"); got != "9001" {
		t.Fatal(got)
	}
}

func TestConnectionTestPortUpdatesRequireAuthenticationAndFreezeAtJoin(t *testing.T) {
	c := &ctCoordinator{tok: "secret", joined: make(chan struct{})}
	for _, line := range []string{"port wrong udp:9000", "port secret udp:0", "port secret udp:65536", "port secret other:9000", "port secret xdi:9000", "port secret udp:abc"} {
		if reply := c.answer(line, "192.0.2.1"); reply != "" {
			t.Fatalf("accepted %q", line)
		}
	}
	if got := c.directPort("udp", "8000"); got != "8000" {
		t.Fatal(got)
	}
	if reply := c.answer("port secret udp:9000", "192.0.2.1"); reply != "ok" {
		t.Fatal(reply)
	}
	if reply := c.answer("hello secret", "192.0.2.1"); reply != "ok" {
		t.Fatal(reply)
	}
	if reply := c.answer("port secret udp:9001", "192.0.2.1"); reply != "" {
		t.Fatal("port changed after join")
	}
	if reply := c.answer("port secret udp:9000", "192.0.2.1"); reply != "ok" {
		t.Fatal("repeat lost acknowledgment rejected")
	}
	if got := c.directPort("udp", "8000"); got != "9000" {
		t.Fatal(got)
	}
}

func TestConnectionTestMissingControlDoesNotClaimEveryCarrierIsBlocked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fetchConnTestLink(ctx, connTestAddr{Host: "127.0.0.1", Coord: 1, Tok: "secret"})
	if err == nil {
		t.Fatal("cancelled fetch succeeded")
	}
	if !strings.Contains(err.Error(), "Tunnel protocols have not been measured") || !strings.Contains(err.Error(), "Direct xDi") {
		t.Fatalf("missing coordinator was mistaken for a tunnel verdict: %v", err)
	}
}

func TestConnectionTestLocalFailureDoesNotSkipTheOtherDirection(t *testing.T) {
	c := &ctCoordinator{tok: "secret"}
	for _, line := range []string{"skip wrong direct/udp", "skip secret unknown/udp", "skip secret direct/no-such-carrier"} {
		if got := c.answer(line, "192.0.2.1"); got != "" {
			t.Fatal(line)
		}
	}
	if got := c.answer("skip secret direct/udp", "192.0.2.1"); got != "ok" {
		t.Fatal(got)
	}
	if !c.skipped["direct/udp"] || c.skipped["reverse/udp"] || c.skipped["udp"] {
		t.Fatal(c.skipped)
	}
	if got := c.answer("skip secret naive", "192.0.2.1"); got != "ok" {
		t.Fatal("old helper skip command broke")
	}
}

// A picked port is bound later by an engine; one inside the ephemeral range
// can meanwhile become the source port of an outgoing connection, and the
// tunnel meant to listen there never comes up.
func TestConnectionTestPortsStayBelowTheEphemeralRange(t *testing.T) {
	used := map[int]bool{}
	for i := 0; i < 200; i++ {
		p := ctPickPort(used, i%2 == 0)
		if p < 20000 || p >= ephemeralDefaultLow {
			t.Fatalf("picked port %d, want [20000, %d)", p, ephemeralDefaultLow)
		}
	}
}

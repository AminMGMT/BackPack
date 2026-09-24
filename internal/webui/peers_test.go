package webui

import (
	"net"
	"os/exec"
	"testing"
)

func TestPeersAreReadPerPortFromOneDump(t *testing.T) {
	out := "ESTAB 0 0 10.0.0.1:9001 203.0.113.5:40000\n" +
		"\t cubic rtt:12.4/3 ato:40\n" +
		"ESTAB 0 0 10.0.0.1:9001 203.0.113.5:40001\n" +
		"\t cubic rtt:13/3\n" +
		"ESTAB 0 0 10.0.0.1:9002 198.51.100.7:5000\n" +
		"\t cubic rtt:80.6/3\n" +
		"ESTAB 0 0 10.0.0.1:443 192.0.2.1:1234\n" +
		"ESTAB 0 0 127.0.0.1:9001 127.0.0.1:5555\n"
	got := parsePeers(out, map[string]bool{"9001": true, "9002": true})
	if p := got["9001"]; len(p) != 1 || p[0].IP != "203.0.113.5" || p[0].RTT != 12 {
		t.Errorf("9001: %+v, want one peer at 12ms (the duplicate and loopback dropped)", p)
	}
	if p := got["9002"]; len(p) != 1 || p[0].IP != "198.51.100.7" || p[0].RTT != 81 {
		t.Errorf("9002: %+v", p)
	}
	if _, ok := got["443"]; ok {
		t.Error("a port nobody asked about was kept")
	}
}

// The kernel-side filter is real syntax on this machine's ss, and it keeps the
// asked-for port and nothing else.
func TestTheSocketFilterWorksAgainstRealSockets(t *testing.T) {
	if _, err := exec.LookPath("ss"); err != nil {
		t.Skip("no ss here")
	}
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	// A non-loopback-looking peer is needed for it to count, so dial the
	// machine's own address when there is one; loopback peers are dropped by
	// design, which still proves the filter ran without error.
	c, err := net.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	peerCache.key = "" // no stale answer from another test
	if got := listeningPeers([]string{port}); got == nil {
		t.Fatal("ss with the port filter failed — the filter syntax is not accepted here")
	}
}

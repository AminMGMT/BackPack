package network

import (
	"bytes"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	relinkPort   = 40311
	relinkHelper = "BP_PCK_RELINK_HELPER"
)

// TestACarrierReopensOnARecreatedInterface deletes the interface both ends of a
// pck carrier run on and creates it again under the same names and addresses —
// a veth or ppp device recreated, a NIC replugged — and requires datagrams to
// flow again with neither carrier reopened by hand. Before relink, the receive
// socket stayed bound to the old interface index and never heard another
// packet, and sends failed with ENXIO, which KCP took for the end of its
// session.
//
// The server end runs here; the client end is this test binary again, run in a
// second network namespace across a veth, sending a datagram every 50 ms.
//
// It needs root in a network namespace of its own, for example:
//
//	go test -c -o /tmp/n.test ./internal/utils/network
//	unshare --map-auto --map-root-user --net --mount --fork /tmp/n.test -test.run Recreated
//
// and is skipped otherwise.
func TestACarrierReopensOnARecreatedInterface(t *testing.T) {
	if os.Getenv(relinkHelper) != "" {
		relinkClient()
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root (run inside unshare --map-root-user --net --mount)")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("needs ip(8)")
	}
	// A fresh namespace has lo and, with tunnel modules loaded, a few fallback
	// devices (tunl0, gre0, ...) that are down; anything up besides lo means this
	// is somebody's real network.
	ifs, _ := net.Interfaces()
	for _, in := range ifs {
		if in.Flags&net.FlagUp != 0 && in.Flags&net.FlagLoopback == 0 {
			t.Skipf("%s is up: not an empty network namespace; refusing to touch real interfaces", in.Name)
		}
	}
	run := func(name string, args ...string) {
		t.Helper()
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
		}
	}
	run("mount", "-t", "tmpfs", "none", "/run")
	run("mkdir", "-p", "/run/netns")
	run("ip", "netns", "add", "bprpeer")
	defer exec.Command("ip", "netns", "del", "bprpeer").Run()
	run("ip", "link", "set", "lo", "up")
	run("ip", "-n", "bprpeer", "link", "set", "lo", "up")
	up := func() {
		run("ip", "link", "add", "bpr0", "type", "veth", "peer", "name", "bpr1", "netns", "bprpeer")
		run("ip", "addr", "add", "10.231.0.1/24", "dev", "bpr0")
		run("ip", "-n", "bprpeer", "addr", "add", "10.231.0.2/24", "dev", "bpr1")
		run("ip", "link", "set", "bpr0", "up")
		run("ip", "-n", "bprpeer", "link", "set", "bpr1", "up")
	}
	up()
	defer exec.Command("ip", "link", "del", "bpr0").Run()

	srvConn, err := newPckConn(true, relinkPort, PcapCarrier{Port: relinkPort, Interface: "bpr0", Token: "relink-test"})
	if err != nil {
		t.Fatalf("server carrier: %v", err)
	}
	defer srvConn.Close()
	srv := srvConn.(*pckConn)
	var logs syncLog // written by relink's goroutine
	srv.SetLogf(func(f string, a ...any) { logs.add(f) })

	var clientOut bytes.Buffer
	cmd := exec.Command("ip", "netns", "exec", "bprpeer", os.Args[0], "-test.run", "^TestACarrierReopensOnARecreatedInterface$")
	cmd.Env = append(os.Environ(), relinkHelper+"=1")
	cmd.Stdout, cmd.Stderr = &clientOut, &clientOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("client: %v", err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()

	// hears reports whether a datagram from the client arrives within d.
	hears := func(d time.Duration) bool {
		buf := make([]byte, 2048)
		end := time.Now().Add(d)
		for time.Now().Before(end) {
			srv.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, _, err := srv.ReadFrom(buf)
			if err == nil && string(buf[:n]) == "ping" {
				return true
			}
			if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("the server's read ended: %v", err)
			}
		}
		return false
	}
	if !hears(10 * time.Second) {
		t.Fatalf("the carrier never worked in the first place; client:\n%s", clientOut.String())
	}

	oldIndex := srv.link.Load().egress.Iface.Index
	run("ip", "link", "del", "bpr0") // takes its peer bpr1 with it
	if hears(700 * time.Millisecond) {
		t.Fatal("a datagram arrived over a deleted interface")
	}
	up()
	if !hears(15 * time.Second) {
		t.Fatalf("nothing reached the server within 15 s of the interface coming back (server relinks %d)\n%s\nclient:\n%s",
			srv.Relinks(), logs.String(), clientOut.String())
	}
	if srv.Relinks() != 1 {
		t.Errorf("the server relinked %d times; want 1", srv.Relinks())
	}
	if got := srv.link.Load().egress.Iface.Index; got == oldIndex {
		t.Errorf("the server still names index %d", got)
	}
	if !strings.Contains(logs.String(), "is back") {
		t.Errorf("nothing logged the reopen:\n%s", logs.String())
	}
}

// relinkClient is the client end of the test above: it sends forever, and a
// send that fails ends the process with the error, which the test then shows.
func relinkClient() {
	conn, err := newPckConn(false, relinkPort, PcapCarrier{Port: relinkPort, Interface: "bpr1", PeerIP: "10.231.0.1", Token: "relink-test"})
	if err != nil {
		os.Stderr.WriteString("client carrier: " + err.Error() + "\n")
		os.Exit(1)
	}
	c := conn.(*pckConn)
	c.SetLogf(func(f string, a ...any) { os.Stderr.WriteString("client: " + f + "\n") })
	to := &net.UDPAddr{IP: net.IPv4(10, 231, 0, 1), Port: relinkPort}
	for {
		if _, err := c.WriteTo([]byte("ping"), to); err != nil {
			os.Stderr.WriteString("client send: " + err.Error() + "\n")
			os.Exit(1)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type syncLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *syncLog) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b.WriteString(line + "\n")
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

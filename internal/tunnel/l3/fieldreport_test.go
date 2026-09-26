package l3

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// A kharej set up as "10.10.2.2/30 ↔ 10.10.2.2" — its own address typed twice
// — is refused with the reason, instead of coming up and carrying nothing.
func TestTheSameAddressAtBothEndsIsRefused(t *testing.T) {
	if err := CheckTunnelEnds("10.10.2.2/30", "10.10.2.2"); err == nil ||
		!strings.Contains(err.Error(), "other machine's") {
		t.Fatalf("CheckTunnelEnds = %v, want a refusal that says what peer_ip is", err)
	}
	if err := CheckTunnelEnds("10.10.2.2/30", "10.10.2.1"); err != nil {
		t.Fatalf("a correct pair was refused: %v", err)
	}
	if err := CheckTunnelEnds("10.10.2.2/30", ""); err != nil {
		t.Fatalf("an omitted peer was refused: %v", err)
	}
	cfg := Config{Mode: ModeListen, Addr: "0.0.0.0:9000", Token: "t", LocalIP: "10.10.2.2/30", PeerIP: "10.10.2.2"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted the same address at both ends")
	}
}

// While the tunnel has no session, a refused port is blamed on the tunnel —
// not on a service at the far end that nothing could have reached.
func TestARefusedPortBlamesTheTunnelWhileItIsDown(t *testing.T) {
	dead := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	port := freePort(t)
	log, captured := testLoggerCapturing(t)
	forwarder, err := NewForwarder(Config{
		Ports:  []string{fmt.Sprintf("127.0.0.1:%d=%s", port, dead)},
		PeerIP: "127.0.0.1",
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	forwarder.SetTunnelState(func() bool { return false })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = forwarder.Run(ctx) }()

	conn := dialUntilReady(t, fmt.Sprintf("127.0.0.1:%d", port))
	conn.Close()
	waitFor(t, captured, "the tunnel is not up yet", "a refused port was not blamed on the tunnel")
	if strings.Contains(captured.String(), "The tunnel itself is up") {
		t.Fatalf("the warning claims the tunnel is up while it is not:\n%s", captured.String())
	}
}

// An xdi echo for another tunnel is reported while there is no session, and
// not once one is up.
func TestAForeignTagIsReportedOnlyWithoutASession(t *testing.T) {
	log, captured := testLoggerCapturing(t)
	tun := &Tunnel{log: log, foreignTags: reportEvery{every: time.Minute}}
	from := &net.IPAddr{IP: net.ParseIP("198.51.100.7")}

	tun.noteForeignTag(from)
	if !strings.Contains(captured.String(), "token on the two servers is not the same") {
		t.Fatalf("no warning without a session:\n%s", captured.String())
	}

	log2, captured2 := testLoggerCapturing(t)
	up := &Tunnel{log: log2, foreignTags: reportEvery{every: time.Minute}, current: &session{}}
	up.noteForeignTag(from)
	if captured2.String() != "" {
		t.Fatalf("warned with a session up:\n%s", captured2.String())
	}
}

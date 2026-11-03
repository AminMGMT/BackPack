//go:build linux

package network

import (
	"encoding/binary"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

// The guard's job is to drop the kernel's automatic Echo Reply while leaving
// the carrier's own Echo Replies alone. In reply mode both are outbound Echo
// Replies carrying the tunnel's identifier, so the mark on the carrier's send
// socket is the only thing that tells them apart. A rule-string test cannot
// show that the mark survives the trip through netfilter, so this sends real
// packets through a real iptables rule, in a network namespace of its own.
//
//	BP_ICMP_GUARD_LIVE=1 go test ./internal/utils/network -run TestICMPEchoGuardExemptsTheCarriersMarkedReplies -v
//
// It needs what internal/e2e's live tests need — unprivileged user namespaces,
// and the unshare, ip and iptables binaries — and, like them, is opt-in.
const netnsChildEnv = "BP_ICMP_GUARD_NETNS_CHILD"

// testReplyMark is the fwmark the carrier stamps on its own replies. It is a
// literal rather than the carrier's constant so this test could also be run
// against the pre-fix rule; TestICMPEchoRuleExemptsTheCarriersMarkedReplies
// pins the production rule to it, so the two cannot drift apart unseen.
const testReplyMark = 0x4241434b

func TestICMPEchoGuardExemptsTheCarriersMarkedReplies(t *testing.T) {
	if os.Getenv(netnsChildEnv) == "1" {
		runICMPEchoGuardNetns(t)
		return
	}
	if os.Getenv("BP_ICMP_GUARD_LIVE") == "" {
		t.Skip("set BP_ICMP_GUARD_LIVE=1 to exercise the guard's iptables rule with real packets in a network namespace")
	}
	for _, tool := range []string{"unshare", "ip", "iptables"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is needed and not installed", tool)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("unshare", "--map-auto", "--map-root-user", "--net", "--mount", "--fork", "--",
		self, "-test.run", "^TestICMPEchoGuardExemptsTheCarriersMarkedReplies$", "-test.v")
	cmd.Env = append(os.Environ(), netnsChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the guard test inside a network namespace failed: %v\n%s", err, out)
	}
}

func runICMPEchoGuardNetns(t *testing.T) {
	if out, err := exec.Command("ip", "link", "set", "lo", "up").CombinedOutput(); err != nil {
		t.Fatalf("bringing lo up: %v\n%s", err, out)
	}
	// A strict reverse-path filter would drop the loopback copies before the
	// raw socket could see them.
	for _, p := range []string{
		"/proc/sys/net/ipv4/conf/all/rp_filter",
		"/proc/sys/net/ipv4/conf/lo/rp_filter",
	} {
		_ = os.WriteFile(p, []byte("0"), 0o644)
	}

	const port = 13000
	guard := installICMPEchoGuard(port)
	defer guard.remove()
	if !guard.Installed() {
		out, _ := exec.Command("iptables", "-S", "OUTPUT").CombinedOutput()
		t.Fatalf("the guard did not install; OUTPUT is:\n%s", out)
	}

	recv, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		t.Fatalf("opening the raw receive socket: %v", err)
	}
	defer recv.Close()

	plainPC, plain, err := openRawIP(protoRawSend, "", 0)
	if err != nil {
		t.Fatalf("opening an unmarked send socket: %v", err)
	}
	defer plainPC.Close()
	markedPC, marked, err := openRawIP(protoRawSend, "", 0)
	if err != nil {
		t.Fatalf("opening a marked send socket: %v", err)
	}
	defer markedPC.Close()
	if err := markTestSocket(markedPC, testReplyMark); err != nil {
		t.Fatalf("marking the send socket: %v", err)
	}

	// A marked reply carrying the tunnel's identifier is the carrier's own
	// uplink in reply mode; the guard must let it through. This is the bug: the
	// pre-fix rule drops every reply with that identifier, marked or not — and
	// netfilter reports that DROP to the local sender as EPERM.
	if err := sendTestEcho(marked, icmpTypeEchoReply, port, 1001); err != nil {
		t.Fatalf("the guard dropped a marked Echo Reply carrying the tunnel's identifier (the send failed: %v) — the tunnel's own packets", err)
	}
	if !awaitTestEcho(t, recv, icmpTypeEchoReply, port, 1001, 2*time.Second) {
		t.Fatal("a marked Echo Reply carrying the tunnel's identifier never arrived")
	}

	// An unmarked reply with that identifier is the kernel's automatic answer
	// to an inbound request; it must still be suppressed.
	if err := sendTestEcho(plain, icmpTypeEchoReply, port, 1002); err == nil &&
		awaitTestEcho(t, recv, icmpTypeEchoReply, port, 1002, 300*time.Millisecond) {
		t.Fatal("the guard let the kernel's automatic Echo Reply through")
	}

	// A different identifier — a real ping across the tunnel — is untouched.
	if err := sendTestEcho(plain, icmpTypeEchoReply, port+1, 1003); err != nil {
		t.Fatalf("the guard dropped an unrelated Echo Reply: %v", err)
	}
	if !awaitTestEcho(t, recv, icmpTypeEchoReply, port+1, 1003, 2*time.Second) {
		t.Fatal("an unrelated Echo Reply never arrived")
	}

	// Requests are untouched: they are the carrier's own uplink in the default
	// request/request mode.
	if err := sendTestEcho(plain, icmpTypeEchoRequest, port, 1004); err != nil {
		t.Fatalf("the guard dropped an Echo Request: %v", err)
	}
	if !awaitTestEcho(t, recv, icmpTypeEchoRequest, port, 1004, 2*time.Second) {
		t.Fatal("an Echo Request never arrived")
	}

	// And removing the guard restores the dropped reply: the rule does not
	// outlive its carrier.
	guard.remove()
	if err := sendTestEcho(plain, icmpTypeEchoReply, port, 1005); err != nil {
		t.Fatalf("the guard's rule outlived the guard: %v", err)
	}
	if !awaitTestEcho(t, recv, icmpTypeEchoReply, port, 1005, 2*time.Second) {
		t.Fatal("the reply after removing the guard never arrived")
	}
}

// markTestSocket sets SO_MARK without going through the production helper, so
// the test exercises the same setsockopt the carrier makes without depending on
// code added by the fix.
func markTestSocket(pc net.PacketConn, mark int) error {
	ipc, ok := pc.(*net.IPConn)
	if !ok {
		return unix.EINVAL
	}
	raw, err := ipc.SyscallConn()
	if err != nil {
		return err
	}
	var setErr error
	if err := raw.Control(func(fd uintptr) {
		setErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, mark)
	}); err != nil {
		return err
	}
	return setErr
}

// sendTestEcho puts one echo message on loopback with a full IP header, the way
// the carrier sends. A DROP in OUTPUT comes back to a local sender as EPERM, so
// the caller decides whether that is the expected outcome.
func sendTestEcho(conn *ipv4.RawConn, typ byte, id, seq uint16) error {
	shim := buildICMPEcho(typ, id, seq, []byte("guard-test"))
	h := &ipv4.Header{
		Version: ipv4.Version, Len: ipv4.HeaderLen, TotalLen: ipv4.HeaderLen + len(shim),
		ID: 0x4242, TTL: 64, Protocol: 1,
		Src: net.IPv4(127, 0, 0, 1).To4(), Dst: net.IPv4(127, 0, 0, 2).To4(),
	}
	return conn.WriteTo(h, shim, nil)
}

// awaitTestEcho reports whether an echo of the given shape arrived within d.
// The raw socket is handed the IP header as well, so it is skipped first.
func awaitTestEcho(t *testing.T, pc net.PacketConn, typ byte, id, seq uint16, d time.Duration) bool {
	t.Helper()
	buf := make([]byte, 2048)
	deadline := time.Now().Add(d)
	for {
		if err := pc.SetReadDeadline(deadline); err != nil {
			t.Fatalf("setting the read deadline: %v", err)
		}
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return false
			}
			t.Fatalf("reading the raw socket: %v", err)
		}
		msg := buf[:n]
		if len(msg) >= ipv4.HeaderLen && msg[0]>>4 == 4 && msg[9] == 1 {
			msg = msg[int(msg[0]&0x0f)*4:]
		}
		if len(msg) >= icmpEchoHeaderLen && msg[0] == typ &&
			binary.BigEndian.Uint16(msg[4:6]) == id && binary.BigEndian.Uint16(msg[6:8]) == seq {
			return true
		}
	}
}

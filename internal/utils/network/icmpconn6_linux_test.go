//go:build linux

package network

import (
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/xtaci/kcp-go/v5"
)

// xdi over ICMPv6: the same carrier, with ICMPv6's echo types (128 out from
// the client, 129 back from the server), the checksum left to the kernel —
// Linux fills it in for a raw ICMPv6 socket, over a pseudo-header only it
// knows — and IPv6 peers throughout.
func TestXdiRunsOverICMPv6(t *testing.T) {
	const token = "an-ipv6-xdi-token-0123456789abcdef"
	serverIP, clientIP := net.ParseIP("2001:db8::2"), net.ParseIP("2001:db8::1")
	wire := newICMPWire()
	block, err := kcpCrypt(token)
	if err != nil {
		t.Fatal(err)
	}

	srvSock := &recordingSocket{fakeICMPSocket: wire.socket(serverIP)}
	srv := newICMPServerConnWith(srvSock, token).(*icmpConn)
	srv.v6 = true
	listener, err := kcp.ServeConn(block, 10, 3, srv)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { listener.Close(); srv.Close() }()
	go func() {
		for {
			s, err := listener.AcceptKCP()
			if err != nil {
				return
			}
			go func() {
				buf := make([]byte, 256)
				for {
					n, err := s.Read(buf)
					if err != nil {
						return
					}
					_, _ = s.Write(buf[:n])
				}
			}()
		}
	}()

	cliSock := &recordingSocket{fakeICMPSocket: wire.socket(clientIP)}
	cli := newICMPClientConnWith(cliSock, token).(*icmpConn)
	cli.v6 = true
	session, err := ownedKCPSession(&net.IPAddr{IP: serverIP}, block, 10, 3, cli)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.Write([]byte("over icmpv6")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	_ = session.SetReadDeadline(time.Now().Add(10 * time.Second))
	n, err := session.Read(buf)
	if err != nil || string(buf[:n]) != "over icmpv6" {
		t.Fatalf("no echo over ICMPv6: %q, %v", buf[:n], err)
	}

	for _, c := range []struct {
		name string
		sock *recordingSocket
		want byte
	}{{"client", cliSock, 128}, {"server", srvSock, 129}} {
		sent := c.sock.sent()
		if len(sent) == 0 {
			t.Fatalf("the %s sent nothing", c.name)
		}
		for _, p := range sent {
			if p[0] != c.want {
				t.Fatalf("the %s sent ICMP type %d, want ICMPv6 %d", c.name, p[0], c.want)
			}
			if p[2] != 0 || p[3] != 0 {
				t.Fatalf("the %s filled in an ICMPv6 checksum the kernel computes: %#x%02x", c.name, p[2], p[3])
			}
		}
	}
	if !cli.IPv6() || !srv.IPv6() {
		t.Fatal("the carrier does not report itself as IPv6, so a tunnel inside it leaves too little room")
	}
}

// The server's family comes from its bind address, the client's from the
// address it dials.
func TestXdiTakesItsFamilyFromTheAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"[::]:443": true, "[2001:db8::1]:443": true, "::": true,
		"0.0.0.0:443": false, ":443": false, "192.0.2.1:443": false, "": false,
	} {
		if got := bindIsIPv6(addr); got != want {
			t.Errorf("bindIsIPv6(%q) = %v, want %v", addr, got, want)
		}
	}
	for addr, want := range map[string]string{
		"[2001:db8::1]:443": "2001:db8::1", "192.0.2.1:443": "192.0.2.1", "2001:db8::5": "2001:db8::5",
	} {
		ip, err := hostToXdiAddrContext(t.Context(), addr)
		if err != nil || ip.IP.String() != want {
			t.Errorf("hostToXdiAddrContext(%q) = %v, %v; want %s", addr, ip, err, want)
		}
	}
	// The ICMPv6 rules match the echo data 48 bytes in: a 40-byte IPv6
	// header, then the 8-byte echo header.
	tag := xdiTag("rule-token")
	for _, rule := range [][]string{xdiEchoRule6(tag), xdiAcceptRule6(tag, true), xdiAcceptRule6(tag, false)} {
		joined := strings.Join(rule, " ")
		for _, want := range []string{"ipv6-icmp", "--icmpv6-type", "48=0x"} {
			if !strings.Contains(joined, want) {
				t.Errorf("an ICMPv6 rule lacks %q: %s", want, joined)
			}
		}
	}
	if g := (&icmpEchoGuard{v6: true}); g.tool() != "ip6tables" {
		t.Errorf("an ICMPv6 guard goes into %s, want ip6tables", g.tool())
	}
}

// recordingSocket keeps a copy of everything written through it.
type recordingSocket struct {
	*fakeICMPSocket
	mu  sync.Mutex
	out [][]byte
}

func (r *recordingSocket) WriteTo(b []byte, dst net.Addr) (int, error) {
	r.mu.Lock()
	r.out = append(r.out, append([]byte(nil), b...))
	r.mu.Unlock()
	return r.fakeICMPSocket.WriteTo(b, dst)
}

func (r *recordingSocket) sent() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.out...)
}

// A full interface queue is one lost packet, not the end of the session. A
// raw IPv6 socket reports it as ENOBUFS where an IPv4 one says nothing, and
// kcp-go ends a session on any write error: over ICMPv6 a tunnel whose queue
// overflowed once under load went silent for good.
func TestAFullQueueIsALostPacketNotADeadTunnel(t *testing.T) {
	wire := newICMPWire()
	c := newICMPClientConnWith(&refusingSocket{fakeICMPSocket: wire.socket(net.ParseIP("2001:db8::1")), err: syscall.ENOBUFS}, "queue-token").(*icmpConn)
	c.v6 = true
	n, err := c.WriteTo([]byte("a segment"), &net.IPAddr{IP: net.ParseIP("2001:db8::2")})
	if err != nil || n != len("a segment") {
		t.Fatalf("a full queue was reported as %d, %v — kcp-go would end the session on it", n, err)
	}
	c2 := newICMPClientConnWith(&refusingSocket{fakeICMPSocket: wire.socket(net.ParseIP("2001:db8::3")), err: syscall.EPERM}, "queue-token").(*icmpConn)
	if _, err := c2.WriteTo([]byte("x"), &net.IPAddr{IP: net.ParseIP("2001:db8::2")}); err == nil {
		t.Fatal("a real failure to send was swallowed with the full queue")
	}
}

// refusingSocket fails every write with err.
type refusingSocket struct {
	*fakeICMPSocket
	err error
}

func (r *refusingSocket) WriteTo([]byte, net.Addr) (int, error) {
	return 0, &net.OpError{Op: "write", Net: "ip6:ipv6-icmp", Err: os.NewSyscallError("sendto", r.err)}
}

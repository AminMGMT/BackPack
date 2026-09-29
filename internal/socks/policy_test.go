package socks

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"
)

// The proxy refuses this machine and the link-local range, whatever it is
// asked for — the forwarded port it sits behind is open to strangers, and
// those are the addresses that would hand them what they were never meant to
// reach. A name that resolves to one of them is refused as well.
func TestTheProxyRefusesThisMachineAndLinkLocal(t *testing.T) {
	saved := Target
	Target = PublicOrPrivate
	defer func() { Target = saved }()

	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	_, port, _ := net.SplitHostPort(backend.Addr().String())

	for _, target := range []string{
		net.JoinHostPort("127.0.0.1", port),
		net.JoinHostPort("localhost", port),
		net.JoinHostPort("169.254.169.254", "80"),
		net.JoinHostPort("::1", port),
	} {
		if _, err := DialTarget("tcp", target, time.Second); !errors.Is(err, ErrRefusedTarget) {
			t.Errorf("%s: %v, want refused", target, err)
		}
	}
	for _, ip := range []string{"8.8.8.8", "10.1.2.3", "2001:4860:4860::8888"} {
		if !PublicOrPrivate(net.ParseIP(ip)) {
			t.Errorf("%s was refused; only this machine and link-local are", ip)
		}
	}

	// And through the SOCKS server itself.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	go Serve(ctx, addr, nil)
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	p, _ := strconv.Atoi(port)
	if c, err := Dial(addr, "", "", "127.0.0.1", p); err == nil {
		c.Close()
		t.Fatal("the SOCKS server connected a stranger to this machine's loopback")
	}
}

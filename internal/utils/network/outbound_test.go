package network

import (
	"context"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Nothing configured must stay nothing: a nil *Outbound has to be safe on every
// method, because that is the ordinary case and it is carried everywhere.
func TestNilOutboundIsInert(t *testing.T) {
	var o *Outbound

	if o.IsSet() {
		t.Error("a nil Outbound reported itself as configured")
	}
	if got := o.String(); got != "direct" {
		t.Errorf("a nil Outbound renders as %q, want \"direct\"", got)
	}
	if got := o.dialAddress("server:9000"); got != "server:9000" {
		t.Errorf("a nil Outbound redirected the dial to %q", got)
	}
	if o.proxy() != nil {
		t.Error("a nil Outbound produced a proxy")
	}
	if err := o.Validate(); err != nil {
		t.Errorf("a nil Outbound failed validation: %v", err)
	}
	addr, err := o.localTCPAddr()
	if err != nil || addr != nil {
		t.Errorf("a nil Outbound resolved a local address: (%v, %v)", addr, err)
	}
}

// An empty one is the same thing, since that is what a config with none of the
// fields set produces.
func TestEmptyOutboundIsNotSet(t *testing.T) {
	o := &Outbound{}
	if o.IsSet() {
		t.Error("an empty Outbound reported itself as configured")
	}
	for _, set := range []*Outbound{
		{LocalAddr: "10.0.0.1"},
		{Interface: "eth0"},
		{Mark: 100},
		{Proxy: &ProxyConfig{Scheme: "socks5", Address: "127.0.0.1:1080"}},
	} {
		if !set.IsSet() {
			t.Errorf("%+v reported itself as unconfigured", set)
		}
	}
}

// A source address is usually written without a port, because the port is the
// kernel's to pick. Both forms have to work.
func TestLocalAddrAcceptsBareAddressOrHostPort(t *testing.T) {
	for _, in := range []string{"127.0.0.1", "127.0.0.1:0", "[::1]", "[::1]:0"} {
		o := &Outbound{LocalAddr: in}
		addr, err := o.localTCPAddr()
		if err != nil {
			t.Errorf("localTCPAddr(%q): %v", in, err)
			continue
		}
		if addr == nil {
			t.Errorf("localTCPAddr(%q) returned nothing", in)
		}
	}
}

func TestLocalAddrRejectsNonsense(t *testing.T) {
	for _, in := range []string{"not an address", "999.999.999.999", "127.0.0.1:notaport"} {
		o := &Outbound{LocalAddr: in}
		if _, err := o.localTCPAddr(); err == nil {
			t.Errorf("localTCPAddr(%q) accepted it", in)
		}
	}
}

// An interface that does not exist has to be caught before anything is dialled,
// so the operator is told which line of the config is wrong rather than
// watching a tunnel that never connects.
func TestValidateRejectsAnUnknownInterface(t *testing.T) {
	o := &Outbound{Interface: "definitely-not-a-real-nic0"}
	err := o.Validate()
	if err == nil {
		t.Fatal("an interface that does not exist passed validation")
	}
	if !strings.Contains(err.Error(), "definitely-not-a-real-nic0") {
		t.Errorf("the error does not name the interface: %v", err)
	}
}

// The loopback exists everywhere these tests run, so a real name has to pass.
func TestValidateAcceptsARealInterface(t *testing.T) {
	name := "lo"
	if runtime.GOOS == "darwin" {
		name = "lo0"
	}
	if _, err := net.InterfaceByName(name); err != nil {
		t.Skipf("no %s interface here", name)
	}
	if err := (&Outbound{Interface: name}).Validate(); err != nil {
		t.Fatalf("a real interface failed validation: %v", err)
	}
}

func TestValidateRejectsANegativeMark(t *testing.T) {
	if err := (&Outbound{Mark: -1}).Validate(); err == nil {
		t.Fatal("a negative fwmark passed validation")
	}
}

// Binding a source address must actually take effect on the socket — this is
// the portable half of the feature, and the half that needs no privilege.
func TestOutboundBindsTheSourceAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- c
	}()

	conn, err := TcpDialerVia(context.Background(), &Outbound{LocalAddr: "127.0.0.1"},
		ln.Addr().String(), 5*time.Second, 30*time.Second, true, 1, 0, 0, 0)
	if err != nil {
		t.Fatalf("dial with a bound source address: %v", err)
	}
	defer conn.Close()

	select {
	case c := <-accepted:
		defer c.Close()
		host, _, err := net.SplitHostPort(c.RemoteAddr().String())
		if err != nil {
			t.Fatalf("SplitHostPort: %v", err)
		}
		if host != "127.0.0.1" {
			t.Fatalf("the connection arrived from %s, not the address it was bound to", host)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection never arrived")
	}
}

// A source address the machine does not hold cannot be bound, and that has to
// fail the dial rather than quietly going out by the default route.
func TestOutboundFailsOnAnUnusableSourceAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// A documentation address, which no machine running this holds.
	if _, err := TcpDialerVia(context.Background(), &Outbound{LocalAddr: "203.0.113.7"},
		ln.Addr().String(), 2*time.Second, 30*time.Second, true, 1, 0, 0, 0); err == nil {
		t.Fatal("binding to an address this machine does not hold reported success")
	}
}

// An interface binding that cannot be applied must fail the dial too. Off Linux
// the option does not exist at all, which is exactly the case that must not be
// silently ignored: a config written for a Linux server would otherwise appear
// to work here while sending traffic out the default route.
func TestOutboundFailsWhenTheBindingCannotBeApplied(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("on Linux this depends on privilege; the off-Linux case is the one that must never be silent")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	name := "lo0"
	if _, err := net.InterfaceByName(name); err != nil {
		t.Skipf("no %s interface here", name)
	}

	_, err = TcpDialerVia(context.Background(), &Outbound{Interface: name},
		ln.Addr().String(), 2*time.Second, 30*time.Second, true, 1, 0, 0, 0)
	if err == nil {
		t.Fatal("an interface binding that cannot be applied was silently ignored")
	}
	if !strings.Contains(err.Error(), name) {
		t.Errorf("the error does not name the interface: %v", err)
	}
}

// The rendering is what goes in the log line that tells the operator which way
// the tunnel is leaving, so it has to mention everything that was configured —
// and still hide the proxy password.
func TestOutboundString(t *testing.T) {
	p, err := ParseProxy("socks5://bob:s3cret@127.0.0.1:1080")
	if err != nil {
		t.Fatal(err)
	}
	o := &Outbound{Proxy: p, LocalAddr: "10.0.0.2", Interface: "eth1", Mark: 42}
	got := o.String()

	for _, want := range []string{"127.0.0.1:1080", "10.0.0.2", "eth1", "42"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "s3cret") {
		t.Fatalf("String() leaked the proxy password: %q", got)
	}
}

// With a proxy configured the socket goes to the proxy; without one it goes to
// the server. Getting this backwards would dial the wrong host entirely.
func TestDialAddressFollowsTheProxy(t *testing.T) {
	direct := &Outbound{LocalAddr: "10.0.0.1"}
	if got := direct.dialAddress("server:9000"); got != "server:9000" {
		t.Errorf("without a proxy the dial went to %q", got)
	}

	viaProxy := &Outbound{Proxy: &ProxyConfig{Scheme: "socks5", Address: "127.0.0.1:1080"}}
	if got := viaProxy.dialAddress("server:9000"); got != "127.0.0.1:1080" {
		t.Errorf("with a proxy the dial went to %q, want the proxy", got)
	}
}

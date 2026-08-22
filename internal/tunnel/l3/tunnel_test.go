package l3

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// fakeDevice stands in for the TUN interface, which needs privileges and a
// Linux kernel. Packets pushed into inject come out of Read, as if the kernel
// had routed them into the device; packets the tunnel writes appear on
// emitted.
type fakeDevice struct {
	inject  chan []byte
	emitted chan []byte
	closed  chan struct{}
	once    sync.Once

	mtuMu sync.Mutex
	mtu   int
}

func newFakeDevice(mtu int) *fakeDevice {
	return &fakeDevice{
		inject:  make(chan []byte, 1024),
		emitted: make(chan []byte, 1024),
		closed:  make(chan struct{}),
		mtu:     mtu,
	}
}

func (d *fakeDevice) Read(bufs [][]byte, sizes []int) (int, error) {
	select {
	case pkt := <-d.inject:
		sizes[0] = copy(bufs[0], pkt)
		return 1, nil
	case <-d.closed:
		return 0, io.EOF
	}
}

// Write blocks when the output is full rather than dropping, which is what a
// real device does and what stops a test from silently losing packets. Close
// unblocks it, so a test that never drains still tears down.
func (d *fakeDevice) Write(bufs [][]byte) (int, error) {
	for _, p := range bufs {
		packet := append([]byte(nil), p...)
		select {
		case d.emitted <- packet:
		case <-d.closed:
			return 0, io.EOF
		}
	}
	return len(bufs), nil
}

func (d *fakeDevice) Close() error {
	d.once.Do(func() { close(d.closed) })
	return nil
}
func (d *fakeDevice) Name() string { return "fake0" }

// BatchSize is 1: the fake moves one packet at a time, which keeps the tests
// about the engine rather than about batching. The batched path is exercised by
// the real device and by TestPumpDrainsAWholeBatch.
func (d *fakeDevice) BatchSize() int { return 1 }
func (d *fakeDevice) MTU() int {
	d.mtuMu.Lock()
	defer d.mtuMu.Unlock()
	return d.mtu
}

// SetMTU records what the prober asked for, so a test can assert the measured
// figure reached the device rather than only being logged.
func (d *fakeDevice) SetMTU(mtu int) error {
	d.mtuMu.Lock()
	d.mtu = mtu
	d.mtuMu.Unlock()
	return nil
}

// settledMTU is what SetMTU last stored.
func (d *fakeDevice) settledMTU() int {
	d.mtuMu.Lock()
	defer d.mtuMu.Unlock()
	return d.mtu
}

// receive waits for one packet out of the device.
func (d *fakeDevice) receive(t *testing.T, within time.Duration) []byte {
	t.Helper()
	select {
	case pkt := <-d.emitted:
		return pkt
	case <-time.After(within):
		t.Fatalf("no packet came out of the device within %s", within)
		return nil
	}
}

func quietLogger() *logrus.Logger {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return log
}

// start runs a tunnel in the background and registers its teardown.
//
// The teardown cancels before it waits, which is the only order that
// terminates. Closing the device alone stops the two pumps but leaves the
// handshake loop retrying, so a teardown that waited first would hang — which
// it did, until it was written this way.
// dev is any closer, so a test can stand in a device with different behaviour
// — a batched one, a slow one — without a second copy of this.
func start(t *testing.T, ctx context.Context, cancel context.CancelFunc, tun *Tunnel, dev io.Closer) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); _ = tun.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		dev.Close()
		<-done
	})
}

// awaitBind waits for the carrier to report the port it actually got, which is
// how a test using port 0 finds out where to dial.
func awaitBind(t *testing.T, tun *Tunnel) net.Addr {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if addr := tun.LocalAddr(); addr != nil {
			return addr
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the tunnel never bound a port")
	return nil
}

func awaitSession(t *testing.T, tun *Tunnel, within time.Duration) {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); {
		if tun.sendSession() != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no session was established within %s", within)
}

// pair brings up a listener and a dialler talking over real UDP on loopback,
// each with a fake device in place of a TUN interface.
type pair struct {
	dialer, listener   *Tunnel
	dialDev, listenDev *fakeDevice
}

func newPair(t *testing.T, encap string, greKey uint32, dialToken, listenToken string) *pair {
	t.Helper()

	p := &pair{
		dialDev:   newFakeDevice(1400),
		listenDev: newFakeDevice(1400),
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var err error
	p.listener, err = New(Config{
		Mode: ModeListen, Addr: "127.0.0.1:0", Token: listenToken,
		Encap: encap, GREKey: greKey,
		LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1400,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(listener): %v", err)
	}
	p.listener.openDevice = func(deviceSpec) (packetDevice, error) {
		return p.listenDev, nil
	}
	start(t, ctx, cancel, p.listener, p.listenDev)

	bound := awaitBind(t, p.listener)

	p.dialer, err = New(Config{
		Mode: ModeDial, Addr: bound.String(), Token: dialToken,
		Encap: encap, GREKey: greKey,
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(dialer): %v", err)
	}
	p.dialer.openDevice = func(deviceSpec) (packetDevice, error) {
		return p.dialDev, nil
	}
	start(t, ctx, cancel, p.dialer, p.dialDev)

	return p
}

// established is a pair whose handshake has completed.
func established(t *testing.T, encap string, greKey uint32) *pair {
	t.Helper()
	const token = "an-end-to-end-token"
	p := newPair(t, encap, greKey, token, token)
	awaitSession(t, p.dialer, 5*time.Second)
	return p
}

// across pushes a packet into one device and expects it out of the other.
func across(t *testing.T, from, to *fakeDevice, packet []byte) {
	t.Helper()
	from.inject <- packet
	got := to.receive(t, 5*time.Second)
	if !bytes.Equal(got, packet) {
		t.Fatalf("packet changed in transit:\n got %x\nwant %x", got, packet)
	}
}

func TestTunnelCarriesPacketsBothWays(t *testing.T) {
	for _, tc := range []struct {
		name   string
		encap  string
		greKey uint32
	}{
		{"ipip", "ipip", 0},
		{"gre", "gre", 0},
		{"gre keyed", "gre", 0x0BADCAFE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := established(t, tc.encap, tc.greKey)

			across(t, p.dialDev, p.listenDev, ipv4Packet(1, 2, 3, 4))
			across(t, p.listenDev, p.dialDev, ipv4Packet(5, 6, 7, 8))

			// IPv6 rides the same tunnel, with no separate mode for it.
			across(t, p.dialDev, p.listenDev, ipv6Packet(0xAA, 0xBB))
			across(t, p.listenDev, p.dialDev, ipv6Packet(0xCC, 0xDD))
		})
	}
}

// Once the listener has heard from the dialler it must be able to originate
// traffic too, which is what proves it learned where its peer is.
func TestTunnelListenerLearnsItsPeer(t *testing.T) {
	p := established(t, "ipip", 0)

	across(t, p.dialDev, p.listenDev, ipv4Packet(0x11))
	for i := 0; i < 5; i++ {
		across(t, p.listenDev, p.dialDev, ipv4Packet(byte(i)))
	}
}

func TestTunnelCarriesManyPacketsInOrder(t *testing.T) {
	p := established(t, "ipip", 0)

	const count = 400
	for i := 0; i < count; i++ {
		p.dialDev.inject <- ipv4Packet(byte(i>>8), byte(i))
	}
	for i := 0; i < count; i++ {
		got := p.listenDev.receive(t, 10*time.Second)
		want := ipv4Packet(byte(i>>8), byte(i))
		if !bytes.Equal(got, want) {
			t.Fatalf("packet %d = %x, want %x", i, got, want)
		}
	}
}

// A packet the size of the interface MTU has to fit, or the buffer arithmetic
// is wrong in the direction that silently loses large flows.
func TestTunnelCarriesAFullSizePacket(t *testing.T) {
	p := established(t, "gre", 0x1234)

	packet := ipv4Packet()
	for len(packet) < 1400 {
		packet = append(packet, byte(len(packet)))
	}
	across(t, p.dialDev, p.listenDev, packet[:1400])
}

// Neither end may establish anything when the tokens differ.
func TestTunnelRejectsTheWrongToken(t *testing.T) {
	p := newPair(t, "ipip", 0, "a-different-token", "the-right-token")

	time.Sleep(1500 * time.Millisecond)

	if p.dialer.sendSession() != nil {
		t.Fatal("a dialler with the wrong token established a session")
	}
	if p.listener.sendSession() != nil {
		t.Fatal("the listener accepted a handshake made with the wrong token")
	}
}

// Both ends must agree on the encapsulation. A mismatch establishes a session
// — the token is right — but every packet must then be discarded rather than
// written into the interface as garbage.
// Two ends that wrap packets differently must not form a tunnel at all.
//
// They used to. The handshake carried no encapsulation, so the keys matched and
// the session came up on both machines: "session established" on one, "session
// confirmed" on the other, a peer in the metrics file, a green card on the
// panel. Every data packet then decrypted perfectly and was discarded one layer
// later, because an IPIP sender's payload is an IP packet and a GRE receiver
// reads its first four bytes as a GRE header.
//
// That was found in the field. Both ends reported a healthy tunnel, no error
// appeared above debug level, and the interface counters showed zero packets
// received on both machines — a tunnel that was up and carried nothing.
func TestTunnelRefusesToPairMismatchedEncapsulation(t *testing.T) {
	const token = "matching-token"
	dialDev := newFakeDevice(1400)
	listenDev := newFakeDevice(1400)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	listener, err := New(Config{
		Mode: ModeListen, Addr: "127.0.0.1:0", Token: token, Encap: "gre", GREKey: 99,
		LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1", MTU: 1400,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(listener): %v", err)
	}
	listener.openDevice = func(deviceSpec) (packetDevice, error) { return listenDev, nil }
	start(t, ctx, cancel, listener, listenDev)

	dialer, err := New(Config{
		Mode: ModeDial, Addr: awaitBind(t, listener).String(), Token: token, Encap: "ipip",
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
	}, quietLogger())
	if err != nil {
		t.Fatalf("New(dialer): %v", err)
	}
	dialer.openDevice = func(deviceSpec) (packetDevice, error) { return dialDev, nil }
	start(t, ctx, cancel, dialer, dialDev)

	// No session, on either end. This is the whole point: the failure has to
	// happen where somebody is looking, not silently in the data path.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if dialer.sendSession() != nil {
			t.Fatal("a tunnel formed between an ipip end and a gre end")
		}
		if listener.sendSession() != nil {
			t.Fatal("the listener installed a session for a peer it cannot understand")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// And nothing crosses, which was already true — but now for a reason that
	// was reported rather than one that was not.
	dialDev.inject <- ipv4Packet(1, 2, 3)
	select {
	case pkt := <-listenDev.emitted:
		t.Fatalf("a packet framed as ipip was written into a gre tunnel: %x", pkt)
	case <-time.After(500 * time.Millisecond):
	}
}

// The message has to name both sides. It is read on a machine that knows only
// its own half, so "the encapsulation does not match" would leave the operator
// to go and look at the other server to find out what it does not match.
func TestTheMismatchMessageNamesBothEnds(t *testing.T) {
	err := errEncapMismatch("ipip", "gre:99")
	for _, want := range []string{"ipip", "gre:99"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not mention %q: %v", want, err)
		}
	}
}

// A GRE key mismatch fails in exactly the same silent way as an encapsulation
// mismatch, so it is part of the same identifier.
func TestTheKeyIsPartOfTheIdentity(t *testing.T) {
	plain, _ := NewEncap("gre", 0)
	keyed, _ := NewEncap("gre", 4242)
	other, _ := NewEncap("gre", 9999)
	ipip, _ := NewEncap("ipip", 0)

	if encapID(plain) == encapID(keyed) {
		t.Error("a keyed and an unkeyed gre tunnel announce the same identity")
	}
	if encapID(keyed) == encapID(other) {
		t.Error("two different gre keys announce the same identity")
	}
	if encapID(ipip) == encapID(plain) {
		t.Error("ipip and gre announce the same identity")
	}
	if encapID(keyed) != "gre:4242" {
		t.Errorf("keyed identity = %q", encapID(keyed))
	}
}

// An older peer sends no identifier at all. It must still be able to connect,
// or an upgrade would take the tunnel down until both ends were done.
func TestAPeerThatAnnouncesNothingIsNotJudged(t *testing.T) {
	const token = "matching-token"

	// A handshake built the old way: no payload.
	attempt, err := beginHandshake(token, 0, "")
	if err != nil {
		t.Fatalf("beginHandshake: %v", err)
	}
	sess, reply, err := respond(token, attempt.id, attempt.msg, "gre:7")
	if err != nil {
		t.Fatalf("a peer announcing nothing was refused: %v", err)
	}
	if sess == nil || reply == nil {
		t.Fatal("no session came out of a handshake that should have succeeded")
	}
}

func TestTunnelStatsCountTraffic(t *testing.T) {
	p := established(t, "ipip", 0)

	packet := ipv4Packet(1, 2, 3, 4)
	across(t, p.dialDev, p.listenDev, packet)

	out := p.dialer.Stats()
	if out.PacketsOut != 1 || out.BytesOut != uint64(len(packet)) {
		t.Fatalf("dialler sent %d packets / %d bytes, want 1 / %d",
			out.PacketsOut, out.BytesOut, len(packet))
	}
	in := p.listener.Stats()
	if in.PacketsIn != 1 || in.BytesIn != uint64(len(packet)) {
		t.Fatalf("listener received %d packets / %d bytes, want 1 / %d",
			in.PacketsIn, in.BytesIn, len(packet))
	}
	if in.Handshakes == 0 {
		t.Fatal("the listener recorded no handshake")
	}
}

func TestNewRejectsBadConfigurations(t *testing.T) {
	base := func() Config {
		return Config{
			Mode: ModeDial, Addr: "1.2.3.4:9000", Token: "token",
			LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2",
		}
	}
	cases := map[string]func(*Config){
		"no mode":                func(c *Config) { c.Mode = "" },
		"unknown mode":           func(c *Config) { c.Mode = "sideways" },
		"no address":             func(c *Config) { c.Addr = "" },
		"address without a port": func(c *Config) { c.Addr = "1.2.3.4" },
		"no token":               func(c *Config) { c.Token = "" },
		"unknown encapsulation":  func(c *Config) { c.Encap = "vxlan" },
		"no local address":       func(c *Config) { c.LocalIP = "" },
		"malformed local":        func(c *Config) { c.LocalIP = "not-an-address" },
		"peer carrying a prefix": func(c *Config) { c.PeerIP = "10.10.0.2/30" },
		"bare local, no peer":    func(c *Config) { c.LocalIP = "10.10.0.1"; c.PeerIP = "" },
		"mtu below the floor":    func(c *Config) { c.MTU = 100 },
		"mtu above the ceiling":  func(c *Config) { c.MTU = 100000 },
	}
	for name, mutate := range cases {
		cfg := base()
		mutate(&cfg)
		if _, err := New(cfg, quietLogger()); err == nil {
			t.Fatalf("%s: New accepted the configuration", name)
		}
	}
}

// An unimplemented carrier has to be refused rather than quietly run over UDP,
// which would look like it worked until the path filtered it.
func TestUnknownCarrierIsRefused(t *testing.T) {
	cfg := Config{
		Mode: ModeDial, Addr: "1.2.3.4:9000", Token: "token",
		Carrier: "smoke-signal",
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2",
	}
	if _, err := New(cfg, quietLogger()); err == nil {
		t.Fatal("New accepted a carrier it does not implement")
	}
	if _, _, err := openCarrier(cfg); err == nil {
		t.Fatal("openCarrier accepted a carrier it does not implement")
	}
}

// Every carrier the config accepts must also be openable, or a name would
// validate and then fail at a much less helpful moment.
func TestKnownCarriersValidate(t *testing.T) {
	for _, name := range []string{CarrierUDP, CarrierPck, CarrierXdi, CarrierSpoof} {
		cfg := Config{
			Mode: ModeDial, Addr: "1.2.3.4:9000", Token: "token",
			Carrier: name,
			LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2",
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("carrier %q was refused by validation: %v", name, err)
		}
	}
}

// The forged-source carrier's listening side cannot learn where its peer
// really is, so a configuration that leaves it out has to be refused up front.
func TestSpoofListenerRequiresPeerIP(t *testing.T) {
	cfg := Config{
		Mode: ModeListen, Addr: "0.0.0.0:9000", Token: "token",
		Carrier: CarrierSpoof,
		LocalIP: "10.10.0.2/30", PeerIP: "10.10.0.1",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("a spoof listener without spoof_peer_ip was accepted")
	}

	cfg.Spoof.SpoofPeerIP = "203.0.113.9"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a spoof listener with spoof_peer_ip was refused: %v", err)
	}

	// The dialling side takes it from the address it was told to reach.
	dial := Config{
		Mode: ModeDial, Addr: "203.0.113.9:9000", Token: "token",
		Carrier: CarrierSpoof,
		LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2",
	}
	if err := dial.Validate(); err != nil {
		t.Fatalf("a spoof dialler without spoof_peer_ip was refused: %v", err)
	}
	peer, err := spoofRealPeer(dial)
	if err != nil {
		t.Fatalf("spoofRealPeer: %v", err)
	}
	if peer.String() != "203.0.113.9" {
		t.Fatalf("spoofRealPeer = %s, want 203.0.113.9", peer)
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := Config{
		Mode: ModeListen, Addr: ":9000", Token: "token",
		LocalIP: "10.10.0.1/30",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.Encap != "ipip" {
		t.Fatalf("Encap defaulted to %q, want ipip", cfg.Encap)
	}
	if cfg.Carrier != "udp" {
		t.Fatalf("Carrier defaulted to %q, want udp", cfg.Carrier)
	}
	if cfg.Iface != defaultIfaceName {
		t.Fatalf("Iface defaulted to %q, want %q", cfg.Iface, defaultIfaceName)
	}
	if cfg.MTU != defaultMTU {
		t.Fatalf("MTU defaulted to %d, want %d", cfg.MTU, defaultMTU)
	}
}

func TestMTUFor(t *testing.T) {
	// udp over IPv4 costs 28; ipip costs nothing; the session costs 29.
	if got := MTUFor(1500, 28, 0); got != 1443 {
		t.Fatalf("MTUFor(1500, 28, 0) = %d, want 1443", got)
	}
	// gre with a key costs eight more.
	if got := MTUFor(1500, 28, 8); got != 1435 {
		t.Fatalf("MTUFor(1500, 28, 8) = %d, want 1435", got)
	}
}

// A generation that loses one of its two devices has to end, so that the
// restart loop in cmd/l3.go can build another one.
//
// This is the regression test for a tunnel that stayed down until the process
// was restarted by hand. The pumps stopped when their device failed, but the
// handshake and probe loops watched the caller's context — which outlives every
// generation — so they went on running against a closed carrier and held Run
// inside wg.Wait() forever. Nothing was ever reopened, while the handshake loop
// logged a retry every few seconds and made it look like the tunnel was busy
// reconnecting.
//
// Each side is held open by a different loop, so both are covered: the dialling
// side by the handshake loop, the listening side by the probe loop.
func TestRunReturnsWhenTheDeviceFails(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		addr    string
		autoMTU bool
	}{
		{"dialling side, held open by the handshake loop", ModeDial, "127.0.0.1:9", false},
		{"listening side, held open by the probe loop", ModeListen, "127.0.0.1:0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tun, err := New(Config{
				Mode: tc.mode, Addr: tc.addr, Token: "a-token-for-the-regression",
				LocalIP: "10.10.0.1/30", PeerIP: "10.10.0.2", MTU: 1400,
				AutoMTU: tc.autoMTU,
			}, quietLogger())
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			dev := newFakeDevice(1400)
			tun.openDevice = func(deviceSpec) (packetDevice, error) { return dev, nil }

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan error, 1)
			go func() { done <- tun.Run(ctx) }()
			awaitBind(t, tun)

			// The device goes away and the carrier does not, which is what a
			// real device failure looks like: nothing closes the socket, so the
			// receive pump stays blocked on a read until something ends the
			// generation for it.
			dev.Close()

			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("Run did not return after the device failed: the tunnel can never be rebuilt, " +
					"and stays down until the process is restarted by hand")
			}
		})
	}
}

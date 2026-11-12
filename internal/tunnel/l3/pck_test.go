package l3

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tunnel/paqet"
	"github.com/sirupsen/logrus"
)

// What a v1.9.0 Iran-side pck file looked like, as the setup wrote it: every key
// for the TUN device, the carrier's sockets and the tuning of a preset.
const oldPckIranFile = `
[l3]
mode = "dial"
addr = "203.0.113.9:9000"
token = "an-old-and-long-shared-token"
carrier = "pck"
encap = "gre"
iface = "bp0"
local_ip = "10.10.0.1/30"
peer_ip = "10.10.0.2"
mtu = 1400
sockbuf = 8388608
txqueuelen = 4096
qdisc = "fq_codel"
preset = "turbo"
fec_data = 10
fec_parity = 3
auto_mtu = true
mss_clamp = 1300
pck_interface = "eth0"
pck_flags = ["PA", "A"]
ports = ["443=8443", "10000-10002", "8080=10.10.0.2:80"]
accept_udp = true
max_connections = 500
bandwidth_mbps = 80
`

const oldPckKharejFile = `
[l3]
mode = "listen"
addr = "0.0.0.0:9000"
token = "an-old-and-long-shared-token"
carrier = "pck"
iface = "bp0"
local_ip = "10.10.0.2/30"
peer_ip = "10.10.0.1"
mtu = 1400
preset = "balance"
sockbuf = 4194304
`

func parseL3(t *testing.T, text string) Config {
	t.Helper()
	var doc struct {
		L3 config.L3Config `toml:"l3"`
	}
	if _, err := toml.Decode(text, &doc); err != nil {
		t.Fatal(err)
	}
	cfg, err := ConfigFromTable(doc.L3)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestAFileWrittenForTheOldPCKRunsOnTheNewEngineWithNoEdit(t *testing.T) {
	cfg := parseL3(t, oldPckIranFile)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the old file is refused: %v", err)
	}
	tun, err := New(cfg, logrus.New())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !tun.UsesStreams() {
		t.Fatal("a pck tunnel must serve its ports through the engine's streams")
	}
	pc := tun.pck.Config()

	// Kept: ports (checked below), accept_udp, token, addresses, preset, flags,
	// the interface override and the capture ring (sockbuf).
	if pc.Server || pc.Addr != "203.0.113.9:9000" || pc.Token != "an-old-and-long-shared-token" {
		t.Errorf("addresses or role lost: %+v", pc)
	}
	if pc.Interface != "eth0" || len(pc.Flags) != 2 {
		t.Errorf("pck_interface / pck_flags lost: %q %v", pc.Interface, pc.Flags)
	}
	turbo, _ := paqet.PresetParams("turbo")
	if pc.Params.Conns != turbo.Conns || pc.Params.Mode != turbo.Mode || pc.Params.SndWnd != turbo.SndWnd {
		t.Errorf("the preset label did not pick Turbo's engine parameters: %+v", pc.Params)
	}
	if pc.Params.PcapBuf != 8<<20 {
		t.Errorf("sockbuf (8 MiB) should be the capture ring, got %d", pc.Params.PcapBuf)
	}
	if pc.Params.MTU == 1400 {
		t.Error("the TUN's mtu of 1400 must not leak into the KCP packet size")
	}
	if !cfg.AcceptUDP || cfg.MaxConnections != 500 || cfg.BandwidthMbps != 80 {
		t.Errorf("forwarder settings lost: %+v", cfg)
	}

	// The keys that no longer apply are named once at start, not silently.
	notes := strings.Join(PckNotes(cfg), "\n")
	if !strings.Contains(notes, "fec_data") {
		t.Errorf("the ignored FEC was not mentioned: %q", notes)
	}

	kh := parseL3(t, oldPckKharejFile)
	if err := kh.Validate(); err != nil {
		t.Fatalf("the old kharej file is refused: %v", err)
	}
	ktun, err := New(kh, logrus.New())
	if err != nil {
		t.Fatal(err)
	}
	kc := ktun.pck.Config()
	bal, _ := paqet.PresetParams("balance")
	if !kc.Server || kc.Params.Conns != bal.Conns || kc.Params.Mode != bal.Mode || kc.Params.PcapBuf != 4<<20 {
		t.Errorf("kharej side: %+v", kc)
	}
	if len(PckNotes(kh)) != 0 {
		t.Errorf("a kharej side with no ports should have nothing to say: %v", PckNotes(kh))
	}
}

func TestMigratedPortsReachTheSameServiceOnTheFarServer(t *testing.T) {
	cfg := parseL3(t, oldPckIranFile)
	f, err := NewForwarder(cfg, logrus.New())
	if err != nil || f == nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	var got []string
	for _, m := range f.mappings {
		got = append(got, m.Listen+" -> "+strings.Join(m.Targets, "|"))
	}
	want := []string{":443 -> 127.0.0.1:8443", ":10000 -> 127.0.0.1:10000", ":10001 -> 127.0.0.1:10001", ":10002 -> 127.0.0.1:10002", ":8080 -> 10.10.0.2:80"}
	if strings.Join(got, ";") != strings.Join(want, ";") {
		t.Errorf("mappings\n got %v\nwant %v", got, want)
	}
	// The dial then rewrites the old far-end address (peer_ip) to the far
	// server's own loopback, which is where the kernel delivered it before.
	for in, out := range map[string]string{
		"127.0.0.1:8443":   "127.0.0.1:8443",
		"10.10.0.2:80":     "127.0.0.1:80",
		":9":               "127.0.0.1:9",
		"192.0.2.7:80":     "192.0.2.7:80",
		"[2001:db8::1]:80": "[2001:db8::1]:80",
	} {
		if got := pckTarget(cfg, in); got != out {
			t.Errorf("pckTarget(%q) = %q, want %q", in, got, out)
		}
	}
}

func TestTheNewKeysRoundTripThroughTheFile(t *testing.T) {
	in := config.L3Config{
		Mode: "dial", Addr: "1.2.3.4:9000", Token: "t", Carrier: "pck", Preset: "aggressive",
		PckConns: 3, PckKCPMode: "fast3", PckKCPMTU: 1300, PckKCPSndWnd: 2048, PckKCPRcvWnd: 4096, PckKCPBlock: "aes-128-gcm",
		SockBuf: 16 << 20,
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(map[string]any{"l3": in}); err != nil {
		t.Fatal(err)
	}
	cfg := parseL3(t, buf.String())
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	p := mustEngineConfig(t, cfg).Params
	if p.Conns != 3 || p.Mode != "fast3" || p.MTU != 1300 || p.SndWnd != 2048 || p.RcvWnd != 4096 ||
		p.Block != "aes-128-gcm" || p.PcapBuf != 16<<20 {
		t.Errorf("explicit keys did not survive the file: %+v", p)
	}
	// And the rest of an explicit file still comes from its preset.
	ag, _ := paqet.PresetParams("aggressive")
	if p.SmuxBuf != ag.SmuxBuf || p.StreamBuf != ag.StreamBuf {
		t.Errorf("smux buffers should follow the preset: %+v", p)
	}
}

func mustEngineConfig(t *testing.T, cfg Config) paqet.Config {
	t.Helper()
	pc, err := PckEngineConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return pc
}

func TestAPckFileThatCannotWorkIsRefusedInWordsThatSayWhatToChange(t *testing.T) {
	base := func() Config {
		return Config{Mode: ModeDial, Addr: "1.2.3.4:9000", Token: "tok", Carrier: CarrierPck, Ports: []string{"443"}}
	}
	for name, tc := range map[string]struct {
		mut  func(*Config)
		want string
	}{
		"bad mode":          {func(c *Config) { c.PckKCPMode = "warp" }, "pck_kcp_mode"},
		"too many sessions": {func(c *Config) { c.PckConns = 99 }, "pck_conns"},
		"mtu too big":       {func(c *Config) { c.PckKCPMTU = 1500 }, "pck_kcp_mtu"},
		"unknown cipher":    {func(c *Config) { c.PckKCPBlock = "rot13" }, "pck_kcp_block"},
		"no token":          {func(c *Config) { c.Token = "" }, "token"},
		"ipv6 peer":         {func(c *Config) { c.Addr = "[2001:db8::1]:9000" }, "IPv4"},
		"no port in addr":   {func(c *Config) { c.Addr = "1.2.3.4" }, "host:port"},
	} {
		c := base()
		tc.mut(&c)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want it to mention %q", name, err, tc.want)
		}
	}
	// A pck tunnel needs none of what the TUN needed.
	c := base()
	c.LocalIP, c.PeerIP, c.MTU, c.Iface = "", "", 0, ""
	if err := c.Validate(); err != nil {
		t.Errorf("local_ip, peer_ip, mtu and iface are not read by pck: %v", err)
	}
	// And the same two addresses that the TUN refused are of no interest to it.
	c.LocalIP, c.PeerIP = "10.10.2.2/30", "10.10.2.2"
	if err := c.Validate(); err != nil {
		t.Errorf("identical tunnel addresses are not a pck problem: %v", err)
	}
}

func TestAPckTunnelWithNoPortsSaysItCarriesNothing(t *testing.T) {
	c := Config{Mode: ModeDial, Addr: "1.2.3.4:9000", Token: "tok", Carrier: CarrierPck}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(PckNotes(c), " ")
	if !strings.Contains(notes, "no ports") || !strings.Contains(notes, "xdi") {
		t.Errorf("an old routed pck tunnel must be told what happened to it: %q", notes)
	}
}

func TestTheOldPCKHandshakeIsWhatTheNewEngineRecognises(t *testing.T) {
	// Built by this package's own handshake, so a change to the header or the
	// version announcement that the engine's sniffer has not followed fails here.
	const token = "recognise-me"
	h, err := beginHandshake(token, 0, "gre")
	if err != nil {
		t.Fatal(err)
	}
	if !paqet.LooksLikeOldPck(h.datagram()) {
		t.Error("a current dialler's first datagram is not recognised as the old PCK")
	}
	fresh, err := beginHandshakeFresh(token, 0, "gre", 1234567890)
	if err != nil {
		t.Fatal(err)
	}
	dg := fresh.datagram()
	if !paqet.LooksLikeOldPck(dg) {
		t.Error("a timestamped handshake is not recognised")
	}
	_, reply, _, err := respondFresh(token, 7, versionCurrent, dg[headerLen:], "gre")
	if err != nil {
		t.Fatal(err)
	}
	if !paqet.LooksLikeOldPck(reply) {
		t.Error("the responder's reply is not recognised")
	}
	// A sealed data datagram is not a handshake and is left to KCP.
	hdr := header{kind: typeData, session: 1, counter: 5}.bytes()
	if paqet.LooksLikeOldPck(append(hdr[:], make([]byte, 64)...)) {
		t.Error("a data message was taken for a handshake")
	}
}

func TestAPckTunnelThatHasNotRunIsDownAndCountsNothing(t *testing.T) {
	cfg := parseL3(t, oldPckIranFile)
	tun, err := New(cfg, logrus.New())
	if err != nil {
		t.Fatal(err)
	}
	if tun.Up() {
		t.Error("a tunnel that never ran is up")
	}
	if s := tun.Stats(); s != (Stats{}) {
		t.Errorf("stats of a tunnel that never ran: %+v", s)
	}
	if tun.LocalAddr() != nil {
		t.Error("a pck tunnel has no carrier address")
	}
	// Dialling through a tunnel that is not up is an error the forwarder
	// reports, not a hang.
	_, err = tun.StreamDialer()(context.Background(), "tcp", "127.0.0.1:80", nil)
	if err == nil || !strings.Contains(err.Error(), "not up") {
		t.Errorf("dial through a down tunnel: %v", err)
	}
}

// The forwarder serves its ports through whatever dial it is given, which is
// how a pck tunnel hands it streams.
func TestTheForwarderUsesTheTunnelsDialWhenItIsGivenOne(t *testing.T) {
	echo := echoTCP(t, "")
	var dials atomic.Int32
	var gotNet, gotAddr atomic.Value
	cfg := Config{Mode: ModeDial, Addr: "1.2.3.4:9000", Token: "tok", Carrier: CarrierPck,
		Ports: []string{"127.0.0.1:0=" + echo}}
	// Port 0 would not tell us the port; pick a free one the usual way.
	port := freeTCPPort(t)
	cfg.Ports = []string{"127.0.0.1:" + port + "=" + echo}
	f, err := NewForwarder(cfg, logrus.New())
	if err != nil || f == nil {
		t.Fatal(err)
	}
	f.SetDialer(func(ctx context.Context, network, addr string, report func(error)) (net.Conn, error) {
		dials.Add(1)
		gotNet.Store(network)
		gotAddr.Store(addr)
		var d net.Dialer
		c, err := d.DialContext(ctx, network, addr)
		if err == nil && report != nil {
			report(nil)
		}
		return c, err
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.Run(ctx)
	select {
	case <-f.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("the forwarder did not bind")
	}
	c, err := net.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("hello"))
	buf := make([]byte, 5)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("echo through the custom dial: %q, %v", buf, err)
	}
	if dials.Load() != 1 || gotNet.Load() != "tcp" || gotAddr.Load() != echo {
		t.Errorf("dial calls=%d net=%v addr=%v", dials.Load(), gotNet.Load(), gotAddr.Load())
	}
}

// pendingConn is a connection whose verdict on the target arrives later, as a
// pck stream's does.
type pendingConn struct {
	net.Conn
	pending bool
}

func (p *pendingConn) StatusPending() bool { return p.pending }

func TestAConnectionWhoseVerdictIsPendingIsNotCalledAnsweringYet(t *testing.T) {
	var logBuf bytes.Buffer
	log := logrus.New()
	log.SetOutput(&logBuf)
	log.SetLevel(logrus.InfoLevel)
	cfg := Config{Mode: ModeDial, Addr: "1.2.3.4:9000", Token: "tok", Carrier: CarrierPck}
	port := freeTCPPort(t)
	cfg.Ports = []string{"127.0.0.1:" + port + "=127.0.0.1:9"}
	f, _ := NewForwarder(cfg, log)
	m := f.mappings[0]
	// A refusal the stream reports after the dial returned.
	f.noteBackend(m, nil) // establishes state
	f.noteBackend(m, io.ErrClosedPipe)
	f.noteBackend(m, nil)
	out := logBuf.String()
	if !strings.Contains(out, "nothing is answering") || !strings.Contains(out, "answering again") {
		t.Errorf("the reported verdicts did not drive the same log lines a dial would:\n%s", out)
	}
	var _ interface{ StatusPending() bool } = (*pendingConn)(nil)
}

func freeTCPPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, p, _ := net.SplitHostPort(l.Addr().String())
	return p
}

func TestPortsOnTheListeningSideOfAPckTunnelAreIgnoredWithANoteNotRefused(t *testing.T) {
	c := Config{Mode: ModeListen, Addr: ":9000", Token: "tok", Carrier: CarrierPck, Ports: []string{"443"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("a migrated file must still start: %v", err)
	}
	if n := strings.Join(PckNotes(c), " "); !strings.Contains(n, "dialling side") {
		t.Errorf("the ignored ports were not mentioned: %q", n)
	}
	if f, err := NewForwarder(c, logrus.New()); err != nil || f != nil {
		t.Errorf("the listening side must not serve ports: %v, %v", f, err)
	}
}

package paqet

import (
	"crypto/sha256"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

func TestThreePresetsExistAndEveryOneIsValid(t *testing.T) {
	if got := Presets(); len(got) != 3 || got[0] != PresetBalance || got[1] != PresetTurbo || got[2] != PresetAggressive {
		t.Fatalf("presets = %v", got)
	}
	for _, name := range Presets() {
		p, ok := PresetParams(name)
		if !ok {
			t.Fatalf("%s: no params", name)
		}
		r := p.Resolve(name)
		if err := r.Validate(); err != nil {
			t.Errorf("%s does not validate: %v", name, err)
		}
	}
}

func TestThePresetsTradeMemoryForSpeedInOrder(t *testing.T) {
	b, _ := PresetParams(PresetBalance)
	tu, _ := PresetParams(PresetTurbo)
	ag, _ := PresetParams(PresetAggressive)
	less := func(name string, x, y, z int) {
		t.Helper()
		if !(x < y && y < z) {
			t.Errorf("%s should rise Balance < Turbo < Aggressive, got %d, %d, %d", name, x, y, z)
		}
	}
	less("conns", b.Conns, tu.Conns, ag.Conns)
	less("sndwnd", b.SndWnd, tu.SndWnd, ag.SndWnd)
	less("smux buffer", b.SmuxBuf, tu.SmuxBuf, ag.SmuxBuf)
	less("stream buffer", b.StreamBuf, tu.StreamBuf, ag.StreamBuf)
	less("capture ring", b.PcapBuf, tu.PcapBuf, ag.PcapBuf)
	if b.Mode != ModeFast || tu.Mode != ModeFast2 || ag.Mode != ModeFast3 {
		t.Errorf("modes = %s, %s, %s", b.Mode, tu.Mode, ag.Mode)
	}
	// The capture ring is the sockbuf the setup already writes for each preset
	// (internal/manage/l3preset.go), so a migrated file asks for what it gets.
	if b.PcapBuf != 4<<20 || tu.PcapBuf != 8<<20 || ag.PcapBuf != 32<<20 {
		t.Errorf("capture rings %d, %d, %d do not match the sockbuf of the three presets", b.PcapBuf, tu.PcapBuf, ag.PcapBuf)
	}
	for _, p := range []Params{b, tu, ag} {
		if p.MTU != 1350 {
			t.Errorf("mtu %d: all presets keep 1350 (see PresetParams)", p.MTU)
		}
	}
}

func TestAnEmptyOrUnknownPresetIsTurbo(t *testing.T) {
	turbo, _ := PresetParams(PresetTurbo)
	if got, ok := PresetParams(""); !ok || got != turbo {
		t.Error("an empty preset is not Turbo")
	}
	if _, ok := PresetParams("warp"); ok {
		t.Error("an unknown preset name was accepted by PresetParams")
	}
	if got := (Params{}).Resolve("warp"); got.Conns != turbo.Conns || got.Mode != turbo.Mode {
		t.Errorf("Resolve of an unknown preset gave %+v", got)
	}
	if got := (Params{}).Resolve(" Aggressive "); got.Conns != 4 {
		t.Errorf("preset names are trimmed and case-insensitive; got conns %d", got.Conns)
	}
}

func TestExplicitValuesWinOverThePreset(t *testing.T) {
	got := Params{Conns: 7, Mode: ModeNormal, SndWnd: 99}.Resolve(PresetAggressive)
	if got.Conns != 7 || got.Mode != ModeNormal || got.SndWnd != 99 {
		t.Fatalf("explicit values were overwritten: %+v", got)
	}
	if got.RcvWnd != 4096 || got.Block != DefaultBlock || got.KeepAlive != 2*time.Second || got.KeepAliveTimeout != 8*time.Second {
		t.Fatalf("the rest should come from the preset and the fixed defaults: %+v", got)
	}
}

func TestValidationNamesTheKeyAndTheLimit(t *testing.T) {
	base, _ := PresetParams(PresetTurbo)
	base = base.Resolve("")
	cases := []struct {
		name string
		mut  func(*Params)
		want string
	}{
		{"conns", func(p *Params) { p.Conns = 17 }, "pck_conns 17"},
		{"conns zero", func(p *Params) { p.Conns = 0 }, "pck_conns 0"},
		{"mode", func(p *Params) { p.Mode = "turbo" }, `pck_kcp_mode "turbo"`},
		{"mtu low", func(p *Params) { p.MTU = 100 }, "pck_kcp_mtu 100"},
		{"mtu high", func(p *Params) { p.MTU = 1500 }, "pck_kcp_mtu 1500"},
		{"sndwnd", func(p *Params) { p.SndWnd = 99999 }, "pck_kcp_sndwnd 99999"},
		{"rcvwnd", func(p *Params) { p.RcvWnd = -1 }, "pck_kcp_rcvwnd -1"},
		{"block", func(p *Params) { p.Block = "rot13" }, `pck_kcp_block "rot13"`},
		{"ring", func(p *Params) { p.PcapBuf = 10 }, "sockbuf 10"},
		{"fec half", func(p *Params) { p.DShard = 4 }, "both shard counts or neither"},
		{"manual", func(p *Params) { p.Mode = ModeManual }, "needs an interval"},
	}
	for _, c := range cases {
		p := base
		c.mut(&p)
		err := p.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want it to mention %q", c.name, err, c.want)
		}
	}
	// Several faults are all reported.
	p := base
	p.Conns, p.MTU = 0, 5
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "pck_conns") || !strings.Contains(err.Error(), "pck_kcp_mtu") {
		t.Errorf("two faults, one message each: %v", err)
	}
}

func TestConfigValidationSaysWhatIsMissing(t *testing.T) {
	p, _ := PresetParams(PresetTurbo)
	p = p.Resolve("")
	cases := []struct {
		name string
		c    Config
		want string
	}{
		{"no token", Config{Addr: "1.2.3.4:9000", Params: p}, "token is required"},
		{"no port", Config{Addr: "1.2.3.4", Token: "t", Params: p}, "host:port"},
		{"port zero", Config{Addr: "1.2.3.4:0", Token: "t", Params: p}, "needs a port"},
		{"no host on the client", Config{Addr: ":9000", Token: "t", Params: p}, "must name the other server"},
		{"bad params", Config{Addr: "1.2.3.4:9000", Token: "t", Params: Params{}}, "pck_conns"},
	}
	for _, c := range cases {
		err := c.c.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
	if err := (Config{Server: true, Addr: ":9000", Token: "t", Params: p}).Validate(); err != nil {
		t.Errorf("a listener with no host is the normal case: %v", err)
	}
}

func TestTheKeyIsDerivedFromTheTokenAndNotTheKeyPaqetWouldDerive(t *testing.T) {
	a, b := DeriveKey("token-one"), DeriveKey("token-one")
	if string(a) != string(b) || len(a) != 32 {
		t.Fatal("the derivation is not deterministic, or not 32 bytes")
	}
	if string(DeriveKey("token-two")) == string(a) {
		t.Fatal("two tokens gave one key")
	}
	stock := pbkdf2.Key([]byte("token-one"), []byte("paqet"), 100_000, 32, sha256.New)
	if string(stock) == string(a) {
		t.Fatal("the key equals stock paqet's: the two products would be confusable on the wire")
	}
}

func TestEveryCipherInTheListBuilds(t *testing.T) {
	key := DeriveKey("t")
	for _, name := range Blocks() {
		if _, err := newBlock(name, key); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if b, err := newBlock("null", key); b != nil || err != nil {
		t.Errorf("null is no cipher at all: %v, %v", b, err)
	}
	if _, err := newBlock("nope", key); err == nil {
		t.Error("an unknown cipher was built")
	}
}

func TestTheCaptureFilterMatchesOnlyTheTunnelsFlow(t *testing.T) {
	local, peer := net.ParseIP("10.0.0.1"), net.ParseIP("203.0.113.9")
	got := CaptureFilter(true, 9000, 9000, local, nil, 0)
	if got != "tcp and dst port 9000 and dst host 10.0.0.1" {
		t.Errorf("server filter: %q", got)
	}
	got = CaptureFilter(false, 33000, 33127, local, peer, 9000)
	want := "tcp and dst portrange 33000-33127 and dst host 10.0.0.1 and src host 203.0.113.9 and src port 9000"
	if got != want {
		t.Errorf("client filter: %q\nwant %q", got, want)
	}
}

func TestTheOldPCKHandshakeIsRecognisedAndKCPTrafficIsNot(t *testing.T) {
	mk := func(kind byte, counter byte, n int) []byte {
		b := make([]byte, n)
		b[0] = kind
		b[12] = counter
		return b
	}
	for _, tc := range []struct {
		name string
		in   []byte
		want bool
	}{
		{"init from a legacy dialler", mk(1, 0, 100), true},
		{"init announcing version 2", mk(1, 2, 100), true},
		{"response", mk(2, 0, 100), true},
		{"data message", mk(3, 0, 100), false},
		{"too short to be a handshake", mk(1, 0, 20), false},
		{"a counter that is not a version", func() []byte { b := mk(1, 0, 100); b[6] = 1; return b }(), false},
		{"empty", nil, false},
	} {
		if got := looksLikeOldPck(tc.in); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAPresetDescribesItselfInOneLine(t *testing.T) {
	p, _ := PresetParams(PresetBalance)
	if got, want := p.Resolve("").Summary(), "1 session, fast, 256-packet windows, 2 MiB buffers, 4 MiB capture"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	a, _ := PresetParams(PresetAggressive)
	if got := a.Resolve("").Summary(); !strings.HasPrefix(got, "4 sessions, fast3, 4096-packet windows") {
		t.Errorf("got %q", got)
	}
}

package paqet

// KCP block ciphers and mode table derived from paqet (MIT); see doc.go.

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/utils/network"
	"github.com/xtaci/kcp-go/v5"
	"golang.org/x/crypto/pbkdf2"
)

// Preset names. They are the same three the other direct carriers offer, and
// the same strings the setup writes into a file's preset key.
const (
	PresetBalance    = "balance"
	PresetTurbo      = "turbo"
	PresetAggressive = "aggressive"
)

// Presets lists the preset names in the order the screens offer them.
func Presets() []string { return []string{PresetBalance, PresetTurbo, PresetAggressive} }

// KCP modes, as paqet names them. The numbers are kcp-go's NoDelay arguments:
// (nodelay, interval ms, fast resend, no congestion control).
//
//	normal  0, 40, 2, 1   write-delay on, ack batching — least CPU
//	fast    0, 30, 2, 1   write-delay on, ack batching — paqet's default
//	fast2   1, 20, 2, 1   write-delay on, ack batching, quicker timeouts
//	fast3   1, 10, 2, 1   write-delay on, ack batching, shortest tick
//	manual  the four numbers are taken from Params
//
// Write-delay and ack batching are on in all four, and that is a change from
// paqet, whose fast2 and fast3 turn them off for the lowest latency: every
// write then leaves as a packet of its own, and every packet received is
// answered by one. On a link with many short connections that is most of the
// packets, and the packet is what costs: the soak path's queue holds 1000 of
// them whatever their size, so the tens of thousands of 60-byte
// acknowledgements a second fill it as well as the data does. Same build, same
// windows, same load (100 Mbit/s, 30 ms, a mix of 8 bulk transfers and 200
// short connections a second): the mode that answered every packet carried
// 7 and 9 Mbit/s of bulk transfer, the batching mode 20 and 34; with bulk
// transfers alone, 62 and 42 against 62 and 68. The batching costs at most one
// tick (10-40 ms) of delay, which the window control reads as noise
// (congestion.go), and which is the round trip itself where the path's is
// shorter than the tick: there the window control turns batching off
// (batchingFor), since a single stream's speed is its window over the round
// trip and Turbo's one stream on a clean veth pair fell from 640 to 390 Mbit/s
// with it on. The modes now differ in the tick, in how soon a lost packet is
// sent again (nodelay) and in nothing else.
//
// KCP's congestion control is off in every one of them, as in paqet: it is
// loss-based, and its backoff under loss is what makes a lossy path unusable.
// What slows the sender on a shared link is the window control instead, which
// reads delay and not loss (congestion.go), and the window below is its ceiling.
const (
	ModeNormal = "normal"
	ModeFast   = "fast"
	ModeFast2  = "fast2"
	ModeFast3  = "fast3"
	ModeManual = "manual"
)

// Modes lists the KCP modes a file may name.
func Modes() []string { return []string{ModeNormal, ModeFast, ModeFast2, ModeFast3, ModeManual} }

// Params is everything that tunes the engine, apart from addresses and the
// token. The zero value means "use the preset"; Resolve fills it in.
type Params struct {
	// Conns is the number of KCP sessions the client holds open. Each is its
	// own TCP flow (its own source port) and its own KCP update loop, so more
	// of them spread load over cores and over a path that shapes per flow.
	// New streams go to the next live session in turn.
	Conns int

	Mode string
	// Manual mode only.
	NoDelay, Interval, Resend, NoCongestion int
	WDelay, AckNoDelay                      bool

	// KCP packet size (the TCP payload), window sizes in packets, and block
	// cipher. DShard and PShard are KCP's own FEC, off unless set.
	MTU, SndWnd, RcvWnd int
	Block               string
	DShard, PShard      int

	// SmuxBuf is smux's receive buffer for the whole session, StreamBuf for
	// one stream. Both are memory the session may hold when the reader is
	// slower than the wire.
	SmuxBuf, StreamBuf int

	KeepAlive, KeepAliveTimeout time.Duration

	// PcapBuf is the capture ring in bytes.
	PcapBuf int
}

// PresetParams maps a preset to engine parameters.
//
// # The mapping, and why
//
// What the three presets trade is memory and CPU for speed on a fast, lossy,
// long-latency path — Iran to abroad, typically 80-250 ms and a percent or two
// of loss. The levers, in the order they matter:
//
//   - Window (sndwnd/rcvwnd, in packets). It is the CEILING of the window
//     control (congestion.go), which moves the windows of a generation's
//     sessions between a small floor and this figure by what it reads of the
//     path, so a figure larger than the path needs costs nothing but the memory
//     the receive side may hold, and one that is too small is the speed limit:
//     throughput is at most window x MTU / RTT. 256 x 1350 B at 150 ms is
//     18 Mbit/s; 1024 is 74; 4096 is 295. The ceiling is per session. This is
//     the main memory dial.
//   - Mode. The tick is how often KCP's update loop wakes, and so how long an
//     acknowledgement or a small write may wait to be batched: 30 ms, 20 ms,
//     10 ms. A shorter tick costs idle CPU on every session; fast3 on a one-vCPU
//     VPS is a measurable share of it.
//   - Conns. One session is one goroutine's worth of KCP; 2 or 4 sessions
//     use more cores, give a per-flow shaper several flows to count rather
//     than one to throttle, and a loss on one holds up only the streams on
//     that one. More than that buys nothing and costs source ports. Four
//     sessions on Turbo's other values (pck_conns = 4) were measured to carry
//     more on a lossy route (10% loss: about 13/15 Mbit/s against 9/8, probe
//     p50 550 ms against 900) and no more on a clean one (a little less up),
//     and a path that flaps can recover slower; it is an opt-in, not Turbo's
//     default.
//   - smux buffers. Bound what a stalled reader pins: the stream buffer should
//     hold a few windows of data, the session buffer a few streams' worth. They
//     do not need to be sized against the number of streams: the receive
//     buffer that used to run out under hundreds of short connections and stop
//     the whole session for up to a minute no longer does, because a reader does
//     not wait for its window update (internal/thirdparty/smux).
//   - Capture ring (PcapBuf). Absorbs a burst the read loop has not reached.
//     A ring too small drops segments the sender believes were delivered, which
//     KCP then repairs at the cost of latency; a larger one costs the memory.
//
// MTU stays 1350 in all three. With the 20-byte IP header and 32 of TCP
// (timestamps) a segment is 1402 bytes, which clears a 1500-byte path and most
// of the places that are smaller. The segments carry the don't-fragment bit, so
// a figure that is too large is not slow, it is lost without a message, and
// the extra 50 bytes a larger MTU buys are not worth that risk for any preset.
//
//	              Balance   Turbo    Aggressive
//	mode          fast      fast2    fast3
//	conns         1         2        4
//	sndwnd/rcvwnd 256/512   1024     4096
//	mtu           1350      1350     1350
//	smux buffer   2 MiB     4 MiB    16 MiB
//	stream buffer 1 MiB     2 MiB    4 MiB
//	capture ring  4 MiB     8 MiB    32 MiB
//
// The capture ring matches the sockbuf the setup already writes for each
// preset, so a file made by an older version and read here gets the same
// memory it asked for.
//
// Balance is paqet's own default (fast, window 128-512 client, 1024 server)
// trimmed for memory; Turbo and Aggressive move up one mode and four times the
// window each. The server's receive window is not smaller than the client's:
// both directions carry bulk (a download is the kharej sending), so the
// windows are symmetric.
//
// # What the presets measured, on the soak path
//
// 100 Mbit/s each way, 30 ms +- 10 ms, a 1000-packet queue, 7 minutes of 8 bulk
// transfers, 200 short connections a second, idle connections, UDP flows and a
// latency probe; bulk Mbit/s down/up, probe latency p50 and the longest stall
// of a transfer (the numbers are in docs/direct-pck.md). Turbo is the default
// and what the control was tuned on. Balance is held to 256 packets, which at
// this path's 75 ms is about 37 Mbit/s for everything the tunnel carries, so
// under this load it has little left for bulk transfer; it is for a small
// VPS and a quiet tunnel, not for a saturated link. Aggressive's four sessions
// and 4096-packet ceiling are for a long, lossy route, where more sessions lose
// less to a lost packet; on a clean path it is slower than Turbo, since the
// control keeps its window at the same few hundred packets per session and
// the four sessions only add overhead and a shorter tick.
func PresetParams(name string) (Params, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case PresetBalance:
		return Params{Conns: 1, Mode: ModeFast, MTU: 1350, SndWnd: 256, RcvWnd: 512,
			SmuxBuf: 2 << 20, StreamBuf: 1 << 20, PcapBuf: 4 << 20}, true
	case PresetTurbo, "":
		return Params{Conns: 2, Mode: ModeFast2, MTU: 1350, SndWnd: 1024, RcvWnd: 1024,
			SmuxBuf: 4 << 20, StreamBuf: 2 << 20, PcapBuf: 8 << 20}, true
	case PresetAggressive:
		return Params{Conns: 4, Mode: ModeFast3, MTU: 1350, SndWnd: 4096, RcvWnd: 4096,
			SmuxBuf: 16 << 20, StreamBuf: 4 << 20, PcapBuf: 32 << 20}, true
	}
	return Params{}, false
}

// Resolve returns p with every zero field taken from the named preset (Turbo
// when the name is empty or unknown) and the constants paqet fixes.
func (p Params) Resolve(preset string) Params {
	base, ok := PresetParams(preset)
	if !ok {
		base, _ = PresetParams(PresetTurbo)
	}
	if p.Conns == 0 {
		p.Conns = base.Conns
	}
	if p.Mode == "" {
		p.Mode = base.Mode
	}
	if p.MTU == 0 {
		p.MTU = base.MTU
	}
	if p.SndWnd == 0 {
		p.SndWnd = base.SndWnd
	}
	if p.RcvWnd == 0 {
		p.RcvWnd = base.RcvWnd
	}
	if p.SmuxBuf == 0 {
		p.SmuxBuf = base.SmuxBuf
	}
	if p.StreamBuf == 0 {
		p.StreamBuf = base.StreamBuf
	}
	if p.PcapBuf == 0 {
		p.PcapBuf = base.PcapBuf
	}
	if p.Block == "" {
		p.Block = DefaultBlock
	}
	if p.KeepAlive == 0 {
		p.KeepAlive = 2 * time.Second
	}
	if p.KeepAliveTimeout == 0 {
		p.KeepAliveTimeout = 8 * time.Second
	}
	return p
}

// DefaultBlock is the cipher used when none is named: AES-256 in kcp-go's
// block mode, which is what paqet defaults to and is hardware accelerated on
// everything Backpack ships for except s390x and the old ARM boards, where
// "aes-128-gcm" or "salsa20" are the usual choices.
const DefaultBlock = "aes"

// Summary is the parameters in one line the screens can show next to a preset's
// name: "2 sessions, fast2, 1024-packet windows, 4 MiB buffers, 8 MiB capture".
func (p Params) Summary() string {
	plural := "sessions"
	if p.Conns == 1 {
		plural = "session"
	}
	mib := func(n int) string { return fmt.Sprintf("%d MiB", n>>20) }
	return fmt.Sprintf("%d %s, %s, %d-packet windows, %s buffers, %s capture",
		p.Conns, plural, p.Mode, p.SndWnd, mib(p.SmuxBuf), mib(p.PcapBuf))
}

// Bounds an operator-facing validation message quotes.
const (
	MaxConns  = 16
	MinMTU    = 576
	MaxMTU    = 1450
	MaxWindow = 32768
)

// blockCrypt describes a cipher kcp-go can build from a derived key.
type blockCrypt struct {
	keySize int // required key size; 0 means the whole key
	build   func(key []byte) (kcp.BlockCrypt, error)
}

var blockCrypts = map[string]blockCrypt{
	"aes":         {0, func(k []byte) (kcp.BlockCrypt, error) { return kcp.NewAESBlockCrypt(k) }},
	"aes-128":     {16, func(k []byte) (kcp.BlockCrypt, error) { return kcp.NewAESBlockCrypt(k) }},
	"aes-128-gcm": {16, func(k []byte) (kcp.BlockCrypt, error) { return kcp.NewAESGCMCrypt(k) }},
	"aes-192":     {24, func(k []byte) (kcp.BlockCrypt, error) { return kcp.NewAESBlockCrypt(k) }},
	"salsa20":     {0, func(k []byte) (kcp.BlockCrypt, error) { return kcp.NewSalsa20BlockCrypt(k) }},
	"blowfish":    {0, func(k []byte) (kcp.BlockCrypt, error) { return kcp.NewBlowfishBlockCrypt(k) }},
	"twofish":     {0, func(k []byte) (kcp.BlockCrypt, error) { return kcp.NewTwofishBlockCrypt(k) }},
	"cast5":       {16, func(k []byte) (kcp.BlockCrypt, error) { return kcp.NewCast5BlockCrypt(k) }},
	"3des":        {24, func(k []byte) (kcp.BlockCrypt, error) { return kcp.NewTripleDESBlockCrypt(k) }},
	"tea":         {16, func(k []byte) (kcp.BlockCrypt, error) { return kcp.NewTEABlockCrypt(k) }},
	"xtea":        {16, func(k []byte) (kcp.BlockCrypt, error) { return kcp.NewXTEABlockCrypt(k) }},
	"xor":         {0, func(k []byte) (kcp.BlockCrypt, error) { return kcp.NewSimpleXORBlockCrypt(k) }},
	"sm4":         {16, func(k []byte) (kcp.BlockCrypt, error) { return kcp.NewSM4BlockCrypt(k) }},
	"none":        {0, func(k []byte) (kcp.BlockCrypt, error) { return kcp.NewNoneBlockCrypt(k) }},
	"null":        {0, func(k []byte) (kcp.BlockCrypt, error) { return nil, nil }},
}

// Blocks lists the cipher names a file may use, sorted.
func Blocks() []string {
	names := make([]string, 0, len(blockCrypts))
	for n := range blockCrypts {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// keySalt separates this product's key derivation from paqet's ("paqet") and
// from anything else that stretches the same token.
const keySalt = "backpack-pck-paqet-v2"

// keyRounds is paqet's figure. The derivation runs once per generation.
const keyRounds = 100_000

// DeriveKey stretches the tunnel token into 32 bytes of key material.
func DeriveKey(token string) []byte {
	return pbkdf2.Key([]byte(token), []byte(keySalt), keyRounds, 32, sha256.New)
}

// newBlock builds the named cipher from derived key material. A nil block with
// a nil error is "null": KCP with no encryption at all.
func newBlock(name string, key []byte) (kcp.BlockCrypt, error) {
	b, ok := blockCrypts[name]
	if !ok {
		return nil, fmt.Errorf("unsupported block cipher %q", name)
	}
	if b.keySize > 0 && len(key) >= b.keySize {
		key = key[:b.keySize]
	}
	return b.build(key)
}

// Validate checks resolved parameters and explains any fault in the words an
// operator can act on. Every message names the key in the file.
func (p Params) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if p.Conns < 1 || p.Conns > MaxConns {
		add("pck_conns %d: use 1 to %d KCP sessions", p.Conns, MaxConns)
	}
	if !slices.Contains(Modes(), p.Mode) {
		add("pck_kcp_mode %q: use one of %s", p.Mode, strings.Join(Modes(), ", "))
	}
	if p.Mode == ModeManual && p.Interval < 1 {
		add("pck_kcp_mode \"manual\" needs an interval")
	}
	if p.MTU < MinMTU || p.MTU > MaxMTU {
		add("pck_kcp_mtu %d: use %d to %d (a larger packet does not fit a 1500-byte path with the TCP and IP headers, and these segments are not fragmented)", p.MTU, MinMTU, MaxMTU)
	}
	if p.SndWnd < 1 || p.SndWnd > MaxWindow {
		add("pck_kcp_sndwnd %d: use 1 to %d", p.SndWnd, MaxWindow)
	}
	if p.RcvWnd < 1 || p.RcvWnd > MaxWindow {
		add("pck_kcp_rcvwnd %d: use 1 to %d", p.RcvWnd, MaxWindow)
	}
	if _, ok := blockCrypts[p.Block]; !ok {
		add("pck_kcp_block %q: use one of %s", p.Block, strings.Join(Blocks(), ", "))
	}
	if p.SmuxBuf < 1024 || p.StreamBuf < 1024 {
		add("the smux buffers must be at least 1024 bytes")
	} else if p.StreamBuf > p.SmuxBuf {
		// smux refuses this itself, and would refuse it on every session.
		add("the smux stream buffer (%d) must not be larger than the session buffer (%d)", p.StreamBuf, p.SmuxBuf)
	}
	if p.KeepAlive <= 0 || p.KeepAliveTimeout < p.KeepAlive {
		add("the smux keepalive timeout (%s) must be at least its interval (%s)", p.KeepAliveTimeout, p.KeepAlive)
	}
	if p.PcapBuf < 1024 || p.PcapBuf > 256<<20 {
		add("sockbuf %d: the capture buffer must be 1024 bytes to 256 MiB", p.PcapBuf)
	}
	if p.DShard < 0 || p.PShard < 0 || (p.DShard == 0) != (p.PShard == 0) {
		add("KCP FEC needs both shard counts or neither")
	}
	return errors.Join(errs...)
}

// Config is one engine: a role, where it points, the secret, and the tuning.
type Config struct {
	// Server is true on the side that waits (paqet's server, the kharej).
	Server bool

	// Addr is the peer's host:port for a client, or the address to bind for a
	// server. Only the port is used to bind: the capture is by port, and the
	// source address of what is sent is the one the route to the peer leaves
	// from.
	Addr string

	// Token is the shared secret the cipher key is derived from.
	Token string

	// Interface and GatewayMAC override the path lookup. Empty finds them.
	Interface  string
	GatewayMAC string

	// Flags is the cycle of TCP flag combinations this side stamps on what it
	// sends. Empty is PSH+ACK.
	Flags []network.TCPFlags

	// RemoteTargets lets a server dial targets that are not its own addresses.
	// Off, a client reaches only this host's services. See targets.go.
	RemoteTargets bool

	Params Params
}

// Validate refuses a configuration that cannot run.
func (c Config) Validate() error {
	var errs []error
	host, port, err := splitHostPort(c.Addr)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("addr %q must be host:port", c.Addr))
	case port < 1:
		errs = append(errs, fmt.Errorf("addr %q needs a port: the tunnel's segments are addressed to it", c.Addr))
	case !c.Server && host == "":
		errs = append(errs, errors.New("addr must name the other server"))
	}
	if strings.TrimSpace(c.Token) == "" {
		errs = append(errs, errors.New("token is required and must match the other end"))
	}
	if err := c.Params.Validate(); err != nil {
		errs = append(errs, err)
	}
	if len(c.Flags) > 64 {
		errs = append(errs, errors.New("pck_flags: at most 64 combinations"))
	}
	return errors.Join(errs...)
}

func splitHostPort(addr string) (string, int, error) {
	host, ps, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return "", 0, err
	}
	var port int
	if ps != "" {
		if _, err := fmt.Sscanf(ps, "%d", &port); err != nil || port < 0 || port > 65535 {
			return "", 0, fmt.Errorf("bad port %q", ps)
		}
	}
	return host, port, nil
}

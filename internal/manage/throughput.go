package manage

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/tui"
)

// Measuring what a tunnel actually carries.
//
// LinkTest next door measures latency, jitter and loss — how the path behaves.
// It says nothing about throughput, and throughput is the question that keeps
// coming up: a tunnel that pings at 97 ms and moves 8 Mbit/s and a tunnel that
// pings at 97 ms and moves 200 Mbit/s look identical from every other screen.
//
// Finding out used to mean `dd if=/dev/zero | nc` on one server and `nc -l` on
// the other, by hand, twice, and reading the answer off the clock. Every number
// this project has learned about its own performance was measured that way.
// This is the same measurement with the coordination taken out.
//
// # What it measures, and what it does not
//
// It pushes bytes through the tunnel interface to a listener on the other end,
// so what comes back is the tunnel's own capacity: encapsulation, encryption,
// carrier and path, all of it. It is not a speedtest of the internet beyond
// kharej — that is a different question and needs a different target.
//
// Random bytes, not zeroes. A carrier that compresses, or a middlebox that
// does, would make a stream of zeroes look faster than anything real.

const (
	// throughputPort is where the receiving end listens. High and unremarkable,
	// and only ever reachable across the tunnel's own private subnet.
	throughputPort = 47113

	// throughputWarmup is skipped before the clock starts, so TCP slow start is
	// not counted as the link being slow. On a 100 ms path a window takes a
	// couple of round trips to open.
	throughputWarmup = 2 * time.Second

	// throughputRun is how long the measurement itself lasts. Long enough for
	// the window to settle, short enough that nobody walks away.
	throughputRun = 8 * time.Second

	// throughputBuf is the write size. Large enough that the syscall rate is
	// not what is being measured.
	throughputBuf = 256 << 10
)

// ThroughputResult is one measurement.
type ThroughputResult struct {
	Bytes    uint64
	Duration time.Duration
}

// Mbps is the measured rate in megabits per second.
func (r ThroughputResult) Mbps() float64 {
	if r.Duration <= 0 {
		return 0
	}
	return float64(r.Bytes) * 8 / r.Duration.Seconds() / 1_000_000
}

func (r ThroughputResult) String() string {
	return fmt.Sprintf("%.1f Mbit/s (%s in %s)",
		r.Mbps(), humanBytes(r.Bytes), r.Duration.Round(100*time.Millisecond))
}

// ServeThroughput listens on the tunnel address and sinks whatever arrives,
// until ctx ends. This is the receiving half, run on the other machine.
func ServeThroughput(ctx context.Context, bind string) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, fmt.Sprint(throughputPort)))
	if err != nil {
		return err
	}
	defer ln.Close()
	go func() { <-ctx.Done(); ln.Close() }()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		// Sunk, not echoed. Echoing would measure the round trip and halve the
		// answer, and the return direction is a separate measurement anyway.
		go func() {
			defer conn.Close()
			_, _ = io.Copy(io.Discard, conn)
		}()
	}
}

// MeasureThroughput pushes bytes to the far end and reports the rate.
func MeasureThroughput(ctx context.Context, peer string) (ThroughputResult, error) {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp",
		net.JoinHostPort(peer, fmt.Sprint(throughputPort)))
	if err != nil {
		return ThroughputResult{}, fmt.Errorf(
			"could not reach the other end on %s:%d — is the receiver running there? %w",
			peer, throughputPort, err)
	}
	defer conn.Close()

	buf := make([]byte, throughputBuf)
	if _, err := rand.Read(buf); err != nil {
		return ThroughputResult{}, err
	}

	// Warm up without counting. TCP opens its window over the first few round
	// trips, and on a long path that is most of a second of deliberately slow
	// sending that has nothing to do with the link's capacity.
	deadline := time.Now().Add(throughputWarmup)
	for time.Now().Before(deadline) {
		if _, err := conn.Write(buf); err != nil {
			return ThroughputResult{}, err
		}
	}

	var sent uint64
	start := time.Now()
	deadline = start.Add(throughputRun)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		n, err := conn.Write(buf)
		sent += uint64(n)
		if err != nil {
			// A short run that moved real bytes is still an answer; only a run
			// that moved nothing is a failure.
			if sent == 0 {
				return ThroughputResult{}, err
			}
			break
		}
	}
	return ThroughputResult{Bytes: sent, Duration: time.Since(start)}, nil
}

// humanBytes renders a byte count the way an operator reads one.
func humanBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// The two menu entries.
//
// A measurement needs both ends, and the coordination is the part that was
// tedious by hand: start a sink over there, start a sender over here, read the
// clock. These are the same two halves, named so it is obvious which machine
// runs which.

// SpeedTest is the menu entry. A measurement needs a sink on one machine and a
// sender on the other, and which one this machine should be is the first thing
// to settle — getting it backwards is the whole of what made the manual version
// fiddly.
func SpeedTest() {
	tui.Clear()
	tui.Title("Speed Test")
	tui.Warn("This needs both servers. Start the receiver on one, then the")
	tui.Warn("sender on the other — the sender is the one that reports.")
	fmt.Println()

	switch tui.ChooseOpt("What should this machine do?", []tui.Option{
		{Title: "Receive", Desc: "start the sink here first, then go to the other server"},
		{Title: "Send and measure", Desc: "the receiver is already running on the other server"},
	}) {
	case 0:
		ThroughputReceiver()
	case 1:
		ThroughputSender()
	}
}

// ThroughputReceiver runs the sink. It is started on the machine that is not
// being measured from, and left running while the other end measures.
func ThroughputReceiver() {
	tui.Clear()
	tui.Title("Speed Test — receiver")

	tunnels := directTunnels()
	if len(tunnels) == 0 {
		tui.Info("No full IP tunnel found on this server.")
		tui.Warn("This measures a tunnel's own capacity, so it needs one to measure.")
		tui.PressEnter()
		return
	}
	t := pickTunnel(tunnels, "Which tunnel should receive?")
	if t == nil {
		return
	}
	local := tunnelLocalIP(t.Name)
	if local == "" {
		tui.Error("This tunnel has no address on its own interface to listen on.")
		tui.PressEnter()
		return
	}

	fmt.Println()
	tui.Success("Listening on " + local + fmt.Sprintf(":%d", throughputPort))
	tui.Info("Now run Speed Test on the OTHER server and choose the same tunnel.")
	fmt.Println()
	tui.Warn("Press Ctrl+C when the other end reports its result.")
	fmt.Println()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := ServeThroughput(ctx, local); err != nil {
		tui.Error("The receiver stopped: " + err.Error())
	}
	tui.PressEnter()
}

// ThroughputSender measures and reports.
func ThroughputSender() {
	tui.Clear()
	tui.Title("Speed Test")
	tui.Warn("Measures what this tunnel actually carries — encapsulation,")
	tui.Warn("encryption, carrier and path together. Start the receiver on the")
	tui.Warn("other server first.")
	fmt.Println()

	tunnels := directTunnels()
	if len(tunnels) == 0 {
		tui.Info("No full IP tunnel found on this server.")
		tui.PressEnter()
		return
	}
	t := pickTunnel(tunnels, "Which tunnel should be measured?")
	if t == nil {
		return
	}
	peer := tunnelPeerIP(t.Name)
	if peer == "" {
		tui.Error("This tunnel has no peer address to measure against.")
		tui.PressEnter()
		return
	}

	fmt.Println()
	tui.Info(fmt.Sprintf("Measuring to %s — about %s, warm-up excluded...",
		peer, (throughputWarmup + throughputRun).Round(time.Second)))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := MeasureThroughput(ctx, peer)
	if err != nil {
		fmt.Println()
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}

	fmt.Println()
	tui.Rule()
	tui.Success("Throughput: " + res.String())
	tui.Rule()
	fmt.Println()
	// The number on its own is not advice, and the two things it usually means
	// are worth naming rather than leaving to be rediscovered.
	tui.Info("Well under the link's capacity usually means one of two things:")
	tui.Info("  · the MTU is wrong — check the log for what the path measured")
	tui.Info("  · the preset is too small for a long path — try Aggressive")
	fmt.Println()
	tui.PressEnter()
}

// directTunnels are the full IP tunnels on this machine, which are the ones
// that have an interface to measure across.
func directTunnels() []Tunnel {
	var out []Tunnel
	for _, t := range List() {
		if strings.HasPrefix(t.Transport, "l3/") {
			out = append(out, t)
		}
	}
	return out
}

// pickTunnel offers a choice, or takes the only one there is.
func pickTunnel(tunnels []Tunnel, question string) *Tunnel {
	if len(tunnels) == 1 {
		return &tunnels[0]
	}
	opts := make([]tui.Option, len(tunnels))
	for i, t := range tunnels {
		opts[i] = tui.Option{Title: t.Name, Desc: TunnelCarrier(t) + " — " + t.Addr}
	}
	idx := tui.ChooseOpt(question, opts)
	if idx < 0 {
		return nil
	}
	return &tunnels[idx]
}

// tunnelLocalIP and tunnelPeerIP read the two ends of a tunnel's private
// subnet, which is what the measurement runs across.
func tunnelLocalIP(name string) string {
	cfg, err := LoadTunnelConfig(name)
	if err != nil || !cfg.L3.Enabled() {
		return ""
	}
	addr, _, _ := strings.Cut(strings.TrimSpace(cfg.L3.LocalIP), "/")
	return addr
}

func tunnelPeerIP(name string) string {
	cfg, err := LoadTunnelConfig(name)
	if err != nil || !cfg.L3.Enabled() {
		return ""
	}
	return strings.TrimSpace(cfg.L3.PeerIP)
}

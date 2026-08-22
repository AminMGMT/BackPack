package manage

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
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

	// throughputSinkWindow is how long the receiver waits for a sender before
	// giving up. It has to cover somebody walking to the other server and
	// finding the same menu entry there, which is the whole reason the two
	// halves are separate screens.
	throughputSinkWindow = 5 * time.Minute
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
	return ServeThroughputOn(ctx, bind, throughputPort)
}

// ServeThroughputOn is the same sink on a port of the caller's choosing.
//
// A layer-3 tunnel always uses throughputPort, because it has a private subnet
// where nothing else is listening. A port forwarder has to borrow the backend
// port of one of its own mappings, since that is the only address the far end
// can reach through the tunnel — see throughputforward.go.
func ServeThroughputOn(ctx context.Context, bind string, port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, fmt.Sprint(port)))
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
	return MeasureThroughputOn(ctx, peer, throughputPort)
}

// MeasureThroughputOn is the same measurement against a port of the caller's
// choosing. See ServeThroughputOn for why a port forwarder needs one.
func MeasureThroughputOn(ctx context.Context, peer string, port int) (ThroughputResult, error) {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp",
		net.JoinHostPort(peer, fmt.Sprint(port)))
	if err != nil {
		return ThroughputResult{}, fmt.Errorf(
			"could not reach the other end on %s:%d — is the receiver running there? %w",
			peer, port, err)
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

	tunnels := measurableTunnels()
	if len(tunnels) == 0 {
		explainNoMeasurableTunnel()
		return
	}
	t := pickTunnel(tunnels, "Which tunnel should receive?")
	if t == nil {
		return
	}

	// Where the sink goes depends on what kind of tunnel this is: a layer-3
	// tunnel has an address of its own to listen on, a port forwarder does not
	// and has to borrow a mapping's backend port instead.
	var local string
	var port int
	if isForwardKind(*t) {
		if HoldsPorts(*t) {
			wrongSideForForward(*t, true)
			return
		}
		p, ok := askBackendPort(*t)
		if !ok {
			return
		}
		local, port = forwardBackendHost, p
	} else {
		local, port = tunnelLocalIP(t.Name), throughputPort
		if local == "" {
			tui.Error("This tunnel has no address on its own interface to listen on.")
			tui.PressEnter()
			return
		}
	}

	fmt.Println()
	tui.Success("Listening on " + local + fmt.Sprintf(":%d", port))
	tui.Info("Now run Speed Test on the OTHER server and choose the same tunnel.")
	fmt.Println()
	tui.Warn("Press Ctrl+C when the other end reports its result.")
	fmt.Println()

	// The interrupt has to be caught here, because this screen is the one that
	// asks for it.
	//
	// Without this the instruction above was a trap: nothing in the menu
	// installs a handler, so Ctrl+C took Go's default and killed the whole CLI.
	// Somebody following the screen exactly lost the menu, the tunnel list and
	// whatever they were in the middle of — which reads as the speed test being
	// broken, because the one action it tells you to take is the one that ends
	// the program. Every other screen that asks for Ctrl+C already handles it;
	// see StatusLive.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)

	ctx, cancel := context.WithTimeout(context.Background(), throughputSinkWindow)
	defer cancel()

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-sig:
			cancel()
		case <-ctx.Done():
		}
	}()

	err := ServeThroughputOn(ctx, local, port)
	cancel()
	<-stopped

	fmt.Println()
	switch {
	case err != nil:
		tui.Error("The receiver stopped: " + err.Error())
	case ctx.Err() == context.DeadlineExceeded:
		// Silence here used to be indistinguishable from success. It is not:
		// nobody ever connected.
		tui.Warn(fmt.Sprintf("No sender arrived within %s — the receiver has stopped.",
			throughputSinkWindow))
		tui.Info("Start the receiver again, then run the sender promptly on the other server.")
	default:
		tui.Success("Receiver stopped.")
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

	tunnels := measurableTunnels()
	if len(tunnels) == 0 {
		explainNoMeasurableTunnel()
		return
	}
	t := pickTunnel(tunnels, "Which tunnel should be measured?")
	if t == nil {
		return
	}

	// A layer-3 tunnel is measured across its private subnet; a port forwarder
	// through one of the mappings it already carries, from this side's own
	// loopback. Either way what follows sends to one address and one port.
	var peer string
	var port int
	if isForwardKind(*t) {
		if !HoldsPorts(*t) {
			wrongSideForForward(*t, false)
			return
		}
		m, ok := pickForwardMapping(*t)
		if !ok {
			return
		}
		fmt.Println()
		tui.Warn(fmt.Sprintf("The receiver on the other server must be sinking on port %d,", m.TargetPort))
		tui.Warn("which means the real backend there is stopped for the moment.")
		fmt.Println()
		if !tui.Confirm("Is the receiver running there now", false) {
			tui.Info("Start it there first, then come back.")
			tui.PressEnter()
			return
		}
		peer, port = forwardBackendHost, m.ListenPort
	} else {
		peer, port = tunnelPeerIP(t.Name), throughputPort
		if peer == "" {
			tui.Error("This tunnel has no peer address to measure against.")
			tui.PressEnter()
			return
		}
	}

	fmt.Println()
	tui.Info(fmt.Sprintf("Measuring to %s:%d — about %s, warm-up excluded...",
		peer, port, (throughputWarmup + throughputRun).Round(time.Second)))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := MeasureThroughputOn(ctx, peer, port)
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

// explainNoMeasurableTunnel is what this screen says when there is nothing it
// can measure.
//
// It used to say "No full IP tunnel found on this server." and stop, which was
// true and useless: the menu offers this entry to everybody, and an operator
// with three working tunnels reads a flat denial as the feature being broken.
// Now that a port forwarder can be measured too, reaching this at all means
// something specific — the tunnels here expose ports but none are configured —
// so it says that instead.
func explainNoMeasurableTunnel() {
	others := List()

	fmt.Println()
	if len(others) == 0 {
		tui.Info("There are no tunnels on this server yet.")
		tui.PressEnter()
		return
	}

	tui.Info("None of the tunnels on this server can be measured yet.")
	fmt.Println()
	for _, t := range others {
		fmt.Printf("  · %s%s%s — %s\n", tui.Bold, t.Name, tui.Reset, TunnelCarrier(t))
	}
	fmt.Println()
	tui.Warn("A port forwarder is measured through one of the ports it carries, and")
	tui.Warn("these have none configured. Add a port mapping in Manage Tunnels and")
	tui.Warn("this screen will be able to use it.")
	fmt.Println()
	tui.Info("Link Test works on any tunnel meanwhile — it measures latency, jitter")
	tui.Info("and loss, and is what the transport recommendation is built on.")
	tui.PressEnter()
}

// measurableTunnels is every tunnel this screen can put a number on.
//
// It used to be the layer-3 ones alone, which made the entry a dead end for
// anybody running a port forwarder — which is most people. Both kinds are
// measurable, by different routes: a layer-3 tunnel across the private subnet
// it already has, a port forwarder through one of the mappings it already
// carries. See throughputforward.go for the second.
func measurableTunnels() []Tunnel {
	var out []Tunnel
	for _, t := range List() {
		// A forwarder with nothing to forward has no route for a measurement,
		// and the side that holds the backends keeps no port list of its own —
		// that side is judged when it is chosen, not here, or it would vanish
		// from the list on the very machine the receiver runs on.
		if isForwardKind(t) && HoldsPorts(t) && len(t.Ports) == 0 {
			continue
		}
		out = append(out, t)
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

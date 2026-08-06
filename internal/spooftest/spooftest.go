package spooftest

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/backpack/backpack/internal/utils/network"
)

// The tester answers one question: starting from host A, can a packet whose
// source IP is forged to X actually cross the network and reach host B? Run it
// over a list, range or CIDR of candidate IPs, and it reports which ones arrive
// and at what loss. Run it both ways (A→B, then B→A) to map each direction; the
// forged sources that pass in one direction are the spoof addresses to configure
// on the corresponding end.
//
// It rides the same two primitives as the tunnel: a raw spoofed-UDP sender, and
// an ordinary UDP receive socket the probes land on (because they are addressed
// to the receiver's real IP, the kernel delivers them normally). Nothing here
// floods — it sends a small, bounded number of probes per candidate.

const probeLen = 8 // [magic:4][seq:4]

// Magic derives the per-session probe marker from the shared token, so the
// receiver accepts only this run's probes and ignores unrelated UDP traffic.
func Magic(token string) uint32 {
	sum := sha256.Sum256([]byte("backpack-spooftest-v1:" + token))
	return binary.BigEndian.Uint32(sum[:4])
}

func encodeProbe(magic, seq uint32) []byte {
	b := make([]byte, probeLen)
	binary.BigEndian.PutUint32(b[0:4], magic)
	binary.BigEndian.PutUint32(b[4:8], seq)
	return b
}

func decodeProbe(magic uint32, b []byte) (seq uint32, ok bool) {
	if len(b) < probeLen || binary.BigEndian.Uint32(b[0:4]) != magic {
		return 0, false
	}
	return binary.BigEndian.Uint32(b[4:8]), true
}

// SenderConfig drives one direction's probe run.
type SenderConfig struct {
	Iface    string // NIC to send from
	Token    string // shared secret → probe magic (must match the receiver)
	TargetIP net.IP // the receiver node's REAL IPv4
	DstPort  uint16 // the receiver's listen port
	Attempts int    // probes per candidate
	Delay    time.Duration
	IPs      []net.IP // candidate forged sources (already expanded)
	// Progress, if set, is called after each candidate with (done, total).
	Progress func(done, total int)
}

// RunSender emits Attempts probes from each candidate forged source toward the
// target. Results are observed on the receiver side, not here.
func RunSender(cfg SenderConfig) error {
	if cfg.TargetIP.To4() == nil {
		return fmt.Errorf("target must be a real IPv4 address")
	}
	if cfg.Attempts <= 0 {
		cfg.Attempts = 5
	}
	sender, err := network.NewSpoofRawSender(cfg.Iface)
	if err != nil {
		return err
	}
	defer sender.Close()

	magic := Magic(cfg.Token)
	const srcPort = 40000 // cosmetic; the forged source IP is what matters
	for i, ip := range cfg.IPs {
		for seq := 0; seq < cfg.Attempts; seq++ {
			if err := sender.SendUDP(ip, cfg.TargetIP, srcPort, cfg.DstPort, encodeProbe(magic, uint32(seq))); err != nil {
				return fmt.Errorf("sending from %v: %w", ip, err)
			}
			if cfg.Delay > 0 {
				time.Sleep(cfg.Delay)
			}
		}
		if cfg.Progress != nil {
			cfg.Progress(i+1, len(cfg.IPs))
		}
	}
	return nil
}

// Result is one forged source's arrival tally.
type Result struct {
	IP       net.IP
	Arrived  int // distinct probes that landed
	Attempts int // probes the sender was expected to emit per IP
}

// LossPercent is the fraction of probes that did not arrive.
func (r Result) LossPercent() float64 {
	if r.Attempts == 0 {
		return 100
	}
	lost := r.Attempts - r.Arrived
	if lost < 0 {
		lost = 0
	}
	return float64(lost) * 100 / float64(r.Attempts)
}

// ReceiverConfig drives the capture side.
type ReceiverConfig struct {
	Token    string        // shared secret → probe magic (must match the sender)
	Port     uint16        // UDP port to listen on
	Attempts int           // probes the sender emits per IP (the loss denominator)
	Window   time.Duration // how long to capture
}

// RunReceiver captures probes for the window and returns a per-source tally,
// sorted by loss then address. It counts distinct sequence numbers per source,
// so a duplicated probe is not mistaken for two arrivals.
func RunReceiver(cfg ReceiverConfig) ([]Result, error) {
	if cfg.Attempts <= 0 {
		cfg.Attempts = 5
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: int(cfg.Port)})
	if err != nil {
		return nil, fmt.Errorf("listening on udp/%d: %w", cfg.Port, err)
	}
	defer conn.Close()

	magic := Magic(cfg.Token)
	seen := make(map[string]map[uint32]struct{}) // src IP -> set of seqs
	deadline := time.Now().Add(cfg.Window)
	_ = conn.SetReadDeadline(deadline)

	buf := make([]byte, 2048)
	for time.Now().Before(deadline) {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			break // deadline or closed
		}
		seq, ok := decodeProbe(magic, buf[:n])
		if !ok {
			continue
		}
		key := addr.IP.String()
		if seen[key] == nil {
			seen[key] = make(map[uint32]struct{})
		}
		seen[key][seq] = struct{}{}
	}

	results := make([]Result, 0, len(seen))
	for ipStr, seqs := range seen {
		results = append(results, Result{
			IP:       net.ParseIP(ipStr),
			Arrived:  len(seqs),
			Attempts: cfg.Attempts,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].LossPercent() != results[j].LossPercent() {
			return results[i].LossPercent() < results[j].LossPercent()
		}
		return bytesLess(results[i].IP, results[j].IP)
	})
	return results, nil
}

// Passing returns the addresses whose loss is at or below maxLoss.
func Passing(results []Result, maxLoss float64) []net.IP {
	var out []net.IP
	for _, r := range results {
		if r.LossPercent() <= maxLoss {
			out = append(out, r.IP)
		}
	}
	return out
}

func bytesLess(a, b net.IP) bool {
	a4, b4 := a.To4(), b.To4()
	if a4 == nil || b4 == nil {
		return a.String() < b.String()
	}
	return binary.BigEndian.Uint32(a4) < binary.BigEndian.Uint32(b4)
}

// Package metrics records what a running tunnel is actually doing — how much
// it carried, how much had to be sent twice, and how much was repaired by
// error correction — and leaves a snapshot on disk for the CLI to read.
//
// A tunnel runs as its own process, so there is no shared memory to inspect.
// Each engine writes a small JSON file that the menu reads, which keeps the
// two sides decoupled and costs nothing when nobody is looking.
package metrics

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/xtaci/kcp-go/v5"
)

// Snapshot is one reading of a tunnel's counters.
type Snapshot struct {
	Name      string    `json:"name"`
	Transport string    `json:"transport"`
	Role      string    `json:"role"`
	Taken     time.Time `json:"taken"`
	Uptime    string    `json:"uptime"`

	// Traffic over the tunnel itself, as the transport sees it.
	BytesIn  uint64 `json:"bytes_in"`
	BytesOut uint64 `json:"bytes_out"`

	// Peer is the address of the connected far end, when the transport knows
	// it and the operating system does not.
	//
	// For the TCP-based transports the socket table already shows who is
	// connected, so this stays empty. A datagram listener is different: it is
	// one unconnected socket that keeps no record of its peers, so the kernel
	// has nothing to report and the panel could show neither ping nor location
	// for a working KCP tunnel. The transport does know, so it says so here.
	Peer string `json:"peer,omitempty"`

	// KCP-only. Zero on every other transport.
	KCP *KCPStats `json:"kcp,omitempty"`
}

// The connected peer, published by whichever transport is running. One tunnel
// runs per process, so a single value needs no key.
var peerAddr atomic.Pointer[string]

// ReportPeer records the address of the connected far end. Called by transports
// whose peer the socket table cannot show.
func ReportPeer(addr string) {
	peerAddr.Store(&addr)
}

// ClearPeer forgets the connected peer, on disconnect or restart, so a stale
// address is not reported as current.
func ClearPeer() {
	peerAddr.Store(nil)
}

// currentPeer returns the reported peer, or "" if there is none.
func currentPeer() string {
	if p := peerAddr.Load(); p != nil {
		return *p
	}
	return ""
}

// KCPStats is what the KCP layer knows about the quality of the link. These
// numbers are the honest answer to "is this transport earning its keep?".
type KCPStats struct {
	PacketsIn  uint64 `json:"packets_in"`
	PacketsOut uint64 `json:"packets_out"`

	// Retransmitted is how many segments had to be sent again — the cost of a
	// lossy link that error correction could not cover.
	Retransmitted uint64 `json:"retransmitted"`
	// Lost is how many segments KCP concluded never arrived.
	Lost uint64 `json:"lost"`
	// Duplicated is how many arrived more than once.
	Duplicated uint64 `json:"duplicated"`

	// FECRecovered is the number of packets rebuilt from parity instead of
	// being waited for. This is forward error correction doing its job, and
	// the clearest signal that KCP is the right transport for this route.
	FECRecovered uint64 `json:"fec_recovered"`
	// FECErrors counts parity groups that could not be rebuilt.
	FECErrors uint64 `json:"fec_errors"`
}

// LossPercent estimates how much of the traffic needed repair or resending.
// It is derived from KCP's own accounting, not from a probe, so it reflects
// the traffic the tunnel actually carried.
func (k *KCPStats) LossPercent() float64 {
	if k == nil || k.PacketsOut == 0 {
		return 0
	}
	return float64(k.Retransmitted+k.Lost) / float64(k.PacketsOut) * 100
}

// Path returns where a tunnel's snapshot lives.
func Path(dir, name string) string {
	return filepath.Join(dir, name+".metrics.json")
}

// Collector periodically writes a tunnel's snapshot to disk.
type Collector struct {
	// baseIn/baseOut are the totals this tunnel had already accumulated before
	// this process started, read from the last written snapshot.
	baseIn, baseOut uint64

	dir       string
	name      string
	transport string
	role      string
	started   time.Time

	// bytesIn/bytesOut are supplied by the caller, since only the transport
	// knows what it moved.
	bytesIn  func() uint64
	bytesOut func() uint64
}

// NewCollector builds a collector for one tunnel. The byte accessors may be
// nil when a transport does not track them.
func NewCollector(dir, name, transport, role string, bytesIn, bytesOut func() uint64) *Collector {
	c := &Collector{
		dir:       dir,
		name:      name,
		transport: transport,
		role:      role,
		started:   time.Now(),
		bytesIn:   bytesIn,
		bytesOut:  bytesOut,
	}
	// Carry on from whatever this tunnel had already moved.
	//
	// The live counters only know about this process, so without a baseline the
	// totals would reset every restart — and a tunnel restarts for all sorts of
	// ordinary reasons: an update, a config edit, the watchdog. A figure that
	// silently returns to zero is not a total of anything.
	//
	// The baseline comes from the tunnel's own metrics file, which lives in the
	// config directory and is therefore inside every backup. So restoring a
	// backup also restores the traffic history, rather than starting the count
	// again on the new machine.
	if prev, err := Read(dir, name); err == nil {
		c.baseIn, c.baseOut = prev.BytesIn, prev.BytesOut
	}
	return c
}

// Snapshot reads the current counters without writing anything.
func (c *Collector) Snapshot() Snapshot {
	s := Snapshot{
		Name:      c.name,
		Transport: c.transport,
		Role:      c.role,
		Taken:     time.Now(),
		Uptime:    time.Since(c.started).Round(time.Second).String(),
		Peer:      currentPeer(),
	}
	// The persisted baseline plus what this process has carried.
	liveIn, liveOut := Traffic()
	s.BytesIn, s.BytesOut = c.baseIn+liveIn, c.baseOut+liveOut
	if c.bytesIn != nil {
		s.BytesIn = c.bytesIn()
	}
	if c.bytesOut != nil {
		s.BytesOut = c.bytesOut()
	}
	if c.transport == "kcp" {
		// kcp-go keeps these counters process-wide. A tunnel runs as its own
		// process, so they describe exactly this tunnel.
		snmp := kcp.DefaultSnmp.Copy()
		s.KCP = &KCPStats{
			PacketsIn:     snmp.InPkts,
			PacketsOut:    snmp.OutPkts,
			Retransmitted: snmp.RetransSegs,
			Lost:          snmp.LostSegs,
			Duplicated:    snmp.RepeatSegs,
			FECRecovered:  snmp.FECRecovered,
			FECErrors:     snmp.FECErrs,
		}
	}
	return s
}

// Write persists one snapshot. Failures are returned but are not worth
// stopping a tunnel over — metrics are diagnostics, not function.
func (c *Collector) Write() error {
	s := c.Snapshot()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.dir, 0755); err != nil {
		return err
	}
	// Write to a temporary file and rename, so a reader never sees a half
	// written snapshot.
	tmp := Path(c.dir, c.name) + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, Path(c.dir, c.name))
}

// Run writes a snapshot every interval until the channel closes.
func (c *Collector) Run(done <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			// A final write on the way out, so a clean restart loses only the
			// traffic since the last tick rather than up to a whole interval.
			_ = c.Write()
			return
		case <-ticker.C:
			_ = c.Write()
		}
	}
}

// Read loads a tunnel's last snapshot. A missing file simply means the tunnel
// has not written one yet.
func Read(dir, name string) (Snapshot, error) {
	var s Snapshot
	b, err := os.ReadFile(Path(dir, name))
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

// Tunnel traffic counting.
//
// Only KCP used to report traffic, because kcp-go happens to keep process-wide
// byte counters and the snapshot could just read them. Every other transport
// reported "0 B in, 0 B out" — not because nothing was flowing, but because
// nobody was counting.
//
// These counters fix that uniformly: the tunnel connection is wrapped, so a
// read is data arriving from the peer and a write is data leaving for it,
// whichever side of the tunnel this process is. One tunnel runs per process, so
// package-level totals describe exactly this tunnel.
var (
	bytesIn  atomic.Uint64
	bytesOut atomic.Uint64
)

// CountedConn wraps a tunnel connection so its traffic is recorded.
//
// It must be applied to the tunnel side, never the local one: wrapping both
// would count the same bytes twice, since every byte crossing the tunnel is
// also read from or written to a local socket.
func CountedConn(c net.Conn) net.Conn { return &countedConn{Conn: c} }

type countedConn struct{ net.Conn }

func (c *countedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		bytesIn.Add(uint64(n))
	}
	return n, err
}

func (c *countedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		bytesOut.Add(uint64(n))
	}
	return n, err
}

// AddBytes records traffic for transports that do not hand out a net.Conn —
// the websocket ones read and write messages instead.
func AddBytes(in, out uint64) {
	if in > 0 {
		bytesIn.Add(in)
	}
	if out > 0 {
		bytesOut.Add(out)
	}
}

// Traffic returns the bytes carried over the tunnel so far.
func Traffic() (in, out uint64) { return bytesIn.Load(), bytesOut.Load() }

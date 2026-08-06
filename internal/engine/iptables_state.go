package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/backpack/backpack/internal/instanceid"
	"github.com/backpack/backpack/internal/metrics"
)

type kernelCount struct {
	RXBytes   uint64 `json:"rx_bytes"`
	TXBytes   uint64 `json:"tx_bytes"`
	RXPackets uint64 `json:"rx_packets"`
	TXPackets uint64 `json:"tx_packets"`
}

type forwardState struct {
	InstanceID  string                 `json:"instance_id"`
	Generation  uint64                 `json:"generation"`
	Families    []string               `json:"families"`
	DesiredHash string                 `json:"desired_hash"`
	RulesHash   string                 `json:"rules_hash"`
	Last        map[string]kernelCount `json:"last_by_generation"`
	Session     kernelCount            `json:"session"`
	Cumulative  kernelCount            `json:"cumulative"`
	StartedAt   time.Time              `json:"started_at"`
}

func stateDir(path string) string      { return filepath.Join(filepath.Dir(path), "forward-state") }
func statePath(path, id string) string { return filepath.Join(stateDir(path), id+".json") }

func loadForwardState(path string, id instanceid.Identity) forwardState {
	s := forwardState{InstanceID: id.InstanceID, Last: map[string]kernelCount{}}
	b, err := os.ReadFile(statePath(path, id.InstanceID))
	if err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if s.InstanceID != id.InstanceID || s.Last == nil {
		s = forwardState{InstanceID: id.InstanceID, Last: map[string]kernelCount{}}
	}
	return s
}

func saveForwardState(path string, s forwardState) error {
	if err := os.MkdirAll(stateDir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	p := statePath(path, s.InstanceID)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func addDelta(total *kernelCount, previous, current kernelCount) {
	// A recreated rule may reset to zero. Treat the current value as the new
	// delta rather than underflowing or losing it.
	delta := func(old, now uint64) uint64 {
		if now >= old {
			return now - old
		}
		return now
	}
	total.RXBytes += delta(previous.RXBytes, current.RXBytes)
	total.TXBytes += delta(previous.TXBytes, current.TXBytes)
	total.RXPackets += delta(previous.RXPackets, current.RXPackets)
	total.TXPackets += delta(previous.TXPackets, current.TXPackets)
}

func persistMetrics(r Request, s forwardState) error {
	name := instanceid.Name(r.ConfigPath)
	snap := metrics.Snapshot{
		Name: name, Engine: "iptables", Mode: "direct", Taken: time.Now(),
		Transport: "", Role: "", BytesIn: s.Cumulative.RXBytes, BytesOut: s.Cumulative.TXBytes,
		PacketsIn: s.Cumulative.RXPackets, PacketsOut: s.Cumulative.TXPackets,
	}
	if !s.StartedAt.IsZero() {
		snap.Uptime = time.Since(s.StartedAt).Round(time.Second).String()
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	p := metrics.Path(filepath.Dir(r.ConfigPath), name)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write direct metrics: %w", err)
	}
	return os.Rename(tmp, p)
}

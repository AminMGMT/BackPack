package manage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The path-MTU probe, against a real coordinator: over loopback everything up
// to a full frame crosses.
func TestThePathMTUProbeFindsWhatCrosses(t *testing.T) {
	port := ctPickPort(map[int]bool{}, true)
	c, err := startCTCoordinator(port, "tok")
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if got := ctProbePMTU(ctx, "127.0.0.1", port, "tok"); got != ctPMTUMax {
		t.Errorf("over loopback the path MTU came out %d, want %d", got, ctPMTUMax)
	}
	if got := ctProbePMTU(ctx, "127.0.0.1", port, "wrong"); got != 0 {
		t.Errorf("without the token the probe was answered: %d", got)
	}
}

func TestTheRecommendationFollowsWhatWasMeasured(t *testing.T) {
	dir := t.TempDir()
	cases := []*connTestCase{
		{kind: "reverse", tr: "tcp", name: "a"},
		{kind: "direct", tr: "pck", name: "b"},
		{kind: "reverse", tr: "ws", name: "c"},
	}
	for i := 0; i < 60; i++ {
		cases[1].rtts = append(cases[1].rtts, 70*time.Millisecond+time.Duration(i%5)*time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.log"), []byte("l3: the path carries 1400 bytes — interface lowered\nl3: the path carries 1439 bytes — interface raised\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	results := []ConnTestResult{
		{Kind: "reverse", Transport: "tcp", Status: ctUnstable, OK: 20, Tried: 60},
		{Kind: "direct", Transport: "pck", Status: ctOK, OK: 60, Tried: 60, Mbps: 600},
		{Kind: "reverse", Transport: "ws", Status: ctDown},
	}
	b := ctComputeBest(results, cases, 1440, dir)
	if b.Transport != "direct pck" || b.DirectMTU != 1439 || b.MSS != 1400 {
		t.Errorf("recommendation: %+v", b)
	}
	if b.Preset != PresetAggressive {
		t.Errorf("600 Mbps over 70ms is %d KB in flight and should take Aggressive, got %s", b.BDPKB, b.Preset)
	}
	if b.FECData != 0 || b.LossPct != 0 {
		t.Errorf("no loss, yet FEC %d:%d loss %.1f", b.FECData, b.FECParity, b.LossPct)
	}
	if b.Heartbeat < 10 || b.KeepAlive != 2*b.Heartbeat {
		t.Errorf("timers %d/%d", b.KeepAlive, b.Heartbeat)
	}
	table := ConnTestBestTable(b)
	for _, want := range []string{"BEST SETTINGS", "Direct PCK", "Aggressive", "1440", "1400", "1439", "Keepalive", "FEC             Off"} {
		if !strings.Contains(table, want) {
			t.Errorf("the table lacks %q:\n%s", want, table)
		}
	}
}

func TestConnTestBestUsesTrafficRatherThanTheFirstFailedRow(t *testing.T) {
	cases := []*connTestCase{{}, {rtts: []time.Duration{20 * time.Millisecond}}}
	rows := []ConnTestResult{
		{Kind: "reverse", Transport: "tcp", Status: ctDown, Tried: 60},
		{Kind: "reverse", Transport: "xhttp", Status: ctUnstable, Tried: 60, OK: 40},
	}
	b := ctComputeBest(rows, cases, 0, t.TempDir())
	if b.Transport != "" || b.RTTms != 20 || b.LossPct != 33.3 {
		t.Fatalf("recommendation ignored partial traffic or recommended an unstable tunnel: %+v", b)
	}
}

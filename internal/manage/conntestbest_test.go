package manage

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestConnTestPMTUCancellationStopsAnUnansweredProbe(t *testing.T) {
	for _, acceptMinimum := range []bool{false, true} {
		t.Run(fmt.Sprint(acceptMinimum), func(t *testing.T) {
			socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			unanswered := make(chan struct{})
			go func() {
				buf := make([]byte, 2048)
				for {
					n, peer, err := socket.ReadFromUDP(buf)
					if err != nil {
						return
					}
					if acceptMinimum && n+28 == ctPMTUMin {
						_, _ = socket.WriteToUDP([]byte(fmt.Sprintf("pm %d", ctPMTUMin)), peer)
						continue
					}
					close(unanswered)
					return
				}
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan int, 1)
			go func() { result <- ctProbePMTU(ctx, "127.0.0.1", socket.LocalAddr().(*net.UDPAddr).Port, "tok") }()
			select {
			case <-unanswered:
			case <-time.After(2 * time.Second):
				t.Fatal("probe did not reach the UDP socket")
			}
			cancel()
			select {
			case got := <-result:
				if got != 0 {
					t.Fatalf("cancelled measurement returned a partial MTU: %d", got)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled probe still waits for its UDP deadline")
			}
		})
	}
}

func TestConnTestIPv4LookupHonorsCancellation(t *testing.T) {
	original := net.DefaultResolver
	defer func() { net.DefaultResolver = original }()
	started := make(chan struct{})
	var once sync.Once
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan string, 1)
	go func() { result <- ctIPv4Context(ctx, "blocked-dns.example.invalid") }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("resolver did not start")
	}
	cancel()
	select {
	case got := <-result:
		if got != "" {
			t.Fatalf("cancelled lookup returned %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("DNS lookup ignored cancellation")
	}
	if got := ctIPv4Context(ctx, "127.0.0.1"); got != "" {
		t.Fatal("already cancelled lookup succeeded")
	}
}

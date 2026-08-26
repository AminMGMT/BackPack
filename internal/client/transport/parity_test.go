package transport

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// What every transport has to do, checked against every transport.
//
// This suite exists because of a pattern rather than a bug. Four times in one
// week a problem was solved for one transport and left in place for the others:
//
//	controlDeadline   kcp and quic had it; the five stream transports did not
//	ReportPeer        kcp, quic and l3 had it; the five stream ones did not
//	MSS clamping      l3 had it; the direct port forwarder did not
//	port allocation   the e2e harness had solved it; the direct tests had not
//
// Each was found by hand, one at a time, after somebody reported a tunnel that
// did not work. Finding them individually does not stop the next one: the
// engines are near-copies of each other, so anything added to one is a thing
// the other six are now missing.
//
// So the properties are asserted for all of them at once. The point is not the
// three checks below — it is that the eighth engine, whenever it arrives, has
// to satisfy them before it can ship.

// engineFile maps an engine to the file that implements it. Several transports
// share one engine: stealth is tcp with a Noise layer under it, wss is ws with
// TLS, and xdi, spoof and pck are all KCP over a different carrier.
var clientEngines = []string{"tcp", "tcpmux", "ws", "wsmux", "udp", "kcp", "quic"}

func readEngine(t *testing.T, side, name string) string {
	t.Helper()
	var path string
	switch side {
	case "client":
		path = name + ".go"
	case "server":
		path = filepath.Join("..", "..", "server", "transport", name+".go")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the %s %s engine: %v", side, name, err)
	}
	return string(b)
}

// The engine list has to be the real one. A new engine that the dispatch knows
// about and this file does not would otherwise be exempt from everything here,
// which is exactly how the four above happened.
func TestEveryClientEngineIsCovered(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "client.go"))
	if err != nil {
		t.Fatalf("reading the client dispatch: %v", err)
	}
	// transport.NewKcpClient -> kcp, transport.NewWSMuxClient -> wsmux, and so on.
	found := map[string]bool{}
	for _, m := range regexp.MustCompile(`transport\.New([A-Za-z]+)Client`).FindAllStringSubmatch(string(b), -1) {
		name := strings.ToLower(m[1])
		switch name {
		case "mux": // NewMuxClient is the tcpmux engine
			name = "tcpmux"
		case "spoofpipe": // the WireGuard relay, not a tunnel transport
			continue
		}
		found[name] = true
	}

	covered := map[string]bool{}
	for _, e := range clientEngines {
		covered[e] = true
	}
	var missing []string
	for name := range found {
		if !covered[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("the client dispatches to engines this suite never checks: %v — add them "+
			"to clientEngines, or the parity checks below silently exempt them", missing)
	}
}

// A control channel that has gone quiet has to be noticed by the engine.
//
// Without a deadline the only thing that ends the read is TCP giving up, which
// on the shipped keepalive takes about eleven minutes — and for the datagram
// transports nothing ends it at all. See controlDeadline.
func TestEveryClientEngineBoundsItsControlChannel(t *testing.T) {
	for _, name := range clientEngines {
		src := readEngine(t, "client", name)
		if !strings.Contains(src, "controlDeadline(") {
			t.Errorf("the %s client never bounds its control channel read, so a tunnel "+
				"whose peer has gone silently is noticed only when TCP gives up — or "+
				"never, on a datagram carrier", name)
		}
	}
}

// The watchdog asks the engine whether it holds a control channel, because the
// socket table answers for a socket rather than for a tunnel. An engine that
// does not report leaves the watchdog back on `ss`, where every failure this
// week looked healthy.
func TestEveryEngineReportsWhetherItIsConnected(t *testing.T) {
	for _, side := range []string{"client", "server"} {
		for _, name := range clientEngines {
			src := readEngine(t, side, name)
			if !strings.Contains(src, "metrics.ReportPeer(") {
				t.Errorf("the %s %s engine never reports its peer, so the watchdog has "+
					"nothing to go on but the socket table", side, name)
			}
			if !strings.Contains(src, "metrics.ClearPeer(") {
				t.Errorf("the %s %s engine never clears its peer, so a tunnel that has "+
					"dropped keeps reporting the peer it used to have", side, name)
			}
		}
	}
}

package manage

import (
	"strings"
	"testing"
)

// Real `ss -tin state established` output, from the server side of a tunnel on
// port 5050. The info lines are what they actually look like — which is the
// whole point, because the traps here are all in their shape.
const ssServerSide = `ESTAB 0      0             [::ffff:217.18.94.59]:5050      [::ffff:77.110.121.118]:41234
	 cubic wscale:7,7 rto:304 rtt:98.5/1.5 ato:40 mss:1308 pmtu:1456 rcvmss:536 advmss:1404 cwnd:10 bytes_sent:13879 bytes_acked:13878 bytes_received:22299 segs_out:120 segs_in:118 send 1.1Mbps
ESTAB 0      0             [::ffff:217.18.94.59]:5050      [::ffff:77.110.121.118]:41235
	 cubic wscale:7,7 rto:304 rtt:97.9/1.2 ato:40 mss:1308 pmtu:1456 rcvmss:64 advmss:1404 cwnd:10 bytes_sent:200 bytes_acked:200 bytes_received:100 segs_out:4 segs_in:4 send 1.1Mbps
ESTAB 0      0                       127.0.0.1:5050                  127.0.0.1:55000
	 cubic wscale:7,7 rto:204 rtt:0.1/0.05 mss:65483 pmtu:65535 rcvmss:536 advmss:65483 cwnd:10 bytes_sent:10 bytes_acked:10 bytes_received:10 segs_out:2 segs_in:2 send 5000Mbps
ESTAB 0      0                 217.18.94.59:22                  1.2.3.4:60000
	 cubic wscale:7,7 rto:300 rtt:95/2 mss:1448 pmtu:1500 rcvmss:1448 advmss:1448 cwnd:10 bytes_sent:999 bytes_acked:999 bytes_received:999 segs_out:9 segs_in:9 send 1.2Mbps`

// The client side of the same tunnel: the port is on the *far* end, and the
// local port is ephemeral.
const ssClientSide = `ESTAB 0      0             10.0.0.5:44100      217.18.94.59:5050
	 cubic wscale:7,7 rto:304 rtt:98.5/1.5 ato:40 mss:1308 pmtu:1456 rcvmss:536 advmss:1404 cwnd:10 bytes_sent:500 bytes_acked:500 bytes_received:900 segs_out:9 segs_in:9 send 1.1Mbps
ESTAB 0      0             10.0.0.5:44101      217.18.94.59:5050
	 cubic wscale:7,7 rto:304 rtt:98.1/1.1 ato:40 mss:1308 pmtu:1456 rcvmss:536 advmss:1404 cwnd:10 bytes_sent:100 bytes_acked:100 bytes_received:50 segs_out:3 segs_in:3 send 1.1Mbps`

// The measurement that matters must not be taken from the middle of a different
// one. `mss:1308`, `rcvmss:536` and `advmss:1404` all contain the text "mss:",
// and reading the wrong one produced nonsense — 64, 128, 536 — the first time
// this was attempted by hand.
func TestIntFieldDoesNotMatchInsideAnotherKey(t *testing.T) {
	line := " cubic mss:1308 pmtu:1456 rcvmss:536 advmss:1404 bytes_sent:13879 bytes_received:22299"

	if got := intField(line, " mss:"); got != 1308 {
		t.Errorf("mss = %d, want 1308 (probably read rcvmss or advmss)", got)
	}
	if got := intField(line, " pmtu:"); got != 1456 {
		t.Errorf("pmtu = %d, want 1456", got)
	}
	if got := intField(line, " rcvmss:"); got != 536 {
		t.Errorf("rcvmss = %d, want 536", got)
	}
	if got := intField(line, " advmss:"); got != 1404 {
		t.Errorf("advmss = %d, want 1404", got)
	}
	if got := intField(line, " bytes_sent:"); got != 13879 {
		t.Errorf("bytes_sent = %d, want 13879", got)
	}
	if got := intField(line, " missing:"); got != 0 {
		t.Errorf("an absent key gave %d, want 0", got)
	}

	// The case the leading space actually exists for. A connection that has not
	// sent anything yet is reported without a plain `mss:` at all — and without
	// the space, the search then lands inside `advmss:` and hands back a
	// completely different measurement while looking perfectly reasonable.
	sparse := " cubic wscale:7,7 rto:204 advmss:1404 rcvmss:536 cwnd:10"
	if got := intField(sparse, " mss:"); got != 0 {
		t.Errorf("a line with no mss: gave %d, want 0 — it matched inside advmss: or rcvmss:", got)
	}
}

// From the server, the tunnel port is the local one — and loopback and unrelated
// services must not be counted as tunnel connections.
func TestParseSocketsFromTheServerSide(t *testing.T) {
	got := parseSockets(ssServerSide, "5050")

	if len(got) != 2 {
		t.Fatalf("found %d tunnel connections, want 2: %+v", len(got), got)
	}
	for _, s := range got {
		if s.peer != "77.110.121.118" {
			t.Errorf("peer = %q, want the client's address (loopback should be skipped)", s.peer)
		}
		if s.mss != 1308 {
			t.Errorf("mss = %d, want 1308", s.mss)
		}
		if s.pmtu != 1456 {
			t.Errorf("pmtu = %d, want 1456", s.pmtu)
		}
	}
	if got[0].sent != 13879 || got[0].recv != 22299 {
		t.Errorf("byte counters = %d/%d, want 13879/22299", got[0].sent, got[0].recv)
	}
}

// From the client, the tunnel port is on the far end. Looking only at the local
// port — which is what the first attempt did — finds nothing at all here.
func TestParseSocketsFromTheClientSide(t *testing.T) {
	got := parseSockets(ssClientSide, "5050")

	if len(got) != 2 {
		t.Fatalf("found %d tunnel connections, want 2 — the port is on the peer, not the local end", len(got))
	}
	if got[0].peer != "217.18.94.59" {
		t.Errorf("peer = %q, want the server's address", got[0].peer)
	}
	if got[0].mss != 1308 {
		t.Errorf("mss = %d, want 1308", got[0].mss)
	}
}

// An unrelated port must match nothing, or every diagnostic would report
// whatever else the machine happens to be doing.
func TestParseSocketsIgnoresOtherPorts(t *testing.T) {
	if got := parseSockets(ssServerSide, "9999"); len(got) != 0 {
		t.Fatalf("port 9999 matched %d connections: %+v", len(got), got)
	}
	// Port 22 is in the sample, so it should match exactly its one connection.
	if got := parseSockets(ssServerSide, "22"); len(got) != 1 {
		t.Fatalf("port 22 matched %d connections, want 1", len(got))
	}
}

func TestParseSocketsHandlesEmptyOutput(t *testing.T) {
	if got := parseSockets("", "5050"); len(got) != 0 {
		t.Fatalf("empty output produced %d connections", len(got))
	}
	// Header-only output, which is what `ss` prints when nothing matches.
	if got := parseSockets("State Recv-Q Send-Q Local Address:Port Peer Address:Port\n", "5050"); len(got) != 0 {
		t.Fatalf("header-only output produced %d connections", len(got))
	}
}

// The arithmetic that decides what to tell the operator to set. Getting this
// wrong by twelve bytes is exactly the case it exists for: the textbook
// mtu-40 leaves no room for the timestamps option Linux turns on by default.
func TestSafeMSS(t *testing.T) {
	// The path measured on the tunnel this was written for.
	if got := safeMSS(1456); got != 1404 {
		t.Errorf("safeMSS(1456) = %d, want 1404", got)
	}
	if got := safeMSS(1500); got != 1448 {
		t.Errorf("safeMSS(1500) = %d, want 1448", got)
	}
	// And it must stay under the naive figure, which is the whole point.
	for _, mtu := range []int{1400, 1456, 1492, 1500} {
		if safeMSS(mtu) >= mtu-40 {
			t.Errorf("safeMSS(%d) = %d, not below the naive mtu-40 = %d", mtu, safeMSS(mtu), mtu-40)
		}
	}
}

// A connection replaced between the two samples makes the counter appear to go
// backwards. Reporting a huge negative-wrapped figure would be worse than
// reporting nothing.
func TestSaturatingSub(t *testing.T) {
	if got := saturatingSub(500, 100); got != 400 {
		t.Errorf("saturatingSub(500,100) = %d, want 400", got)
	}
	if got := saturatingSub(100, 500); got != 0 {
		t.Errorf("a counter that went backwards gave %d, want 0", got)
	}
	if got := saturatingSub(0, 0); got != 0 {
		t.Errorf("saturatingSub(0,0) = %d, want 0", got)
	}
}

// The false alarm the field reported as "it complains about the path MTU in
// every case, automatic or manual".
//
// Two separate mistakes produced it, and either alone is enough to condemn a
// working tunnel.

// One: the kernel's snd_mss already has the TCP option bytes taken out of it,
// so testing it against safeMSS — which takes them out a second time — calls a
// perfectly ordinary socket oversized.
func TestASocketWithoutTimestampsIsNotOversized(t *testing.T) {
	// A 1500-byte path. With timestamps the kernel reports 1448, without them
	// 1460. Both fit: 1460 + 20 + 20 = 1500 exactly.
	for _, mss := range []int{1448, 1460} {
		c := pathMTUCheck("g", 1500, mss, true)
		if c.Level != CheckOK {
			t.Errorf("a 1500-byte path carrying mss %d was reported as %v: %s",
				mss, c.Level, c.Detail)
		}
	}
	// And the segment that genuinely cannot fit still is a fault.
	if c := pathMTUCheck("g", 1500, 1461, true); c.Level != CheckFail {
		t.Errorf("mss 1461 does not fit in 1500 and must be a fault, got %v", c.Level)
	}
}

// Two: the ping probe under-reports on any path that filters large ICMP, which
// is ordinary on the routes this project exists for. A socket moving traffic at
// a size the probe says is impossible is the probe being wrong.
func TestAnICMPFilteringPathDoesNotCondemnTheTunnel(t *testing.T) {
	// The probe bottomed out at 1000 while the sockets carry 1308.
	probe := pathMTUCheck("g", 1000, 1308, false)
	if probe.Level == CheckFail {
		t.Errorf("a probe-only figure was treated as fact and failed a working tunnel: %s",
			probe.Detail)
	}
	if probe.Level != CheckWarn {
		t.Errorf("the disagreement is worth mentioning, so it should warn, got %v", probe.Level)
	}
	if !strings.Contains(probe.Detail, "1308") || !strings.Contains(probe.Detail, "1000") {
		t.Errorf("the warning should name both figures so the operator can judge: %s", probe.Detail)
	}

	// The same numbers from the kernel are a real fault, because the kernel
	// learned them by carrying traffic rather than by pinging.
	kernel := pathMTUCheck("g", 1000, 1308, true)
	if kernel.Level != CheckFail {
		t.Errorf("the kernel's own pmtu should be believed, got %v: %s", kernel.Level, kernel.Detail)
	}
}

// The kernel's pmtu is preferred over the probe, and the probe is used only
// when the kernel has nothing to say. This is what stops the probe from being
// consulted at all on a healthy tunnel.
func TestTheKernelIsAskedBeforeThePingProbe(t *testing.T) {
	sockets := []sockStat{
		{peer: "203.0.113.9", mss: 1308, pmtu: 1456},
		{peer: "203.0.113.9", mss: 1308, pmtu: 1456},
	}
	mtu, fromKernel := pathMTU(sockets, "203.0.113.9")
	if !fromKernel {
		t.Fatal("the kernel reported a pmtu and it was ignored in favour of pinging")
	}
	if mtu != 1456 {
		t.Errorf("path mtu = %d, want the kernel's 1456", mtu)
	}

	// With no kernel figure and nowhere to ping, there is simply no answer —
	// which must be reported as unknown rather than guessed.
	if mtu, fromKernel := pathMTU([]sockStat{{mss: 1308}}, ""); mtu != 0 || fromKernel {
		t.Errorf("with nothing to go on, got mtu %d fromKernel %v; want 0 and false", mtu, fromKernel)
	}
}

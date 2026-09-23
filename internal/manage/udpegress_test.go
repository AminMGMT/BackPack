package manage

import (
	"os"
	"strings"
	"testing"
	"time"
)

// What the reading is allowed to change.
//
// The recommendation for a lossy link has always been a UDP carrier, and it has
// always carried a sentence telling the operator to go and check that UDP works
// before committing — which is the tool telling somebody to find out something
// the tool had not asked. These hold what asking is for.

func lossy() PathQuality {
	return PathQuality{Target: "example:443", Sent: 12, Received: 8, Avg: 80 * time.Millisecond}
}

func clean() PathQuality {
	return PathQuality{Target: "example:443", Sent: 12, Received: 12,
		Avg: 40 * time.Millisecond, Jitter: 2 * time.Millisecond}
}

func joined(list []string) string { return strings.Join(list, " | ") }

// The case that matters: a link whose loss argues for KCP, on a network where
// UDP cannot leave. KCP there is not a slower choice, it is one that never
// comes up.
func TestAUDPCarrierIsNotRecommendedWhereUDPCannotLeave(t *testing.T) {
	blocked := UDPEgress{Tried: 3, Answered: 0}
	rec := RecommendTransport(lossy(), "tcp", blocked)

	if needsUDP(rec.Transport) {
		t.Fatalf("recommended %s on a network with no UDP egress", rec.Transport)
	}
	if !strings.Contains(joined(rec.Why), "UDP does not appear to leave") {
		t.Errorf("the recommendation does not say why it moved: %v", rec.Why)
	}
	// And it does not pretend the second-best answer is the best one.
	if !strings.Contains(joined(rec.Caveats), "second-best") {
		t.Errorf("the recommendation does not say it is a fallback: %v", rec.Caveats)
	}
	// pck is the route to KCP that needs no UDP, and it is worth naming here.
	if !strings.Contains(joined(rec.Caveats), "PCK") {
		t.Errorf("the alternative that carries KCP without UDP is not mentioned: %v", rec.Caveats)
	}
}

// When UDP does work, the caveat that told the operator to go and check is
// replaced by the reading. A caveat that has been answered and is still printed
// teaches people to skip caveats.
func TestTheCaveatIsReplacedByTheReadingWhenUDPWorks(t *testing.T) {
	works := UDPEgress{Tried: 1, Answered: 1, Via: "1.1.1.1:53", RTT: 19 * time.Millisecond}
	rec := RecommendTransport(lossy(), "tcp", works)

	if !needsUDP(rec.Transport) {
		t.Fatalf("a lossy link with working UDP was recommended %s", rec.Transport)
	}
	if strings.Contains(joined(rec.Caveats), "test it before committing") {
		t.Errorf("the tool still asks the operator to check what it has measured: %v", rec.Caveats)
	}
	if !strings.Contains(joined(rec.Why), "1.1.1.1:53") {
		t.Errorf("the reading is not reported: %v", rec.Why)
	}
}

// A probe that could not be taken is not a probe that failed, and must change
// nothing — including the caveat, which is still the right advice when nothing
// has been measured.
func TestAnUnmeasuredNetworkChangesNothing(t *testing.T) {
	none := UDPEgress{}
	rec := RecommendTransport(lossy(), "tcp", none)

	if !needsUDP(rec.Transport) {
		t.Fatalf("an unmeasured network moved the recommendation to %s", rec.Transport)
	}
	if !strings.Contains(joined(rec.Caveats), "test it before committing") {
		t.Errorf("the caveat was dropped without anything having been measured: %v", rec.Caveats)
	}
}

// A clean link is recommended TCP either way, and the finding still rules out
// the alternatives the caveats offer rather than saying nothing.
func TestACleanLinkIsToldWhatIsRuledOut(t *testing.T) {
	blocked := UDPEgress{Tried: 3, Answered: 0}
	rec := RecommendTransport(clean(), "tcp", blocked)

	if needsUDP(rec.Transport) {
		t.Fatalf("a clean link was recommended %s", rec.Transport)
	}
	if !strings.Contains(joined(rec.Caveats), "are not options on this network") {
		t.Errorf("a network with no UDP egress did not rule the UDP carriers out: %v", rec.Caveats)
	}
}

// pck carries KCP inside packets it builds below the kernel, which is the whole
// reason it exists — so it must not be treated as needing UDP egress.
func TestPckIsNotAUDPCarrierForThisPurpose(t *testing.T) {
	for _, transport := range []string{"kcp", "quic", "udp", "xdi"} {
		if !needsUDP(transport) {
			t.Errorf("%s needs UDP and was not treated as needing it", transport)
		}
	}
	for _, transport := range []string{"tcp", "tcpmux", "ws", "wss", "wsmux", "wssmux", "stealth", "pck"} {
		if needsUDP(transport) {
			t.Errorf("%s does not need UDP egress and was treated as if it did", transport)
		}
	}
}

// The DNS query is a query: an answer to somebody else's question, or a packet
// that is not a response at all, must not be read as UDP working.
func TestOnlyAnAnswerToOurOwnQuestionCounts(t *testing.T) {
	id := [2]byte{0xAB, 0xCD}
	q := dnsQuery(id)
	if len(q) < 12 {
		t.Fatalf("the query is %d bytes, which is not a DNS message", len(q))
	}
	if q[0] != id[0] || q[1] != id[1] {
		t.Fatal("the query does not carry the transaction ID it was given")
	}
	if q[2]&0x80 != 0 {
		t.Fatal("the query is marked as a response")
	}
	// example.com, which is reserved for exactly this and says nothing about
	// the operator.
	if !strings.Contains(string(q), "example") || !strings.Contains(string(q), "com") {
		t.Fatalf("the query asks for something other than the reserved name: %q", q)
	}
}

// The decision must stay a decision.
//
// This guard exists because the first version of the reading did not have one.
// RecommendTransport called ProbeUDPEgress() itself, which put a live DNS query
// inside a pure function: every test of the recommendation became a test of the
// network the test happened to run on. It passed on a workstation and failed on
// a CI runner that blocks outbound UDP/53 — a failure with nothing to do with
// the code under test, which then blocked the release build behind it.
//
// Probing is I/O and belongs to the callers that are already doing I/O. The
// list below is those callers; a new name in it is a decision to make on
// purpose, not one to arrive at by reaching for the network from somewhere new.
func TestTheRecommendationDoesNotReachForTheNetworkItself(t *testing.T) {
	mayProbe := map[string]bool{
		"linktest.go":      true, // already runs the path measurement
		"benchmarkmenu.go": true, // already runs the benchmark
		"udpegress.go":     true, // defines it
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(stripComments(string(src)), "ProbeUDPEgress()") {
			continue
		}
		if !mayProbe[name] {
			t.Errorf("%s calls ProbeUDPEgress(): the reading should be passed in, "+
				"so the code stays testable without outbound UDP", name)
		}
	}
}

// stripComments blanks out // and /* */ comments so a file that only mentions
// the probe while explaining why it does not call it is not mistaken for one
// that does.
func stripComments(src string) string {
	var out strings.Builder
	for i := 0; i < len(src); {
		switch {
		case strings.HasPrefix(src[i:], "//"):
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				return out.String()
			}
			i += end
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out.String()
			}
			i += end + 4
		default:
			out.WriteByte(src[i])
			i++
		}
	}
	return out.String()
}

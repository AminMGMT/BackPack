package manage

import (
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
	rec := recommendWith(lossy(), "tcp", blocked)

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
	rec := recommendWith(lossy(), "tcp", works)

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
	rec := recommendWith(lossy(), "tcp", none)

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
	rec := recommendWith(clean(), "tcp", blocked)

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

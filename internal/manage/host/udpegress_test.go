package host

import (
	"strings"
	"testing"
)

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

package manage

import (
	"crypto/rand"
	"encoding/binary"
	"net"
	"time"
)

// Does UDP leave this machine at all?
//
// Every recommendation this tool makes for a lossy link is a UDP carrier — KCP,
// QUIC, plain UDP — and every one of them carries the same caveat, written
// where RecommendTransport builds it: *"KCP runs over UDP — if your provider
// throttles UDP this will be worse, not better, so test it before committing."*
//
// That sentence is an admission. The tool measured the path with TCP connects,
// concluded the link was lossy, recommended a UDP carrier, and then told the
// operator to go and find out for themselves whether UDP works — which they
// mostly cannot, because the far end refuses to answer an unauthenticated
// datagram by design and there is nothing to test against.
//
// This measures it instead. Not against the tunnel port, which cannot answer,
// but against the public resolvers that answer UDP to anybody: if a DNS query
// leaves this machine over UDP and comes back, UDP egress is not blocked. If
// none of them answer, recommending a UDP carrier is recommending something
// that cannot work, and the recommendation says so rather than hedging.
//
// # What it does not claim
//
// It answers "can UDP leave here", not "can UDP reach that port on that
// server". A provider that allows DNS and blocks everything else would pass
// this and still fail a KCP tunnel. The narrower question needs a far end that
// will answer, which needs a wire addition on both sides — see §4.1/§4.2 in
// todo.md. What this closes is the case that actually happens: a network where
// UDP does not leave at all, which today is discovered by building the tunnel
// and watching it never come up.
//
// # Why DNS and not a purpose-built responder
//
// Because it needs no infrastructure and no trust. A query to a public resolver
// is the most ordinary UDP packet on the internet, it is answered in
// milliseconds by several independent operators, and nothing about it reveals
// anything: it asks for the address of a name the operator did not choose and
// is not going to connect to.

// udpEgressProbes are the resolvers asked, in order. Three operators rather
// than one, on different networks, so that a single provider's outage or a
// single blocked address is not read as "UDP does not work here".
var udpEgressProbes = []string{
	"1.1.1.1:53",
	"8.8.8.8:53",
	"9.9.9.9:53",
}

// udpEgressTimeout is per resolver. Short: this runs while an operator is
// looking at a menu, and a resolver that has not answered in this long is not
// the one that is going to.
const udpEgressTimeout = 1500 * time.Millisecond

// UDPEgress is what the probe found.
type UDPEgress struct {
	// Tried is how many resolvers were asked, Answered how many replied.
	Tried    int
	Answered int
	// Via names the first resolver that answered, so the reading can be
	// repeated by hand.
	Via string
	// RTT is how long that one took.
	RTT time.Duration
}

// Works reports whether UDP left this machine and came back.
func (u UDPEgress) Works() bool { return u.Answered > 0 }

// Checked reports whether the probe ran at all. A probe that could not be taken
// is not a probe that failed, and the difference decides whether a
// recommendation may lean on it.
func (u UDPEgress) Checked() bool { return u.Tried > 0 }

// ProbeUDPEgress asks whether UDP datagrams leave this machine and come back.
//
// It stops at the first answer: one resolver answering is the whole finding,
// and asking the other two afterwards would only slow down a menu.
func ProbeUDPEgress() UDPEgress {
	var out UDPEgress
	for _, resolver := range udpEgressProbes {
		out.Tried++
		rtt, ok := askResolver(resolver)
		if ok {
			out.Answered++
			out.Via = resolver
			out.RTT = rtt
			return out
		}
	}
	return out
}

// askResolver sends one DNS query over UDP and waits for a reply.
//
// The reply is not parsed beyond checking that it is a DNS message answering
// the question that was asked: the content is irrelevant, and what is being
// measured is that a datagram went out and one came back. Matching the
// transaction ID is what stops a stray datagram from another conversation being
// read as an answer.
func askResolver(addr string) (time.Duration, bool) {
	conn, err := net.DialTimeout("udp", addr, udpEgressTimeout)
	if err != nil {
		return 0, false
	}
	defer conn.Close()

	var id [2]byte
	if _, err := rand.Read(id[:]); err != nil {
		return 0, false
	}
	query := dnsQuery(id)

	if err := conn.SetDeadline(time.Now().Add(udpEgressTimeout)); err != nil {
		return 0, false
	}
	start := time.Now()
	if _, err := conn.Write(query); err != nil {
		return 0, false
	}

	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil || n < 12 {
		return 0, false
	}
	// The transaction ID, and the response bit.
	if buf[0] != id[0] || buf[1] != id[1] || buf[2]&0x80 == 0 {
		return 0, false
	}
	return time.Since(start), true
}

// dnsQuery builds a minimal A query for a name chosen to be uninteresting:
// example.com is reserved by the IETF for exactly this kind of use, so nothing
// about asking for it says anything about the operator.
func dnsQuery(id [2]byte) []byte {
	var q []byte
	q = append(q, id[0], id[1])
	q = append(q, 0x01, 0x00) // standard query, recursion desired
	q = binary.BigEndian.AppendUint16(q, 1)
	q = binary.BigEndian.AppendUint16(q, 0)
	q = binary.BigEndian.AppendUint16(q, 0)
	q = binary.BigEndian.AppendUint16(q, 0)
	for _, label := range []string{"example", "com"} {
		q = append(q, byte(len(label)))
		q = append(q, label...)
	}
	q = append(q, 0)
	q = binary.BigEndian.AppendUint16(q, 1) // A
	q = binary.BigEndian.AppendUint16(q, 1) // IN
	return q
}

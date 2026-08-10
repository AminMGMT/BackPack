package manage

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// The two questions a tunnel could not answer about itself.
//
// A tunnel that connects and then carries nothing is the hardest failure in
// this system to attribute, because every surface reports health: the service
// is running, the control channel is up, the panel shows it online. Working out
// what was actually wrong meant counting sockets by hand and sampling byte
// counters between two runs of `ss` — an afternoon of it, for a fault that the
// tunnel had all the information to describe in a sentence.
//
// Two things turned out to matter, and neither was visible anywhere:
//
//   - How much of the pool exists. A control channel with no pool behind it
//     looks exactly like a healthy tunnel until somebody tries to use it.
//   - Whether the path can carry a full-sized packet. Where it cannot, and the
//     network declines to say so with an ICMP message, TCP never finds out: the
//     small packets — the handshake, the heartbeats — arrive, so the tunnel
//     comes up and stays up, and every real transfer stalls on the first
//     full-sized segment. Nothing in the tunnel, the kernel or the panel
//     reports this. It has to be measured on purpose.
//
// Both are read-only measurements taken from the live tunnel.

// sockStat is what the kernel knows about one live tunnel connection.
type sockStat struct {
	peer string
	mss  int
	pmtu int
	sent uint64
	recv uint64
}

// tunnelSockets reports the established connections on a tunnel's port, with
// the kernel's view of each. It is the same information `ss -tin` prints,
// parsed rather than eyeballed.
//
// Either endpoint may be the one holding the tunnel port, and which it is
// depends on the role: a server's connections have it locally, a client's have
// it on the far side with an ephemeral port of their own. Matching both is what
// makes this work from either end.
func tunnelSockets(port string) []sockStat {
	out, err := exec.Command("ss", "-tin", "state", "established").Output()
	if err != nil {
		return nil
	}
	return parseSockets(string(out), port)
}

// parseSockets is the parsing half, kept apart from the command so it can be
// tested against real `ss` output. Both mistakes this has already made — taking
// "mss" out of the middle of "rcvmss", and looking only at the local end of the
// connection — were parsing mistakes, and neither was reachable by a test while
// this was welded to exec.
func parseSockets(out, port string) []sockStat {
	var stats []sockStat
	var cur *sockStat
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			// The indented line carries the kernel's measurements for the
			// connection named on the line before it.
			if cur != nil {
				cur.mss = intField(line, " mss:")
				cur.pmtu = intField(line, " pmtu:")
				cur.sent = uint64(intField(line, " bytes_sent:"))
				cur.recv = uint64(intField(line, " bytes_received:"))
				stats = append(stats, *cur)
				cur = nil
			}
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		local, peer := f[len(f)-2], f[len(f)-1]
		if !strings.HasSuffix(local, ":"+port) && !strings.HasSuffix(peer, ":"+port) {
			continue
		}
		host := peer
		if i := strings.LastIndex(peer, ":"); i >= 0 {
			host = peer[:i]
		}
		host = strings.Trim(host, "[]")
		host = strings.TrimPrefix(host, "::ffff:")
		if host == "" || host == "127.0.0.1" || host == "::1" {
			continue
		}
		cur = &sockStat{peer: host}
	}
	return stats
}

// intField pulls a numeric value out of an `ss` info line. The key must include
// its leading space: without it "mss:" also matches inside "rcvmss:" and
// "advmss:", which are different measurements entirely.
func intField(line, key string) int {
	i := strings.Index(line, key)
	if i < 0 {
		return 0
	}
	rest := line[i+len(key):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	n, _ := strconv.Atoi(rest[:end])
	return n
}

// probePathMTU finds the largest packet that reaches host, by binary search
// over ICMP with the don't-fragment bit set.
//
// It returns 0 when the question cannot be answered — the path drops ICMP
// entirely, or ping is not installed — which is reported as unknown rather than
// guessed at. Guessing here would be worse than silence: a wrong MTU would have
// the operator clamping a connection that was never the problem.
func probePathMTU(host string) int {
	if host == "" {
		return 0
	}
	if !pingOK(host, 64) {
		return 0 // ICMP does not survive this path at all
	}

	lo, hi, best := 512, 1472, 64
	for lo <= hi {
		mid := (lo + hi) / 2
		if pingOK(host, mid) {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best + 28 // IP header + ICMP header
}

func pingOK(host string, payload int) bool {
	cmd := exec.Command("ping", "-c", "1", "-W", "2", "-M", "do", "-s", strconv.Itoa(payload), host)
	return cmd.Run() == nil
}

// safeMSS is the largest TCP payload that fits inside mtu.
//
// It subtracts a full 40 bytes of headers plus 12 for the timestamps option,
// which Linux enables by default. The textbook figure of mtu-40 assumes a
// bare 20-byte TCP header and produces segments 12 bytes too large on exactly
// the paths where 12 bytes is the difference.
func safeMSS(mtu int) int { return mtu - 20 - 32 }

// minPathMTU is the smallest IPv4 datagram every path is required to carry.
const minPathMTU = 576

// The range an MSS clamp has to fall inside, shared by the check that names a
// value and the setting that applies it.
//
// The floor is derived rather than picked, so that every number pathMTUCheck
// can print is a number SetMSS will accept. Those two drifting apart is the
// worst failure available here: a diagnostic that names the fix and a tunnel
// that then refuses it leaves the operator with nowhere to go, which is how
// this fault wasted an afternoon in the first place.
//
// The ceiling is the textbook maximum for a 1500-byte Ethernet MTU less a bare
// IPv4 and TCP header. A clamp above it can never bind on an ordinary link, so
// accepting one would only ever mean accepting a typo.
var (
	minMSS = safeMSS(minPathMTU) // 524
	maxMSS = 1500 - 20 - 20      // 1460
)

// pathMTUCheck turns a measured path MTU and the segment size the tunnel is
// actually sending into the line the operator reads.
//
// It is kept pure and apart from the measurement so the one property that must
// hold — that any clamp it prints is one SetMSS accepts — can be tested across
// the whole range the probe can report, rather than only on the paths someone
// happened to have.
func pathMTUCheck(g string, mtu, negotiated int) Check {
	clamp := safeMSS(mtu)
	switch {
	case negotiated > clamp && clamp < minMSS:
		// Below what IPv4 guarantees every route carries. No clamp rescues
		// this: the value it would need is one no TCP stack is obliged to
		// accept, and the tunnel would crawl even if both ends took it. Saying
		// so beats printing a number the tunnel will refuse.
		return Check{Group: g, Name: "Path MTU", Level: CheckFail,
			Detail: fmt.Sprintf("path carries only %d bytes, below the %d every IPv4 route must — too small to tunnel over", mtu, minPathMTU),
			Fix:    "this is a fault in the route rather than in the tunnel — look for a broken tunnel or VPN in front of it"}

	case negotiated > clamp:
		// The measured limit is below what the sockets are already sending.
		// Nothing reports this on its own: the oversized segments are dropped
		// without an ICMP reply, so the kernel keeps sending them and the
		// tunnel keeps looking healthy while every real transfer stalls.
		return Check{Group: g, Name: "Path MTU", Level: CheckFail,
			Detail: fmt.Sprintf("path carries %d bytes but the tunnel sends segments of %d — larger packets are being dropped silently", mtu, negotiated),
			Fix:    fmt.Sprintf("set the MSS clamp to %d on both ends — Edit → TCP MSS clamp, or Fine Tune in the panel", clamp)}

	case mtu < 1500:
		return Check{Group: g, Name: "Path MTU", Level: CheckOK,
			Detail: fmt.Sprintf("%d (below 1500, and the tunnel is already inside it at mss %d)", mtu, negotiated)}

	default:
		return Check{Group: g, Name: "Path MTU", Level: CheckOK,
			Detail: fmt.Sprintf("%d, full-sized packets get through", mtu)}
	}
}

// pathChecks measures what the tunnel could not say about itself: how much of
// the pool is really there, and whether the path can carry a full-sized packet.
func pathChecks(g string, t Tunnel) []Check {
	var out []Check

	port := addrPort(t.Addr)
	if port == "" || isDatagram(t.Transport) {
		// A datagram tunnel has no entry in the TCP socket table, so none of
		// this can be measured from here.
		return out
	}

	before := tunnelSockets(port)
	if len(before) == 0 {
		out = append(out, Check{Group: g, Name: "Pool", Level: CheckFail,
			Detail: "no established tunnel connections",
			Fix:    "the peer is not connected — check the other side's log and that both hold the same token"})
		return out
	}

	// A control channel on its own is not a working tunnel: the pool is what
	// carries the traffic, and a tunnel with one connection and no pool looks
	// healthy from every other angle while forwarding nothing.
	peer := before[0].peer
	if len(before) == 1 {
		out = append(out, Check{Group: g, Name: "Pool", Level: CheckWarn,
			Detail: "control channel only, no pool connections",
			Fix:    "the client is not maintaining a pool — check its log for dial failures"})
	} else {
		out = append(out, Check{Group: g, Name: "Pool", Level: CheckOK,
			Detail: fmt.Sprintf("%d connections with %s", len(before), peer)})
	}

	// What is actually crossing, right now. Reported rather than judged: an
	// idle tunnel legitimately carries nothing, and calling that a fault would
	// cry wolf on every quiet tunnel.
	time.Sleep(1500 * time.Millisecond)
	var sent, recv uint64
	for _, s := range tunnelSockets(port) {
		sent += s.sent
		recv += s.recv
	}
	var was uint64
	var wasRecv uint64
	for _, s := range before {
		was += s.sent
		wasRecv += s.recv
	}
	out = append(out, Check{Group: g, Name: "Traffic", Level: CheckInfo,
		Detail: fmt.Sprintf("%s out, %s in over 1.5s",
			humanSize(int(saturatingSub(sent, was))), humanSize(int(saturatingSub(recv, wasRecv))))})

	// And the question that costs the most to answer by hand.
	mtu := probePathMTU(peer)
	if mtu == 0 {
		out = append(out, Check{Group: g, Name: "Path MTU", Level: CheckInfo,
			Detail: "could not probe — the path drops ICMP"})
		return out
	}

	negotiated := 0
	for _, s := range before {
		if s.mss > negotiated {
			negotiated = s.mss
		}
	}

	return append(out, pathMTUCheck(g, mtu, negotiated))
}

// saturatingSub avoids a wild figure when a connection is replaced between the
// two samples and the counter appears to go backwards.
func saturatingSub(now, then uint64) uint64 {
	if now < then {
		return 0
	}
	return now - then
}

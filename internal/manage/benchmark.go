package manage

import (
	"fmt"
	"math"
	"net"
	"sort"
	"time"
)

// Path benchmarking.
//
// Choosing a transport is really a question about the link between the two
// servers: how far away is it, how steady is it, and does anything get lost on
// the way. Those three numbers — latency, jitter and loss — decide the answer,
// so that is exactly what this measures.
//
// It deliberately does NOT claim to measure throughput. Honest throughput needs
// a cooperating peer pushing real traffic (iperf3 does this properly); a number
// invented from a handful of handshakes would look precise and mean nothing.

const (
	probeSamples = 12
	probeTimeout = 4 * time.Second
	// A short gap between samples keeps a burst of connections from being
	// treated as a flood by the far side, and lets jitter show up.
	probeGap = 150 * time.Millisecond
)

// PathQuality is the measured state of the link to a peer.
type PathQuality struct {
	Target string

	Sent     int
	Received int

	Min    time.Duration
	Avg    time.Duration
	Max    time.Duration
	Jitter time.Duration // mean deviation from the average

	// Err is set when the probe could not run at all (an unusable address).
	Err error
}

// LossPercent returns the share of probes that never completed.
func (p PathQuality) LossPercent() float64 {
	if p.Sent == 0 {
		return 0
	}
	return float64(p.Sent-p.Received) / float64(p.Sent) * 100
}

// Usable reports whether enough probes came back to trust the numbers.
func (p PathQuality) Usable() bool { return p.Err == nil && p.Received >= 2 }

// ProbePath measures latency, jitter and loss to a host:port over TCP.
//
// TCP is used rather than ICMP on purpose: plenty of networks on this route
// drop ping entirely while carrying tunnel traffic perfectly well, so a ping
// based measurement would report a dead link that is in fact fine.
func ProbePath(target string) PathQuality {
	q := PathQuality{Target: target}

	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" || port == "" {
		q.Err = fmt.Errorf("cannot understand the address %q", target)
		return q
	}

	var samples []time.Duration
	for i := 0; i < probeSamples; i++ {
		q.Sent++
		start := time.Now()
		conn, err := net.DialTimeout("tcp", target, probeTimeout)
		if err != nil {
			time.Sleep(probeGap)
			continue
		}
		samples = append(samples, time.Since(start))
		conn.Close()
		q.Received++
		time.Sleep(probeGap)
	}

	if len(samples) == 0 {
		return q
	}

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	q.Min = samples[0]
	q.Max = samples[len(samples)-1]

	var total time.Duration
	for _, s := range samples {
		total += s
	}
	q.Avg = total / time.Duration(len(samples))

	// Mean absolute deviation: a steadier link than the min/max spread alone
	// suggests, because one slow outlier does not dominate it.
	var deviation float64
	for _, s := range samples {
		deviation += math.Abs(float64(s - q.Avg))
	}
	q.Jitter = time.Duration(deviation / float64(len(samples)))

	return q
}

// Recommendation is a suggested transport with the reasoning behind it.
type Recommendation struct {
	Transport string
	Label     string
	Preset    string
	Why       []string
	// Caveats are things the user must check or accept before applying it.
	Caveats []string
	// FEC is the parity ratio to run when Transport is KCP, sized to the loss
	// that was measured. Zero when the recommendation is not a KCP transport.
	FEC FECPlan
}

// FECPlan is a forward-error-correction ratio: for every Data packets the link
// carries Parity extra ones, so up to Parity losses in each group of Data+Parity
// are repaired without waiting a round trip for a retransmit. Both ends must run
// the same ratio.
type FECPlan struct {
	Data   int
	Parity int
	Why    string
}

// Set reports whether the plan carries a real ratio.
func (f FECPlan) Set() bool { return f.Data > 0 && f.Parity > 0 }

// Ratio renders the plan as "data:parity".
func (f FECPlan) Ratio() string { return fmt.Sprintf("%d:%d", f.Data, f.Parity) }

// RecommendFEC sizes the FEC ratio to the measured loss. This is the single
// setting that most decides how a KCP link feels on a lossy route: too little
// parity and lost packets still stall waiting for a retransmit, too much and the
// parity traffic itself congests the link.
//
// The rule is that parity/(data+parity) has to exceed the loss rate with
// headroom, because loss arrives in bursts rather than evenly, so each tier
// tolerates comfortably more than the loss that selects it. The ratios are the
// ones proven on this kind of route; the data shard count drops as parity climbs
// so a very lossy link is not also carrying huge FEC groups.
func RecommendFEC(q PathQuality) FECPlan {
	loss := q.LossPercent()
	switch {
	case !q.Usable():
		// Nothing trustworthy to size against: the gaming default, which every
		// KCP preset already carries.
		return FECPlan{10, 3, "link not measurable — the default gaming ratio"}
	case loss < 1:
		return FECPlan{20, 5, fmt.Sprintf("%.0f%% loss — a light 25%% parity is plenty on a clean route", loss)}
	case loss < 5:
		return FECPlan{10, 5, fmt.Sprintf("%.0f%% loss — 10:5 repairs bursts up to ~33%%", loss)}
	case loss < 15:
		return FECPlan{8, 8, fmt.Sprintf("%.0f%% loss — 8:8 repairs bursts up to ~50%%", loss)}
	default:
		return FECPlan{4, 8, fmt.Sprintf("%.0f%% loss — 4:8 trades 200%% overhead for repairing up to ~67%%", loss)}
	}
}

// SetFEC applies a parity ratio to a KCP tunnel and restarts it. Like the other
// measured tunings it clears the preset, because the numbers no longer match a
// named profile and a later preset change must not silently overwrite them.
func SetFEC(name string, plan FECPlan) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if !isKCP(s.Transport) {
		return fmt.Errorf("FEC only applies to a KCP-based transport")
	}
	if s.KCPDataShards == plan.Data && s.KCPParityShards == plan.Parity {
		return fmt.Errorf("the tunnel already uses %s FEC", plan.Ratio())
	}
	s.KCPDataShards = plan.Data
	s.KCPParityShards = plan.Parity
	s.Preset = ""
	return applySpec(s)
}

// RecommendTransport turns a path measurement into a transport choice.
//
// The thresholds encode what actually matters on this route:
//
//   - Loss is what KCP's error correction exists to fix. Below a couple of
//     percent it is not worth the parity traffic; above that it is the single
//     biggest win available.
//   - Jitter that is large relative to the latency means a congested or shaped
//     path, where multiplexing many streams over few connections behaves much
//     better than opening a connection per stream.
//   - A path that barely answers at all is a filtering problem, not a tuning
//     problem, and the answer is camouflage rather than speed.
//
// QUIC handles loss well too, and is offered in the menu, but it is never the
// recommendation: on the route this tool exists for it has been seen to fail
// its handshake outright on links where KCP ran at full speed. It is named as
// an alternative to try instead, which is the honest place for it until it has
// a track record here.
func RecommendTransport(q PathQuality, current string) Recommendation {
	return recommendWith(q, current, ProbeUDPEgress())
}

// recommendWith is RecommendTransport with the UDP reading supplied, so the
// decision can be tested against a network that allows UDP and one that does
// not without needing either.
func recommendWith(q PathQuality, current string, udp UDPEgress) Recommendation {
	r := Recommendation{Preset: PresetTurbo}

	switch {
	case !q.Usable():
		r.Transport, r.Label = "wss", "WSS"
		r.Why = append(r.Why,
			"the server's tunnel port barely answered, which usually means filtering rather than a slow link",
			"WSS looks like ordinary HTTPS web traffic, so it is the most likely to get through")
		r.Caveats = append(r.Caveats,
			"first make sure the server is actually running and the port is open — a stopped server looks exactly the same from here")

	case q.LossPercent() >= 20:
		r.Transport, r.Label = "kcp", "UDP + KCP + FEC"
		r.Preset = PresetAggressive
		r.Why = append(r.Why,
			fmt.Sprintf("%.0f%% of probes never completed — this link loses a lot of packets", q.LossPercent()),
			"KCP repairs losses with error correction instead of waiting for retransmits, which is exactly this problem")
		r.Caveats = append(r.Caveats,
			udpUnknownCaveat,
			"if KCP does not hold up on this link, QUIC is the other UDP option worth trying: it recovers losses on its own and needs no tuning",
			"and if UDP itself is the problem here, TCP + PCK carries the same KCP inside TCP-shaped packets built below the kernel — Linux and root on both ends")

	case q.LossPercent() >= 2:
		r.Transport, r.Label = "kcp", "UDP + KCP + FEC"
		r.Why = append(r.Why,
			fmt.Sprintf("%.0f%% packet loss measured — enough that TCP keeps backing off and losing speed", q.LossPercent()),
			"KCP's error correction recovers those losses without a full round trip")
		r.Caveats = append(r.Caveats,
			udpUnknownCaveat,
			"if KCP does not hold up on this link, QUIC is the other UDP option worth trying: it recovers losses on its own and needs no tuning",
			"and if UDP itself is the problem here, TCP + PCK carries the same KCP inside TCP-shaped packets built below the kernel — Linux and root on both ends")

	case q.Jitter > q.Avg/3:
		r.Transport, r.Label = "tcpmux", "TCP Mux"
		r.Why = append(r.Why,
			fmt.Sprintf("latency swings a lot (±%s around %s) — a congested or shaped path", shortDur(q.Jitter), shortDur(q.Avg)),
			"multiplexing many streams over a few steady connections rides this out far better than one connection per stream")

	default:
		r.Transport, r.Label = "tcpmux", "TCP Mux"
		r.Why = append(r.Why,
			fmt.Sprintf("the link is clean and steady (%s, ±%s, no measurable loss)", shortDur(q.Avg), shortDur(q.Jitter)),
			"with nothing to repair, plain multiplexed TCP is the fastest and the lightest on CPU")
		r.Caveats = append(r.Caveats,
			tcpOnlyCaveat,
			"and it measures speed, not filtering: if the tunnel works but is throttled after a while, "+
				"that is a filtering problem and the answer is a camouflaged transport rather than a faster one")
	}

	// The UDP reading, which can move the answer as well as explain it — see
	// applyUDPFinding. It runs before the FEC plan below, because a
	// recommendation that has just moved off KCP has no parity ratio to carry.
	applyUDPFinding(&r, udp)

	// When the answer is KCP, the parity ratio is the setting that most decides
	// how it feels, so carry the measured recommendation alongside the transport.
	if r.Transport == "kcp" {
		r.FEC = RecommendFEC(q)
	}

	if r.Transport == current {
		r.Why = append(r.Why, "this is what the tunnel already uses — nothing to change")
	}
	return r
}

// KeepAlivePlan is a keepalive/heartbeat pair derived from a real measurement
// of the link rather than from a fixed default.
type KeepAlivePlan struct {
	KeepAlive int // seconds
	Heartbeat int // seconds
	Why       string
}

// RecommendKeepAlive derives the liveness timers from the measured link.
//
// The fixed defaults (75s / 40s) have to be safe for the worst link anyone
// might have, which makes them slow to notice a dead peer on a good one. Once
// the round trip and its variability are known, the timers can be tightened or
// loosened to match:
//
//   - The heartbeat must comfortably exceed the worst round trip seen, or a
//     slow-but-alive peer gets declared dead and the tunnel restarts for nothing.
//   - Beyond that, shorter is better: it is how quickly a genuinely dropped
//     tunnel is noticed and rebuilt.
func RecommendKeepAlive(q PathQuality) KeepAlivePlan {
	// Without a trustworthy measurement, keep the conservative defaults.
	if !q.Usable() {
		return KeepAlivePlan{KeepAlive: 75, Heartbeat: 40,
			Why: "the link could not be measured, so the safe defaults are kept"}
	}

	// Budget on the worst observed round trip plus the jitter, not the average:
	// the timer has to survive the bad samples, not the typical ones.
	worst := q.Max + q.Jitter
	if lossy := q.LossPercent() >= 2; lossy {
		// On a lossy link a heartbeat can itself be the packet that goes
		// missing, so leave room for a retry before declaring the peer gone.
		worst *= 2
	}

	heartbeat := int(worst.Seconds()) + 5
	switch {
	case heartbeat < 10:
		heartbeat = 10 // never hammer a healthy link
	case heartbeat > 60:
		heartbeat = 60 // past this, a drop takes too long to notice
	}
	keepalive := heartbeat * 2

	return KeepAlivePlan{
		KeepAlive: keepalive,
		Heartbeat: heartbeat,
		Why: fmt.Sprintf("worst round trip seen was %s with ±%s jitter and %.0f%% loss",
			shortDur(q.Max), shortDur(q.Jitter), q.LossPercent()),
	}
}

// SetKeepAlive applies a measured keepalive/heartbeat pair to a tunnel.
func SetKeepAlive(name string, plan KeepAlivePlan) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	if s.KeepAlive == plan.KeepAlive && s.Heartbeat == plan.Heartbeat {
		return fmt.Errorf("the tunnel already uses these timers")
	}
	s.KeepAlive = plan.KeepAlive
	s.Heartbeat = plan.Heartbeat
	// These no longer match the preset's values, so the tunnel is custom.
	s.Preset = ""
	return applySpec(s)
}

// shortDur formats a duration the way a person reads latency.
func shortDur(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000)
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// applyUDPFinding turns the measurement into the difference it should make.
//
// Every recommendation for a lossy link is a UDP carrier, and every one of them
// used to carry the same sentence: test whether UDP works before committing.
// That was the tool telling the operator to find out something the tool had not
// asked. It asks now, and the answer changes the advice rather than decorating
// it:
//
//   - UDP does not leave this machine → a UDP carrier is not a slower choice,
//     it is one that cannot come up. The recommendation moves to the best
//     carrier that does not need UDP, and says why it moved.
//   - UDP works → the caveat that told the operator to go and check is replaced
//     by the reading, because a caveat that has been answered and is still
//     printed is one that teaches people to skip caveats.
//   - The probe could not run → nothing changes. A measurement that was not
//     taken is not a measurement that failed.
func applyUDPFinding(r *Recommendation, udp UDPEgress) {
	if !udp.Checked() {
		return
	}

	// The two sentences this replaces, wherever they were added. Both were the
	// tool telling the operator to go and find out something it had not asked.
	kept := r.Caveats[:0]
	for _, c := range r.Caveats {
		if c != udpUnknownCaveat && c != tcpOnlyCaveat {
			kept = append(kept, c)
		}
	}
	r.Caveats = kept

	if udp.Works() {
		if needsUDP(r.Transport) {
			r.Why = append(r.Why,
				fmt.Sprintf("UDP leaves this machine and comes back (%s answered in %s), so a UDP carrier can work here",
					udp.Via, shortDur(udp.RTT)))
		}
		return
	}

	if !needsUDP(r.Transport) {
		// Nothing to move, but it is still worth saying: it rules out the
		// alternatives the caveats offer.
		r.Caveats = append(r.Caveats,
			"UDP does not appear to leave this machine at all — none of the public resolvers answered — so kcp, quic and udp are not options on this network")
		return
	}

	was := r.Label
	r.Transport, r.Label = "tcpmux", "TCP Mux"
	r.Why = append(r.Why,
		"but UDP does not appear to leave this machine at all — none of the public resolvers answered — so "+was+" would not come up here, however well it would have suited the link",
		"multiplexed TCP is the best of what is left: it rides a congested path far better than one connection per stream")
	r.Caveats = append(r.Caveats,
		"this is the second-best answer for this link. If UDP can be opened on this network, measure again — the loss on this path is exactly what KCP's error correction is for",
		"TCP + PCK is the other route to that: it carries the same KCP inside TCP-shaped packets built below the kernel, so it needs no UDP at all. Linux and root on both ends")
}

// needsUDP reports whether a transport cannot run without UDP leaving the
// machine. pck is deliberately absent: it carries KCP inside packets it builds
// below the kernel, which is the whole reason it exists.
// The two caveats the measurement answers, named so that the place that adds
// them and the place that removes them cannot drift apart.
const (
	udpUnknownCaveat = "KCP runs over UDP — if your provider throttles UDP this will be worse, not better, so test it before committing"
	tcpOnlyCaveat    = "this test is TCP only, so it cannot tell you whether UDP is throttled on your route — " +
		"a clean result here says nothing either way about KCP or QUIC"
)

func needsUDP(transport string) bool {
	switch transport {
	case "kcp", "quic", "udp", "xdi":
		return true
	}
	return false
}

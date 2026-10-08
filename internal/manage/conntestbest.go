package manage

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Best settings: what the Connection Test measured, turned into the numbers
// to build the tunnel with.
//
// The test already carries everything these need: an echo a second through
// every tunnel gives the round trip, its spread and the loss; the bulk transfer
// gives the speed; and the direct tunnels measure the path MTU for themselves
// once they are up. One thing it did not measure is added here — the largest
// packet the path carries unfragmented — by the kharej sending datagrams that
// may not be fragmented to the Iran side's coordinator, each from a new socket
// so a path that cuts flows after a few packets still answers.
//
// The timers and the error-correction ratio come from the same rules Link Test
// uses (RecommendKeepAlive, RecommendFEC), so the two screens never disagree.

// ConnTestBest is the recommendation table's content.
type ConnTestBest struct {
	Transport string  `json:"tr,omitempty"` // "direct pck"
	Mbps      float64 `json:"mb,omitempty"`
	RTTms     int     `json:"rt,omitempty"`
	JitterMs  int     `json:"ji,omitempty"`
	WorstMs   int     `json:"wo,omitempty"`
	LossPct   float64 `json:"lo,omitempty"`
	Preset    string  `json:"pr,omitempty"`
	BDPKB     int     `json:"bd,omitempty"`
	PathMTU   int     `json:"pm,omitempty"` // 0: not measured
	MSS       int     `json:"ms,omitempty"` // 0: automatic
	DirectMTU int     `json:"dm,omitempty"` // measured by the best direct tunnel; 0: none
	DirectBy  string  `json:"db,omitempty"`
	KeepAlive int     `json:"ka,omitempty"`
	Heartbeat int     `json:"hb,omitempty"`
	FECData   int     `json:"fd,omitempty"`
	FECParity int     `json:"fp,omitempty"`
}

// The path-MTU probe's bounds: 576 is what every IPv4 path must carry, 1500 an
// Ethernet frame.
const (
	ctPMTUMin = 576
	ctPMTUMax = 1500
)

// ctProbePMTU is the largest IPv4 packet that crosses from here to the Iran
// side's coordinator without being fragmented, found by halving; 0 if nothing
// came back at all.
func ctProbePMTU(ctx context.Context, host string, port int, tok string) int {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	fits := func(size int) bool {
		for try := 0; try < 2 && ctx.Err() == nil; try++ {
			if ctPMTUProbe(addr, tok, size) {
				return true
			}
		}
		return false
	}
	if !fits(ctPMTUMin) {
		return 0
	}
	if fits(ctPMTUMax) {
		return ctPMTUMax
	}
	lo, hi := ctPMTUMin, ctPMTUMax // lo fits, hi does not
	for hi-lo > 1 && ctx.Err() == nil {
		mid := (lo + hi) / 2
		if fits(mid) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}

// ctPMTUProbe sends one unfragmentable datagram making an IPv4 packet of size
// bytes, from a socket of its own, and reports whether it was answered.
func ctPMTUProbe(addr, tok string, size int) bool {
	raddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return false
	}
	c, err := net.DialUDP("udp4", nil, raddr)
	if err != nil {
		return false
	}
	defer c.Close()
	if ctDontFragment(c) != nil {
		return false
	}
	head := fmt.Sprintf("pmtu %s %d ", tok, size)
	payload := size - 28 // IPv4 and UDP headers
	if payload < len(head) {
		return false
	}
	msg := []byte(head + strings.Repeat("x", payload-len(head)))
	_ = c.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	if _, err := c.Write(msg); err != nil {
		return false // EMSGSIZE: bigger than this machine's own interface
	}
	buf := make([]byte, 64)
	n, err := c.Read(buf)
	return err == nil && strings.TrimSpace(string(buf[:n])) == fmt.Sprintf("pm %d", size)
}

// ctKharejPMTU measures the path MTU and hands it to the Iran side.
func ctKharejPMTU(ctx context.Context, link ConnTestLink) {
	p := ctProbePMTU(ctx, link.Host, link.Coord, link.Tok)
	for i := 0; i < 8 && ctx.Err() == nil; i++ {
		if reply, err := ctAskContext(ctx, link.Host, link.Coord, fmt.Sprintf("pmtu %s %d", link.Tok, p)); err == nil && reply == "ok" {
			return
		}
		ctSleep(ctx, 2*time.Second)
	}
}

// ctDirectMTURe is how a direct tunnel logs the path MTU it measured.
var ctDirectMTURe = regexp.MustCompile(`the path carries (\d+) bytes`)

// ctComputeBest turns the test into the recommendation.
func ctComputeBest(results []ConnTestResult, cases []*connTestCase, pmtu int, logDir string) ConnTestBest {
	var b ConnTestBest
	b.PathMTU = pmtu

	// The tunnel whose numbers to go by: the fastest that carried everything,
	// else the one that carried the most.
	best := -1
	for i, r := range results {
		if r.Kind == "spoof" || r.Status == ctSkipped || r.Tried == 0 || r.OK == 0 {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		cur := results[best]
		switch {
		case r.Status == ctOK && cur.Status != ctOK:
			best = i
		case r.Status == ctUnstable && cur.Status == ctDown:
			best = i
		case r.Status == cur.Status && r.Status == ctOK && r.Mbps > cur.Mbps:
			best = i
		case r.Status == cur.Status && r.Status != ctOK && r.OK > cur.OK:
			best = i
		}
	}
	var q PathQuality
	if best >= 0 {
		r := results[best]
		if r.Status == ctOK {
			b.Transport = r.Kind + " " + r.Transport
			b.Mbps = r.Mbps
		}
		q = ctQuality(r, cases[best].rtts)
		b.RTTms = int(q.Avg.Milliseconds())
		b.JitterMs = int(q.Jitter.Milliseconds())
		b.WorstMs = int(q.Max.Milliseconds())
		b.LossPct = math.Round(q.LossPercent()*10) / 10
	}

	// A buffer that fills the pipe: speed × round trip. Past what Turbo's
	// buffers hold, Aggressive's are worth their memory.
	b.Preset = PresetTurbo
	if b.Mbps > 0 && b.RTTms > 0 {
		bdp := b.Mbps * 1e6 / 8 * float64(b.RTTms) / 1000
		b.BDPKB = int(bdp / 1024)
		if bdp > 4<<20 {
			b.Preset = PresetAggressive
		}
	}

	// A reverse tunnel's TCP segments fit a full-sized path on their own; the
	// clamp is for a path that carries less.
	if pmtu > 0 && pmtu < ctPMTUMax {
		b.MSS = pmtu - 40
	}

	// What the direct tunnels found for themselves, from the best of them.
	for _, i := range ctByPreference(results) {
		if results[i].Kind != "direct" || results[i].Status != ctOK {
			continue
		}
		// Never more than the path itself carries: a carrier that writes its
		// own frames can be let past a local MTU the next router would not.
		if m := ctLastMatch(logDir, cases[i].name); m > 0 && (pmtu == 0 || m < pmtu) {
			b.DirectMTU, b.DirectBy = m, "direct "+results[i].Transport
			break
		}
	}

	ka := RecommendKeepAlive(q)
	b.KeepAlive, b.Heartbeat = ka.KeepAlive, ka.Heartbeat
	if q.Usable() && q.LossPercent() > 0 {
		f := RecommendFEC(q)
		b.FECData, b.FECParity = f.Data, f.Parity
	}
	return b
}

// ctQuality is one tunnel's echoes as a PathQuality.
func ctQuality(r ConnTestResult, rtts []time.Duration) PathQuality {
	q := PathQuality{Sent: r.Tried, Received: r.OK}
	if len(rtts) == 0 {
		return q
	}
	var sum time.Duration
	q.Min = rtts[0]
	for _, d := range rtts {
		sum += d
		q.Min = min(q.Min, d)
		q.Max = max(q.Max, d)
	}
	q.Avg = sum / time.Duration(len(rtts))
	var dev time.Duration
	for _, d := range rtts {
		if d > q.Avg {
			dev += d - q.Avg
		} else {
			dev += q.Avg - d
		}
	}
	q.Jitter = dev / time.Duration(len(rtts))
	return q
}

// ctByPreference is the result indexes, the tunnels that carried everything
// first and fastest first.
func ctByPreference(results []ConnTestResult) []int {
	idx := make([]int, len(results))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ra, rb := results[idx[a]], results[idx[b]]
		if (ra.Status == ctOK) != (rb.Status == ctOK) {
			return ra.Status == ctOK
		}
		return ra.Mbps > rb.Mbps
	})
	return idx
}

// ctLastMatch is the last path MTU a tunnel's log reports; 0 if none.
func ctLastMatch(dir, name string) int {
	b, err := os.ReadFile(dir + "/" + name + ".log")
	if err != nil {
		return 0
	}
	m := ctDirectMTURe.FindAllSubmatch(b, -1)
	if len(m) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(string(m[len(m)-1][1]))
	return n
}

// ConnTestBestTable renders the recommendation as the second table.
func ConnTestBestTable(b ConnTestBest) string {
	var s strings.Builder
	row := func(setting, value, measured string) {
		fmt.Fprintf(&s, "%-15s %-26s %s\n", setting, value, measured)
	}
	s.WriteString("BEST SETTINGS\n" + ctRule() + "\n\n")
	row("SETTING", "VALUE", "MEASURED")
	s.WriteString("\n")

	link := fmt.Sprintf("%dms ±%dms, %.1f%% loss", b.RTTms, b.JitterMs, b.LossPct)
	if b.Transport != "" {
		kind, tr, _ := strings.Cut(b.Transport, " ")
		row("Transport", ctKind(kind)+" "+ctName(tr), fmt.Sprintf("%.1f Mbps, %s", b.Mbps, link))
	} else {
		row("Transport", "-", "nothing carried traffic steadily")
	}
	if b.BDPKB > 0 {
		row("Preset", titleWord(b.Preset), fmt.Sprintf("%d KB in flight (speed × round trip)", b.BDPKB))
	} else {
		row("Preset", titleWord(b.Preset), "the default")
	}
	switch {
	case b.PathMTU == 0:
		row("Path MTU", "-", "not measured")
	case b.PathMTU >= ctPMTUMax:
		row("Path MTU", strconv.Itoa(b.PathMTU), "full-size packets cross")
	default:
		row("Path MTU", strconv.Itoa(b.PathMTU), "largest unfragmented packet")
	}
	if b.MSS > 0 {
		row("TCP MSS Clamp", strconv.Itoa(b.MSS), "path MTU below 1500 (reverse tunnels)")
	} else {
		row("TCP MSS Clamp", "0 (Auto)", "not needed")
	}
	if b.DirectMTU > 0 {
		kind, tr, _ := strings.Cut(b.DirectBy, " ")
		row("Direct MTU", strconv.Itoa(b.DirectMTU), "measured by "+ctKind(kind)+" "+ctName(tr)+" (Auto MTU does this)")
	}
	row("Keepalive", fmt.Sprintf("%ds", b.KeepAlive), fmt.Sprintf("worst round trip %dms ±%dms", b.WorstMs, b.JitterMs))
	row("Heartbeat", fmt.Sprintf("%ds", b.Heartbeat), fmt.Sprintf("%.1f%% loss", b.LossPct))
	if b.FECData > 0 {
		row("FEC", fmt.Sprintf("%d:%d", b.FECData, b.FECParity), fmt.Sprintf("%.1f%% loss", b.LossPct))
	} else {
		row("FEC", "Off", "no loss")
	}
	s.WriteString("\n" + ctRule() + "\n")
	return s.String()
}

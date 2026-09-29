package webui

import (
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// How the panel learns about a tunnel's far end from here: the peers in the
// socket table, and the ping and address lookups behind a card.

func tcpPing(host, port string) int {
	if port == "" {
		port = "80"
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 2*time.Second)
	if err != nil {
		return -1
	}
	conn.Close()
	return int(time.Since(start).Milliseconds())
}

// peerConn is a client (kharej) currently connected to a server tunnel, with
// the kernel-measured RTT of that socket (ms), or -1 if unknown.
type peerConn struct {
	IP  string
	RTT int
}

// peerConn's are read with `ss -tin`: the `-i` flag adds a second, indented
// info line per socket containing `rtt:`, which is the real latency of the
// tunnel connection (no ICMP needed).
//
// The peers of every listening tunnel, from one read of the socket table.
//
// This was one `ss -tin state established` per listening tunnel on every
// tunnel poll — every six seconds, from every open tab — and each of them
// dumped the TCP state of every established socket on the machine, to keep the
// handful on one port. On a server that is what these usually are, a proxy
// carrying tens of thousands of connections, that was the panel's cost:
// measured with 40,000 sockets, five tunnels and one tab open, 45% of a core.
//
// Now the kernel does the filtering — ss hands the port list to it, so only the
// tunnels' own sockets come back — it happens once per poll for all tunnels,
// and the answer is shared for a few seconds, so a second tab costs nothing.
var peerCache struct {
	mu     sync.Mutex
	key    string
	at     time.Time
	byPort map[string][]peerConn
}

// peerCacheTTL is how long one read of the peers is reused. Under the panel's
// own poll interval, so every poll sees a fresh-enough answer.
const peerCacheTTL = 3 * time.Second

// listeningPeers returns, for each port, the remote peers established on it.
func listeningPeers(ports []string) map[string][]peerConn {
	want := map[string]bool{}
	var list []string
	for _, p := range ports {
		if p != "" && !want[p] {
			want[p] = true
			list = append(list, p)
		}
	}
	if len(list) == 0 {
		return nil
	}
	sort.Strings(list)
	key := strings.Join(list, ",")

	peerCache.mu.Lock()
	defer peerCache.mu.Unlock()
	if peerCache.key == key && time.Since(peerCache.at) < peerCacheTTL {
		return peerCache.byPort
	}

	filter := make([]string, 0, len(list))
	for _, p := range list {
		filter = append(filter, "sport = :"+p)
	}
	out, err := exec.Command("ss", "-Htin", "state", "established",
		"( "+strings.Join(filter, " or ")+" )").Output()
	if err != nil {
		return nil
	}
	byPort := parsePeers(string(out), want)
	peerCache.key, peerCache.at, peerCache.byPort = key, time.Now(), byPort
	return byPort
}

// parsePeers reads `ss -Htin` output into the peers on each wanted local port,
// one entry per remote address.
func parsePeers(out string, want map[string]bool) map[string][]peerConn {
	byPort := map[string][]peerConn{}
	seen := map[string]bool{}
	var cur *peerConn
	var curPort string
	flush := func() {
		if cur != nil && !seen[curPort+"|"+cur.IP] {
			seen[curPort+"|"+cur.IP] = true
			byPort[curPort] = append(byPort[curPort], *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			// Info line for the current connection — extract rtt:X/Y.
			if cur != nil {
				cur.RTT = parseRTT(line)
			}
			continue
		}
		// Connection line.
		flush()
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		local, peer := f[len(f)-2], f[len(f)-1]
		_, lp := splitHostPort(local)
		if !want[lp] {
			continue
		}
		ph, _ := splitHostPort(peer)
		if ph == "" || ph == "127.0.0.1" || ph == "::1" {
			continue
		}
		cur, curPort = &peerConn{IP: ph, RTT: -1}, lp
	}
	flush()
	return byPort
}

// parseRTT extracts the smoothed RTT (in ms) from an `ss -i` info line.
func parseRTT(line string) int {
	idx := strings.Index(line, "rtt:")
	if idx < 0 {
		return -1
	}
	var num strings.Builder
	for _, c := range line[idx+4:] {
		if (c >= '0' && c <= '9') || c == '.' {
			num.WriteRune(c)
		} else {
			break // stops at the '/' separating srtt from rttvar
		}
	}
	f, err := strconv.ParseFloat(num.String(), 64)
	if err != nil {
		return -1
	}
	return int(f + 0.5)
}

// icmpPing returns the round-trip time to ip in milliseconds using the system
// ping command, or -1 if unreachable/blocked.
func icmpPing(ip string) int {
	out, err := exec.Command("ping", "-c", "1", "-W", "1", ip).CombinedOutput()
	if err != nil {
		return -1
	}
	s := string(out)
	idx := strings.Index(s, "time=")
	if idx < 0 {
		return -1
	}
	var num strings.Builder
	for _, c := range s[idx+5:] {
		if (c >= '0' && c <= '9') || c == '.' {
			num.WriteRune(c)
		} else {
			break
		}
	}
	f, err := strconv.ParseFloat(num.String(), 64)
	if err != nil {
		return -1
	}
	return int(f + 0.5)
}

func resolveIP(host string) string {
	if net.ParseIP(host) != nil {
		return host
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return ""
	}
	return ips[0].String()
}

// tunnelPortOf reduces a bind address to the part that matters. A server binds
// "0.0.0.0:1231" or "[::]:1231"; the host half is noise on a card, and on a
// client the address is the peer's and belongs in full.
func tunnelPortOf(addr string) string {
	if _, port := splitHostPort(addr); port != "" {
		return port
	}
	return ""
}

func splitHostPort(addr string) (string, string) {
	if h, p, err := net.SplitHostPort(addr); err == nil {
		return h, p
	}
	return addr, ""
}

// --- transfer-rate history ---------------------------------------------------

// rateKeep is how many sparkline points are kept per tunnel. Snapshots are
// written every few seconds, so this covers roughly the last few minutes —
// enough to see a stall or a spike, which is what a sparkline is for.
const rateKeep = 48

// Probing from the panel's poll.
//
// The tunnel list is polled every few seconds, and every card used to probe its
// far end afresh on each poll: a TCP connect that waits up to two seconds when
// the far end is down, or a ping process that waits one. The whole list waits
// for its slowest card, so one unreachable server made the page slow for
// everyone looking at it. A probe's answer now serves the polls after it for a
// few seconds, and is renewed in the background rather than on the poll's time;
// only the very first answer for a target is waited for.

// probeFresh is how long an answer stands before it is renewed.
const probeFresh = 10 * time.Second

type probeEntry struct {
	val  int
	at   time.Time
	busy bool
}

var (
	probeMu sync.Mutex
	probes  = map[string]*probeEntry{}
)

// cachedProbe is fn's last answer for key, renewing it in the background when
// it is older than probeFresh.
func cachedProbe(key string, fn func() int) int {
	probeMu.Lock()
	e, ok := probes[key]
	if !ok {
		probeMu.Unlock()
		v := fn()
		probeMu.Lock()
		probes[key] = &probeEntry{val: v, at: time.Now()}
		probeMu.Unlock()
		return v
	}
	v := e.val
	if !e.busy && time.Since(e.at) > probeFresh {
		e.busy = true
		go func() {
			nv := fn()
			probeMu.Lock()
			e.val, e.at, e.busy = nv, time.Now(), false
			probeMu.Unlock()
		}()
	}
	probeMu.Unlock()
	return v
}

func tcpPingCached(host, port string) int {
	return cachedProbe("tcp\x00"+host+"\x00"+port, func() int { return tcpPing(host, port) })
}

func icmpPingCached(ip string) int {
	return cachedProbe("icmp\x00"+ip, func() int { return icmpPing(ip) })
}

// resolveIPCached is resolveIP for the poll: a name's address is kept for a
// minute and renewed in the background, so a slow resolver costs one wait, not
// one per poll per card.
func resolveIPCached(host string) string {
	if net.ParseIP(host) != nil {
		return host
	}
	resolveMu.Lock()
	e, ok := resolved[host]
	if !ok {
		resolveMu.Unlock()
		ip := resolveIP(host)
		resolveMu.Lock()
		resolved[host] = &resolvedEntry{ip: ip, at: time.Now()}
		resolveMu.Unlock()
		return ip
	}
	ip := e.ip
	if !e.busy && time.Since(e.at) > time.Minute {
		e.busy = true
		go func() {
			next := resolveIP(host)
			resolveMu.Lock()
			if next != "" {
				e.ip = next
			}
			e.at, e.busy = time.Now(), false
			resolveMu.Unlock()
		}()
	}
	resolveMu.Unlock()
	return ip
}

type resolvedEntry struct {
	ip   string
	at   time.Time
	busy bool
}

var (
	resolveMu sync.Mutex
	resolved  = map[string]*resolvedEntry{}
)

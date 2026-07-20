package webui

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/geo"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/sysstat"
	psnet "github.com/shirou/gopsutil/v4/net"
)

// SystemStats is the payload for /api/stats.
type SystemStats struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Uptime   string `json:"uptime"`

	IPv4     string `json:"ipv4"`
	IPv6     string `json:"ipv6"`
	Location string `json:"location"`
	ISP      string `json:"isp"`

	CPUPercent float64 `json:"cpuPercent"`
	CPUCores   int     `json:"cpuCores"`
	Load       string  `json:"load"`

	MemUsed    string  `json:"memUsed"`
	MemTotal   string  `json:"memTotal"`
	MemPercent float64 `json:"memPercent"`

	SwapUsed    string  `json:"swapUsed"`
	SwapTotal   string  `json:"swapTotal"`
	SwapPercent float64 `json:"swapPercent"`

	DiskUsed    string  `json:"diskUsed"`
	DiskTotal   string  `json:"diskTotal"`
	DiskPercent float64 `json:"diskPercent"`

	TotalSent string `json:"totalSent"`
	TotalRecv string `json:"totalRecv"`
	UpSpeed   string `json:"upSpeed"`
	DownSpeed string `json:"downSpeed"`

	TunnelsTotal   int `json:"tunnelsTotal"`
	TunnelsRunning int `json:"tunnelsRunning"`
}

// TunnelInfo is one row for /api/tunnels.
type TunnelInfo struct {
	Name         string `json:"name"`
	Role         string `json:"role"`
	Transport    string `json:"transport"`
	Addr         string `json:"addr"`
	Ports        string `json:"ports"`
	State        string `json:"state"`
	Ping         int    `json:"ping"` // milliseconds, -1 = n/a
	PeerLocation string `json:"peerLocation"`
	PeerISP      string `json:"peerISP"`
	BotRelay     bool   `json:"botRelay"` // has a hidden port used for the Telegram relay
	Country      string `json:"country"`  // user-chosen ISO country code (label)
	// PeerCountry is the ISO code detected from the peer's address, used for
	// the flag. It is separate from Country so a label the user set by hand is
	// never silently overwritten by a lookup.
	PeerCountry string `json:"peerCountry"`
	// TunnelPort is the port clients dial, pulled out of the bind address
	// because ":1231" is what matters and "0.0.0.0:1231" is noise.
	TunnelPort string `json:"tunnelPort"`
}

// splitBotRelay removes the internal SOCKS relay mapping from the visible ports
// and reports whether it was present.
func splitBotRelay(ports []string) (string, bool) {
	suffix := fmt.Sprintf("=127.0.0.1:%d", app.SocksInternalPort)
	var kept []string
	bot := false
	for _, p := range ports {
		if strings.HasSuffix(strings.TrimSpace(p), suffix) {
			bot = true
			continue
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, ", "), bot
}

// --- network speed sampling -------------------------------------------------

type netSample struct {
	sent, recv uint64
	at         time.Time
}

var (
	lastNet   netSample
	netMu     sync.Mutex
	ipv4Cache string
	ipv6Cache string
	ipOnce    sync.Once
)

// GatherSystem collects the current system statistics.
func GatherSystem() SystemStats {
	var s SystemStats

	// Shared with the Telegram bot, so an alert and the dashboard can never
	// disagree about the same instant.
	m := sysstat.Get()

	s.Hostname, s.OS = m.Hostname, m.OS
	s.Uptime = sysstat.HumanDuration(m.Uptime)

	s.CPUPercent, s.CPUCores = m.CPUPercent, m.CPUCores
	s.Load = m.LoadString()

	s.MemUsed, s.MemTotal = sysstat.HumanBytes(m.MemUsed), sysstat.HumanBytes(m.MemTotal)
	s.MemPercent = m.MemPercent

	s.SwapUsed, s.SwapTotal = sysstat.HumanBytes(m.SwapUsed), sysstat.HumanBytes(m.SwapTotal)
	s.SwapPercent = m.SwapPercent

	s.DiskUsed, s.DiskTotal = sysstat.HumanBytes(m.DiskUsed), sysstat.HumanBytes(m.DiskTotal)
	s.DiskPercent = m.DiskPercent

	fillNetwork(&s)

	// Public IPs (cached — they don't change) and geo (cached with TTL).
	ipOnce.Do(func() {
		ipv4Cache = manage.PublicIPv4()
		ipv6Cache = manage.PublicIPv6()
	})
	s.IPv4, s.IPv6 = ipv4Cache, ipv6Cache
	if g := geo.Lookup(ipv4Cache); g != nil {
		s.Location = strings.TrimSpace(g.City + ", " + g.Country)
		s.ISP = g.ISP
	}

	tunnels := manage.List()
	s.TunnelsTotal = len(tunnels)
	for _, t := range tunnels {
		if manage.IsActive(t.Service) {
			s.TunnelsRunning++
		}
	}
	return s
}

// fillNetwork computes cumulative traffic and up/down speed since the last call.
func fillNetwork(s *SystemStats) {
	counters, err := psnet.IOCounters(false)
	if err != nil || len(counters) == 0 {
		return
	}
	cur := netSample{sent: counters[0].BytesSent, recv: counters[0].BytesRecv, at: time.Now()}
	s.TotalSent = sysstat.HumanBytes(cur.sent)
	s.TotalRecv = sysstat.HumanBytes(cur.recv)

	netMu.Lock()
	prev := lastNet
	lastNet = cur
	netMu.Unlock()

	if !prev.at.IsZero() {
		secs := cur.at.Sub(prev.at).Seconds()
		if secs > 0 {
			s.UpSpeed = sysstat.HumanBytes(uint64(float64(cur.sent-prev.sent)/secs)) + "/s"
			s.DownSpeed = sysstat.HumanBytes(uint64(float64(cur.recv-prev.recv)/secs)) + "/s"
		}
	}
	if s.UpSpeed == "" {
		s.UpSpeed, s.DownSpeed = "0 B/s", "0 B/s"
	}
}

// GatherTunnels collects per-tunnel info concurrently, including ping and peer
// geo. State reflects *real* connectivity, not just the local systemd unit:
//
//	stopped  — the systemd service is not active
//	offline  — the service is active but the peer is unreachable (e.g. the other
//	           side was stopped); a client stuck reconnecting shows here
//	online   — active and reachable
func GatherTunnels() []TunnelInfo {
	tunnels := manage.List()
	out := make([]TunnelInfo, len(tunnels))

	// One shared answer for "is it up", from the same code the watchdog and the
	// health check use. Working it out here separately is what made KCP tunnels
	// show as offline: the panel looked for peers in the TCP socket table, and a
	// datagram listener has none.
	health := manage.AllHealth()

	var wg sync.WaitGroup
	for i, t := range tunnels {
		wg.Add(1)
		go func(i int, t manage.Tunnel) {
			defer wg.Done()
			ports, bot := splitBotRelay(t.Ports)
			info := TunnelInfo{
				Name:      t.Name,
				Role:      t.Role,
				Transport: t.Transport,
				Addr:      t.Addr,
				Ports:     ports,
				BotRelay:  bot,
				Country:   manage.TunnelCountry(t.Name),
				Ping:      -1,
			}
			if t.Role == "server" {
				// Server (e.g. the Iran node): we can't ping our own bind_addr,
				// but we can detect the connected client(s) — the kharej peers
				// dialing in — and measure/geo-locate them. This gives the Iran
				// web panel real per-tunnel health + latency to each kharej.
				peers := serverPeers(t.Addr)
				// A datagram listener has no peers in the socket table — the
				// kernel genuinely does not know. The transport does, and writes
				// it to the metrics file, so fall back to that rather than
				// showing a working tunnel with no ping and no location.
				if len(peers) == 0 {
					if ip := peerFromMetrics(t.Name); ip != "" {
						peers = []peerConn{{IP: ip, RTT: -1}}
					}
				}
				if len(peers) > 0 {
					p := peers[0]
					// Prefer the kernel-measured RTT of the live tunnel socket
					// (works even where ICMP is blocked); fall back to ping.
					info.Ping = p.RTT
					if info.Ping < 0 {
						info.Ping = icmpPing(p.IP)
					}
					if g := geo.Lookup(p.IP); g != nil {
						info.PeerLocation = strings.TrimSpace(g.City + ", " + g.Country)
						info.PeerISP = g.ISP
						info.PeerCountry = g.Code
					}
				}
				info.State = health[t.Name].State
			} else {
				// Client (e.g. the kharej node): ping the remote server directly.
				h, port := splitHostPort(t.Addr)
				pingable := h != "" && h != "0.0.0.0" && h != "::" && h != "[::]"
				if pingable {
					info.Ping = tcpPing(h, port)
					if ip := resolveIP(h); ip != "" {
						if g := geo.Lookup(ip); g != nil {
							info.PeerLocation = strings.TrimSpace(g.City + ", " + g.Country)
							info.PeerISP = g.ISP
							info.PeerCountry = g.Code
						}
					}
				}
				info.State = health[t.Name].State
				// A client whose peer does not answer a probe is offline even
				// if the socket table still lists the connection.
				if info.State == "online" && pingable && info.Ping < 0 {
					info.State = "offline"
				}
			}
			out[i] = info
		}(i, t)
	}
	wg.Wait()
	return out
}

// TunnelLogs returns the last N journal lines for a tunnel service.
func TunnelLogs(name string) string {
	service := "backpack-" + name + ".service"
	out, err := exec.Command("journalctl", "-u", service, "-n", "150", "--no-pager", "-o", "short-iso").CombinedOutput()
	if err != nil && len(out) == 0 {
		return "No logs available for " + name
	}
	return string(out)
}

// --- helpers ----------------------------------------------------------------

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

// serverPeers returns the unique remote peers connected to the tunnel's control
// port — i.e. the client (kharej) servers dialed in — using `ss -tin`. The
// `-i` flag adds a second, indented info line per socket containing `rtt:`,
// which is the real latency of the tunnel connection (no ICMP needed).
func serverPeers(bindAddr string) []peerConn {
	_, tport := splitHostPort(bindAddr)
	if tport == "" {
		return nil
	}
	out, err := exec.Command("ss", "-Htin", "state", "established").Output()
	if err != nil {
		return nil
	}

	seen := map[string]struct{}{}
	var peers []peerConn
	var cur *peerConn

	flush := func() {
		if cur != nil {
			if _, dup := seen[cur.IP]; !dup {
				seen[cur.IP] = struct{}{}
				peers = append(peers, *cur)
			}
			cur = nil
		}
	}

	for _, line := range strings.Split(string(out), "\n") {
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
		if _, lp := splitHostPort(local); lp != tport {
			continue
		}
		ph, _ := splitHostPort(peer)
		if ph == "" || ph == "127.0.0.1" || ph == "::1" {
			continue
		}
		cur = &peerConn{IP: ph, RTT: -1}
	}
	flush()
	return peers
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

func splitHostPort(addr string) (string, string) {
	if h, p, err := net.SplitHostPort(addr); err == nil {
		return h, p
	}
	return addr, ""
}

// --- geo lookup with cache --------------------------------------------------

// peerFromMetrics reads the connected peer's IP from a tunnel's metrics file.
//
// Only the datagram transports need this: for everything else the socket table
// is authoritative and fresher. The address is written by the engine when the
// control channel is established and cleared when it drops, so an empty result
// means "not connected" rather than "unknown".
func peerFromMetrics(name string) string {
	snap, err := metrics.Read(app.ConfigDir, name)
	if err != nil || snap.Peer == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(snap.Peer)
	if err != nil {
		return snap.Peer
	}
	return host
}

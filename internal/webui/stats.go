package webui

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
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
	BotRelay     bool   `json:"botRelay"`  // has a hidden port used for the Telegram relay
	Country      string `json:"country"`   // user-chosen ISO country code (label)
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

	if info, err := host.Info(); err == nil {
		s.Hostname = info.Hostname
		s.OS = strings.Title(info.Platform) + " " + info.PlatformVersion
		s.Uptime = humanDuration(time.Duration(info.Uptime) * time.Second)
	}

	if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
		s.CPUPercent = round1(pct[0])
	}
	s.CPUCores, _ = cpu.Counts(true)
	if l, err := load.Avg(); err == nil {
		s.Load = fmt.Sprintf("%.2f, %.2f, %.2f", l.Load1, l.Load5, l.Load15)
	}

	if vm, err := mem.VirtualMemory(); err == nil {
		s.MemUsed = humanBytes(vm.Used)
		s.MemTotal = humanBytes(vm.Total)
		s.MemPercent = round1(vm.UsedPercent)
	}
	if sw, err := mem.SwapMemory(); err == nil {
		s.SwapUsed = humanBytes(sw.Used)
		s.SwapTotal = humanBytes(sw.Total)
		s.SwapPercent = round1(sw.UsedPercent)
	}
	if du, err := disk.Usage("/"); err == nil {
		s.DiskUsed = humanBytes(du.Used)
		s.DiskTotal = humanBytes(du.Total)
		s.DiskPercent = round1(du.UsedPercent)
	}

	fillNetwork(&s)

	// Public IPs (cached — they don't change) and geo (cached with TTL).
	ipOnce.Do(func() {
		ipv4Cache = manage.PublicIPv4()
		ipv6Cache = manage.PublicIPv6()
	})
	s.IPv4, s.IPv6 = ipv4Cache, ipv6Cache
	if g := lookupGeo(ipv4Cache); g != nil {
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
	s.TotalSent = humanBytes(cur.sent)
	s.TotalRecv = humanBytes(cur.recv)

	netMu.Lock()
	prev := lastNet
	lastNet = cur
	netMu.Unlock()

	if !prev.at.IsZero() {
		secs := cur.at.Sub(prev.at).Seconds()
		if secs > 0 {
			s.UpSpeed = humanBytes(uint64(float64(cur.sent-prev.sent)/secs)) + "/s"
			s.DownSpeed = humanBytes(uint64(float64(cur.recv-prev.recv)/secs)) + "/s"
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
			active := manage.IsActive(t.Service)

			if t.Role == "server" {
				// Server (e.g. the Iran node): we can't ping our own bind_addr,
				// but we can detect the connected client(s) — the kharej peers
				// dialing in — and measure/geo-locate them. This gives the Iran
				// web panel real per-tunnel health + latency to each kharej.
				peers := serverPeers(t.Addr)
				if len(peers) > 0 {
					p := peers[0]
					// Prefer the kernel-measured RTT of the live tunnel socket
					// (works even where ICMP is blocked); fall back to ping.
					info.Ping = p.RTT
					if info.Ping < 0 {
						info.Ping = icmpPing(p.IP)
					}
					if g := lookupGeo(p.IP); g != nil {
						info.PeerLocation = strings.TrimSpace(g.City + ", " + g.Country)
						info.PeerISP = g.ISP
					}
				}
				switch {
				case !active:
					info.State = "stopped"
				case len(peers) == 0:
					info.State = "offline" // up, but no client connected
				default:
					info.State = "online"
				}
			} else {
				// Client (e.g. the kharej node): ping the remote server directly.
				h, port := splitHostPort(t.Addr)
				pingable := h != "" && h != "0.0.0.0" && h != "::" && h != "[::]"
				if pingable {
					info.Ping = tcpPing(h, port)
					if ip := resolveIP(h); ip != "" {
						if g := lookupGeo(ip); g != nil {
							info.PeerLocation = strings.TrimSpace(g.City + ", " + g.Country)
							info.PeerISP = g.ISP
						}
					}
				}
				switch {
				case !active:
					info.State = "stopped"
				case pingable && info.Ping < 0:
					info.State = "offline" // active locally but peer unreachable
				default:
					info.State = "online"
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

type geoInfo struct {
	Status  string `json:"status"`
	Country string `json:"country"`
	City    string `json:"city"`
	ISP     string `json:"isp"`
}

type geoEntry struct {
	info *geoInfo
	at   time.Time
}

var (
	geoCache = map[string]geoEntry{}
	geoMu    sync.Mutex
)

// geoProviders is an ordered list of lookup functions. The first that succeeds
// wins. Multiple providers are tried because any single one (e.g. ip-api.com)
// may be blocked from some networks such as Iran.
var geoProviders = []func(string) *geoInfo{geoFromIPApi, geoFromIpwho, geoFromIpSb}

func lookupGeo(ip string) *geoInfo {
	if ip == "" || ip == "-" {
		return nil
	}
	geoMu.Lock()
	if e, ok := geoCache[ip]; ok && time.Since(e.at) < 6*time.Hour {
		geoMu.Unlock()
		return e.info
	}
	geoMu.Unlock()

	for _, provider := range geoProviders {
		if g := provider(ip); g != nil && (g.Country != "" || g.ISP != "") {
			geoMu.Lock()
			geoCache[ip] = geoEntry{info: g, at: time.Now()}
			geoMu.Unlock()
			return g
		}
	}
	return nil
}

func geoGet(url string, out any) bool {
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return json.NewDecoder(resp.Body).Decode(out) == nil
}

// geoFromIPApi — ip-api.com (HTTP, no key).
func geoFromIPApi(ip string) *geoInfo {
	var g geoInfo
	if geoGet("http://ip-api.com/json/"+ip+"?fields=status,country,city,isp", &g) && g.Status == "success" {
		return &g
	}
	return nil
}

// geoFromIpwho — ipwho.is (HTTPS, no key).
func geoFromIpwho(ip string) *geoInfo {
	var r struct {
		Success    bool   `json:"success"`
		Country    string `json:"country"`
		City       string `json:"city"`
		Connection struct {
			ISP string `json:"isp"`
			Org string `json:"org"`
		} `json:"connection"`
	}
	if geoGet("https://ipwho.is/"+ip, &r) && r.Success {
		isp := r.Connection.ISP
		if isp == "" {
			isp = r.Connection.Org
		}
		return &geoInfo{Country: r.Country, City: r.City, ISP: isp}
	}
	return nil
}

// geoFromIpSb — api.ip.sb (HTTPS, no key).
func geoFromIpSb(ip string) *geoInfo {
	var r struct {
		Country      string `json:"country"`
		City         string `json:"city"`
		ISP          string `json:"isp"`
		Organization string `json:"organization"`
	}
	if geoGet("https://api.ip.sb/geoip/"+ip, &r) && r.Country != "" {
		isp := r.ISP
		if isp == "" {
			isp = r.Organization
		}
		return &geoInfo{Country: r.Country, City: r.City, ISP: isp}
	}
	return nil
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func humanDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

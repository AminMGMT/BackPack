package webui

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/config"
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

	// MonitorRunning reports the backpack-monitor service — the watchdog, the
	// Telegram bot and the alerts live there, not in this panel. When it is
	// down, dropped tunnels are not restarted and no alert fires, and nothing
	// else visibly breaks — which is exactly why the panel must say so.
	MonitorRunning bool `json:"monitorRunning"`

	// UpdateTag is the newer release the cached background check knows about,
	// empty when this version is current. Same source as the CLI's notice and
	// the Telegram announcement, so the three can never disagree.
	UpdateTag string `json:"updateTag,omitempty"`
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

	// From the tunnel's metrics snapshot (empty when none has been written yet).
	Uptime   string `json:"uptime,omitempty"`
	BytesIn  string `json:"bytesIn,omitempty"`
	BytesOut string `json:"bytesOut,omitempty"`
	// KCP link-quality counters; nil on every other transport.
	KCP *metrics.KCPStats `json:"kcp,omitempty"`
	// KCPLossPercent is derived from the counters above: how much of the sent
	// traffic needed resending — the honest answer to "is this link lossy?".
	KCPLossPercent float64 `json:"kcpLossPercent,omitempty"`

	// From the tunnel's own config.
	Preset         string   `json:"preset,omitempty"`         // display label: Balance / Turbo / Aggressive / Custom
	MaxConnections int      `json:"maxConnections,omitempty"` // 0 = unlimited
	BandwidthMbps  int      `json:"bandwidthMbps,omitempty"`  // 0 = unlimited
	ProxyProtocol  bool     `json:"proxyProtocol,omitempty"`
	LoadBalance    bool     `json:"loadBalance,omitempty"`
	FallbackAddrs  []string `json:"fallbackAddrs,omitempty"`
	// CertType is "letsencrypt" or "self-signed", only for wss/wssmux servers.
	CertDomain string `json:"certDomain,omitempty"`
	CertType   string `json:"certType,omitempty"`
	// CertExpiry is the NotAfter date of the certificate on disk, when it can
	// be read. ACME certificates renew themselves, so no expiry is shown.
	CertExpiry string `json:"certExpiry,omitempty"`

	// Rates is the recent transfer speed of this tunnel, oldest first, for the
	// dashboard's sparkline. Derived from successive metrics snapshots.
	Rates []RatePoint `json:"rates,omitempty"`
}

// RatePoint is one sparkline sample: bytes per second at a moment in time.
type RatePoint struct {
	T   int64   `json:"t"`   // unix seconds
	In  float64 `json:"in"`  // bytes/s received over the tunnel
	Out float64 `json:"out"` // bytes/s sent over the tunnel
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

	s.MonitorRunning = manage.MonitorRunning()

	// Refresh in the background so the stats endpoint never waits on GitHub —
	// and at most once per interval, not once per poll: this endpoint is hit
	// every few seconds and does not need a goroutine each time.
	kickUpdateCheck()
	if tag, ok := manage.UpdateAvailable(); ok {
		s.UpdateTag = tag
	}
	return s
}

var (
	updKickMu   sync.Mutex
	updKickedAt time.Time
)

// kickUpdateCheck starts a background staleness check, but no more than once
// every 10 minutes across all polls.
func kickUpdateCheck() {
	updKickMu.Lock()
	defer updKickMu.Unlock()
	if time.Since(updKickedAt) < 10*time.Minute {
		return
	}
	updKickedAt = time.Now()
	go manage.RefreshUpdateCheckIfStale(6 * time.Hour)
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
			// One read serves both the peer fallback and the traffic fields.
			snap, snapErr := metrics.Read(app.ConfigDir, t.Name)
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
				if len(peers) == 0 && snapErr == nil {
					if ip := peerHost(snap.Peer); ip != "" {
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
				// Client (e.g. the kharej node): measure and geo-locate the
				// remote server.
				h, port := splitHostPort(t.Addr)
				resolvable := h != "" && h != "0.0.0.0" && h != "::" && h != "[::]"
				datagram := manage.IsDatagram(t.Transport)
				if resolvable {
					ip := resolveIP(h)
					// A TCP probe is only meaningful for the TCP-based transports.
					// KCP and UDP listen on a UDP port, so a TCP connect there
					// always fails — using its result for ping (or worse, for
					// liveness) reports a working datagram tunnel as dead, with no
					// ping. ICMP is the only probe left for those, and it is
					// best-effort: many routes drop it while carrying the tunnel.
					if datagram {
						if ip != "" {
							info.Ping = icmpPing(ip)
						}
					} else {
						info.Ping = tcpPing(h, port)
					}
					if ip != "" {
						if g := geo.Lookup(ip); g != nil {
							info.PeerLocation = strings.TrimSpace(g.City + ", " + g.Country)
							info.PeerISP = g.ISP
							info.PeerCountry = g.Code
						}
					}
				}
				info.State = health[t.Name].State
				// A failed TCP probe is evidence the tunnel is down; a failed
				// ICMP one is not (it may simply be filtered), so a datagram
				// tunnel's liveness rests on the socket check in AllHealth alone,
				// never on ping.
				if info.State == "online" && resolvable && !datagram && info.Ping < 0 {
					info.State = "offline"
				}
			}
			if snapErr == nil {
				fillMetrics(&info, snap)
			}
			// The snapshot's uptime describes the last run; on a stopped tunnel
			// that is history, not state. The traffic totals stay — they are
			// cumulative and survive restarts by design.
			if info.State == "stopped" {
				info.Uptime = ""
			}
			fillConfig(&info, t)
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

// --- transfer-rate history ---------------------------------------------------

// rateKeep is how many sparkline points are kept per tunnel. Snapshots are
// written every few seconds, so this covers roughly the last few minutes —
// enough to see a stall or a spike, which is what a sparkline is for.
const rateKeep = 48

// rateTracker derives bytes-per-second from successive metrics snapshots.
//
// It keys on the snapshot's own Taken time, not on when we happened to poll:
// two browsers polling at once see the same snapshot, and a rate computed
// between identical readings would be a meaningless zero.
type rateTracker struct {
	mu   sync.Mutex
	last map[string]metrics.Snapshot
	hist map[string][]RatePoint
}

var rates = &rateTracker{last: map[string]metrics.Snapshot{}, hist: map[string][]RatePoint{}}

// sample records one snapshot and returns the current history, oldest first.
func (r *rateTracker) sample(name string, snap metrics.Snapshot) []RatePoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev, ok := r.last[name]
	if !ok || !snap.Taken.Equal(prev.Taken) {
		if ok {
			secs := snap.Taken.Sub(prev.Taken).Seconds()
			// A counter that went backwards means the totals file was reset
			// (e.g. a restore); skip the point rather than plotting nonsense.
			if secs > 0 && snap.BytesIn >= prev.BytesIn && snap.BytesOut >= prev.BytesOut {
				h := append(r.hist[name], RatePoint{
					T:   snap.Taken.Unix(),
					In:  float64(snap.BytesIn-prev.BytesIn) / secs,
					Out: float64(snap.BytesOut-prev.BytesOut) / secs,
				})
				if len(h) > rateKeep {
					h = h[len(h)-rateKeep:]
				}
				r.hist[name] = h
			}
		}
		r.last[name] = snap
	}
	return append([]RatePoint(nil), r.hist[name]...)
}

// fillMetrics copies traffic and link-quality numbers from the tunnel's
// metrics snapshot. A tunnel that has never run has no snapshot; that is not
// an error, the fields just stay empty.
func fillMetrics(info *TunnelInfo, snap metrics.Snapshot) {
	info.Uptime = snap.Uptime
	info.BytesIn = sysstat.HumanBytes(snap.BytesIn)
	info.BytesOut = sysstat.HumanBytes(snap.BytesOut)
	info.Rates = rates.sample(info.Name, snap)
	if snap.KCP != nil {
		info.KCP = snap.KCP
		info.KCPLossPercent = snap.KCP.LossPercent()
	}
}

// fillConfig copies the monitoring-relevant parts of the tunnel's own config:
// preset, limits, PROXY protocol, failover addresses and the certificate.
func fillConfig(info *TunnelInfo, t manage.Tunnel) {
	cfg, err := manage.LoadTunnelConfig(t.Name)
	if err != nil {
		return
	}
	if t.Role == "server" {
		sc := cfg.Server
		info.Preset = manage.PresetValueLabel(sc.Preset)
		info.MaxConnections = sc.MaxConnections
		info.BandwidthMbps = sc.BandwidthMbps
		info.ProxyProtocol = sc.ProxyProtocol
		if sc.Transport == config.WSS || sc.Transport == config.WSSMUX {
			if sc.ACMEDomain != "" {
				info.CertType, info.CertDomain = "letsencrypt", sc.ACMEDomain
			} else {
				info.CertType = "self-signed"
				if exp, err := manage.CertExpiry(sc.TLSCertFile); err == nil {
					info.CertExpiry = exp.Format("2006-01-02")
				}
			}
		}
	} else {
		cc := cfg.Client
		info.Preset = manage.PresetValueLabel(cc.Preset)
		info.LoadBalance = cc.LoadBalance
		info.FallbackAddrs = cc.FallbackAddrs
	}
}

// --- geo lookup with cache --------------------------------------------------

// peerHost extracts the host from a snapshot's peer address ("" when there is
// none). Only the datagram transports report a peer this way: for everything
// else the socket table is authoritative and fresher. The address is written by
// the engine when the control channel is established and cleared when it drops,
// so an empty result means "not connected" rather than "unknown".
func peerHost(peer string) string {
	if peer == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(peer)
	if err != nil {
		return peer
	}
	return host
}

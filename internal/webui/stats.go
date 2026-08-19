package webui

import (
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/geo"
	"github.com/backpack/backpack/internal/localproxy"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/sysstat"
	"github.com/backpack/backpack/internal/utils/network"
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

	// Traffic carried by the tunnels — every tunnel's persisted counters added
	// up, so the headline figure is the sum of what the cards show rather than a
	// larger number nothing on the page accounts for. It used to be the machine's
	// NIC counters, which also include ssh, apt and the panel itself, and which
	// reset on reboot while the per-tunnel counters survive one.
	TotalSent    string `json:"totalSent"`
	TotalRecv    string `json:"totalRecv"`
	TotalTraffic string `json:"totalTraffic"`
	// Speed stays on the interface counters: it answers "what is this box doing
	// right now", which is the question a live rate is read for, and a tunnel's
	// own rate is already on its card.
	UpSpeed   string `json:"upSpeed"`
	DownSpeed string `json:"downSpeed"`

	TunnelsTotal   int `json:"tunnelsTotal"`
	TunnelsRunning int `json:"tunnelsRunning"`

	// MonitorRunning reports the backpack-monitor service — the watchdog, the
	// Telegram bot and the alerts live there, not in this panel. When it is
	// down, dropped tunnels are not restarted and no alert fires, and nothing
	// else visibly breaks — which is exactly why the panel must say so.
	MonitorRunning bool `json:"monitorRunning"`

	// Version is what is running here, so the update notice can say what it is
	// asking the operator to move away from rather than only where to.
	Version string `json:"version,omitempty"`

	// UpdateTag is the newer release the cached background check knows about,
	// empty when this version is current. Same source as the CLI's notice and
	// the Telegram announcement, so the three can never disagree.
	UpdateTag string `json:"updateTag,omitempty"`

	// The built-in proxy, when the operator has turned it on. It is off by
	// default, so all of this stays empty and the panel shows nothing.
	//
	// ProxyEnabled and ProxyRunning are deliberately separate. The proxy is a
	// service of its own, and a tunnel can be forwarding a port to it while it
	// is dead: the tunnel is up, the panel is green, and every connection
	// through that port is refused at the far end. Only the two together say
	// whether the thing actually answers.
	ProxyEnabled bool   `json:"proxyEnabled,omitempty"`
	ProxyRunning bool   `json:"proxyRunning,omitempty"`
	ProxyType    string `json:"proxyType,omitempty"`
	ProxyPort    int    `json:"proxyPort,omitempty"`

	// Congestion is the TCP congestion control the tunnel's own sockets run
	// under; CongestionWanted is what they ask for. They differ when the kernel
	// does not have the requested algorithm, and the request is silently
	// dropped by design — the connection still works, just not as fast on a
	// long lossy path, and the presets were tuned expecting it to be there.
	// Empty means the question has no answer here (not Linux), so the panel
	// says nothing rather than guessing.
	Congestion       string `json:"congestion,omitempty"`
	CongestionWanted string `json:"congestionWanted,omitempty"`
}

// TunnelInfo is one row for /api/tunnels.
type TunnelInfo struct {
	Name string `json:"name"`
	Role string `json:"role"`

	// Transport is the raw value the rest of the panel keys off — "tcp",
	// "l3/pck", "direct/wss". Kept exactly as it was, because the edit form and
	// several capability checks compare against it.
	Transport string `json:"transport"`

	// Direction and Carrier are the same thing split for the card, which has
	// two badges rather than one: whether the tunnel is dialled from Iran or
	// from kharej, and what carries it.
	//
	// Split here rather than in the browser because the browser would have to
	// know which prefixes mean what — and would then be a second place that
	// has to learn about every new tunnel kind, and the place nobody remembers
	// to update. "l3/pck" on a card was the symptom: an internal name, leaking
	// out because there was one field where there are two facts.
	Direction    string `json:"direction"`
	Carrier      string `json:"carrier"`
	Addr         string `json:"addr"`
	Ports        string `json:"ports"`
	State        string `json:"state"`
	Ping         int    `json:"ping"` // milliseconds, -1 = n/a
	PeerLocation string `json:"peerLocation"`
	PeerISP      string `json:"peerISP"`
	BotRelay     bool   `json:"botRelay"` // has a hidden port used for the Telegram relay
	// BotRelayPort is the loopback port that relay listens on. It is shown on
	// its own, under its own name, rather than as the raw mapping: the mapping
	// reads like something the operator set up and can therefore tidy away,
	// and removing it stops the bot for a reason that looks unconnected.
	BotRelayPort int    `json:"botRelayPort,omitempty"`
	Country      string `json:"country"` // user-chosen ISO country code (label)
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
	// BytesTotal is the two added. The card shows all three on one line, and a
	// sum of two already-formatted strings is not something the browser can do.
	BytesTotal string `json:"bytesTotal,omitempty"`
	// KCP link-quality counters; nil on every other transport.
	KCP *metrics.KCPStats `json:"kcp,omitempty"`
	// KCPLossPercent is derived from the counters above: how much of the sent
	// traffic needed resending — the honest answer to "is this link lossy?".
	KCPLossPercent float64 `json:"kcpLossPercent,omitempty"`
	// Pool is the client's connection pool; nil on a server tunnel and on the
	// transports that do not keep one.
	Pool *metrics.PoolStats `json:"pool,omitempty"`

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

// splitBotRelay hides the bot's own relay mapping from the forwarded ports.
//
// It defers to manage, which owns the definition. This used to carry its own
// copy that knew only the oldest of the three shapes a relay mapping can have,
// so the current one — the mapping straight to the Telegram API — was listed
// among the operator's ports as something they had configured.
//
// The token is not available this early; manage recognises the two
// token-independent forms without it, and fillConfig runs the same split again
// with the real token to catch the third.
func splitBotRelay(ports []string, token string) (string, int, bool) {
	visible, port, found := manage.SplitBotRelay(ports, token)
	return strings.Join(visible, ", "), port, found
}

// --- network speed sampling -------------------------------------------------

type netSample struct {
	sent, recv uint64
	at         time.Time
}

var (
	lastNet netSample
	netMu   sync.Mutex
)

// --- identity (public addresses + geo) ---------------------------------------

// All four values come from the network: two calls to an address echo, then a
// geo lookup on the result. None of it belongs on the request path — a poll that
// lands while they are being fetched would wait on up to three third-party HTTP
// calls, and /api/stats is polled every few seconds.
//
// So the endpoint reads a cache and a refresher fills it in the background. The
// first poll after a restart shows dashes and the one after that is complete,
// which is the right trade: a blank field for a few seconds costs nothing, a
// stalled dashboard costs the page.
//
// The addresses used to be behind a sync.Once. That made a transient failure
// permanent — the panel starts from systemd at boot, often before the network is
// up, and one failed lookup then left IPv4 (and with it Location and ISP, which
// are derived from it) empty for as long as the process ran. Anything still
// missing is retried; what has been found is refreshed hourly in case the VPS
// is renumbered.
type identityInfo struct {
	ipv4, ipv6, location, isp string
}

var (
	idMu        sync.Mutex
	idCur       identityInfo
	idRefreshed time.Time
	idBusy      bool
)

// identity returns the cached values and kicks a refresh when they are stale or
// incomplete. It never blocks on the network.
func identity() identityInfo {
	idMu.Lock()
	cur, at, busy := idCur, idRefreshed, idBusy
	// Complete answers keep for an hour; an incomplete one is retried every
	// 30 seconds until it fills in, rather than being frozen by the first
	// failure.
	ttl := time.Hour
	if cur.ipv4 == "" || cur.location == "" || cur.isp == "" {
		ttl = 30 * time.Second
	}
	if !busy && time.Since(at) > ttl {
		idBusy = true
		go refreshIdentity()
	}
	idMu.Unlock()
	return cur
}

func refreshIdentity() {
	next := identityInfo{ipv4: manage.PublicIPv4(), ipv6: manage.PublicIPv6()}
	if g := geo.Lookup(next.ipv4); g != nil {
		next.location = strings.Trim(strings.TrimSpace(g.City+", "+g.Country), ", ")
		next.isp = g.ISP
	}

	idMu.Lock()
	defer idMu.Unlock()
	idBusy = false
	idRefreshed = time.Now()
	// Keep what we already knew when a round comes back empty: a lookup that
	// fails once should blank nothing on the page.
	if next.ipv4 != "" {
		idCur.ipv4 = next.ipv4
	}
	if next.ipv6 != "" {
		idCur.ipv6 = next.ipv6
	}
	if next.location != "" {
		idCur.location = next.location
	}
	if next.isp != "" {
		idCur.isp = next.isp
	}
}

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

	tunnels := manage.List()
	s.TunnelsTotal = len(tunnels)
	for _, t := range tunnels {
		if manage.IsActive(t.Service) {
			s.TunnelsRunning++
		}
	}

	fillNetwork(&s, tunnels)

	// Identity — the public addresses and where they are. Read from a cache that
	// a background refresher fills, never inline: the lookups are HTTP calls to
	// third parties, and this endpoint is polled every few seconds.
	id := identity()
	s.IPv4, s.IPv6, s.Location, s.ISP = id.ipv4, id.ipv6, id.location, id.isp

	s.MonitorRunning = manage.MonitorRunning()
	s.Congestion, s.CongestionWanted = network.TunnelCongestion()

	// Only ask systemd about the proxy when it is supposed to be there; an
	// operator who never enabled it should not pay for the check.
	if pc := localproxy.Load(); pc.Enabled {
		s.ProxyEnabled = true
		s.ProxyType = string(pc.Type)
		s.ProxyPort = pc.Port
		s.ProxyRunning = manage.ProxyRunning()
	}

	// Refresh in the background so the stats endpoint never waits on GitHub —
	// and at most once per interval, not once per poll: this endpoint is hit
	// every few seconds and does not need a goroutine each time.
	kickUpdateCheck()
	s.Version = app.Version
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

// fillNetwork sets the traffic totals from the tunnels and the live rate from
// the interface counters.
//
// The two come from different places on purpose. The total answers "how much has
// this tunnel setup carried", so it is the sum of exactly what the cards show —
// counting the box's ssh and apt traffic into a headline figure the cards cannot
// account for is what made the number look wrong. The rate answers "what is
// happening now", where the interface is the honest source.
func fillNetwork(s *SystemStats, tunnels []manage.Tunnel) {
	var in, out uint64
	for _, t := range tunnels {
		// A tunnel with no snapshot yet simply contributes nothing; the file
		// appears once it has carried its first bytes.
		snap, err := metrics.Read(app.ConfigDir, t.Name)
		if err != nil {
			continue
		}
		in += snap.BytesIn
		out += snap.BytesOut
	}
	s.TotalRecv = sysstat.HumanBytes(in)
	s.TotalSent = sysstat.HumanBytes(out)
	s.TotalTraffic = sysstat.HumanBytes(in + out)

	counters, err := psnet.IOCounters(false)
	if err != nil || len(counters) == 0 {
		return
	}
	cur := netSample{sent: counters[0].BytesSent, recv: counters[0].BytesRecv, at: time.Now()}

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
			ports, relayPort, bot := splitBotRelay(t.Ports, "")
			info := TunnelInfo{
				Name:         t.Name,
				Role:         t.Role,
				Transport:    t.Transport,
				Addr:         t.Addr,
				Ports:        ports,
				BotRelay:     bot,
				BotRelayPort: relayPort,
				// The port clients dial. It was declared and documented but
				// never filled in, so every server card showed a dash where
				// its own port should be — the one number on the card you
				// cannot look up anywhere else on the page.
				TunnelPort: tunnelPortOf(t.Addr),
				Country:    manage.TunnelCountry(t.Name),
				Ping:       -1,
				Direction:  manage.TunnelDirection(t),
				Carrier:    manage.TunnelCarrier(t),
			}
			// The question is whether this side can ping the far end from its
			// own config, or has to detect whoever connected to it. A reverse
			// server listens and a reverse client dials; a direct tunnel has
			// the same split, with Iran on the dialling side. See DialsOut.
			if !manage.DialsOut(t) {
				// Listening side (e.g. the Iran node of a reverse tunnel): we
				// can't ping our own bind_addr,
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
func TunnelLogs(name string) string { return manage.Logs(name, 150) }

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
	info.BytesTotal = sysstat.HumanBytes(snap.BytesIn + snap.BytesOut)
	info.Rates = rates.sample(info.Name, snap)
	info.Pool = snap.Pool
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
	// The direct kinds keep their settings in their own tables. Reading
	// [server] or [client] for one of them would not be wrong so much as
	// empty, and would quietly report a preset and limits it does not have.
	if manage.IsDirectKind(t) {
		fillDirectConfig(info, cfg)
		return
	}
	if t.Role == "server" {
		sc := cfg.Server
		// Now that the token is known, split the ports again: one of the relay
		// shapes derives its port from the token and cannot be spotted without
		// it, so the earlier pass had to leave it in the list.
		if ports, relayPort, bot := splitBotRelay(t.Ports, sc.Token); bot {
			info.Ports, info.BotRelay = ports, true
			if relayPort != 0 {
				info.BotRelayPort = relayPort
			}
		}
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

// fillDirectConfig reports what a direct or layer-3 tunnel actually has.
//
// Neither carries a performance preset or a connection limit — those belong to
// the reverse transports' tuning, which these do not share — so the panel
// leaves those fields empty rather than showing a zero that looks like a
// setting. What it does show is the certificate, which is the one thing an
// operator of a wss direct tunnel has to keep an eye on.
func fillDirectConfig(info *TunnelInfo, cfg config.Config) {
	if cfg.L3.Enabled() {
		info.MaxConnections = cfg.L3.MaxConnections
		info.BandwidthMbps = cfg.L3.BandwidthMbps
		return // and no certificate: a layer-3 tunnel has none
	}
	if !cfg.Direct.Enabled() {
		return
	}
	info.MaxConnections = cfg.Direct.MaxConnections
	info.BandwidthMbps = cfg.Direct.BandwidthMbps
	info.Preset = manage.PresetValueLabel(cfg.Direct.Preset)
	if cfg.Direct.Transport != "wss" {
		return
	}
	switch {
	case cfg.Direct.ACMEDomain != "":
		info.CertType, info.CertDomain = "letsencrypt", cfg.Direct.ACMEDomain
	case cfg.Direct.TLSCertFile != "":
		info.CertType = "file"
		if exp, err := manage.CertExpiry(cfg.Direct.TLSCertFile); err == nil {
			info.CertExpiry = exp.Format("2006-01-02")
		}
	default:
		// Generated in memory at start-up, so there is no file to inspect and
		// no expiry that outlives the process.
		info.CertType = "generated"
	}
}

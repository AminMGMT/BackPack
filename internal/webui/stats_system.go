package webui

import (
	"strings"
	"sync"
	"time"

	psnet "github.com/shirou/gopsutil/v4/net"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/geo"
	"github.com/backpack/backpack/internal/localproxy"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/sysstat"
	"github.com/backpack/backpack/internal/utils/network"
)

// The machine's own figures for the dashboard: CPU, memory, disk, network,
// and who this server is.

// SystemStats is the payload for /api/stats.
type SystemStats struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Uptime   string `json:"uptime"`

	IPv4 string `json:"ipv4"`
	IPv6 string `json:"ipv6"`
	// Where IPv4 was decided from — "interface" when the machine holds the
	// address itself, "echo" when it had to be inferred from how this host
	// appears to an outside service. Location and ISP are looked up from this
	// address, so when it is the inferred one the panel says so rather than
	// presenting a guess as a fact.
	IPv4Source string `json:"ipv4Source"`
	Location   string `json:"location"`
	ISP        string `json:"isp"`

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

	// The same five measurements as plain numbers — bytes, and bytes per
	// second.
	//
	// The strings above are formatted for a reader and are what the classic
	// panel prints. Anything that has to compute rather than print needs the
	// number: the new panel scales a column history against a peak, works out
	// each tunnel's share of the total, and animates the headline figure up to
	// its value. Number("873 B/s") is NaN, so every one of those silently
	// became zero — a page reporting no traffic on a link that was carrying
	// it. Sending both is the honest fix; parsing a formatted string back into
	// a number in the browser is guesswork about units that will be wrong the
	// first time the formatter changes.
	UpBps             float64 `json:"upBps"`
	DownBps           float64 `json:"downBps"`
	TotalSentBytes    uint64  `json:"totalSentBytes"`
	TotalRecvBytes    uint64  `json:"totalRecvBytes"`
	TotalTrafficBytes uint64  `json:"totalTrafficBytes"`

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
	ipv4Source                string
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
	ipv4, ipv4Source := manage.PublicIPv4Detail()
	next := identityInfo{ipv4: ipv4, ipv4Source: ipv4Source, ipv6: manage.PublicIPv6()}
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
		// The source travels with the address it describes: keeping one and
		// replacing the other would label this address with where the previous
		// one came from.
		idCur.ipv4, idCur.ipv4Source = next.ipv4, next.ipv4Source
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
	s.IPv4Source = id.ipv4Source

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
//
// "This tunnel setup" includes the tunnels that are gone: the panel calls the
// figure what this server has carried since it was set up, and a delete used
// to take a tunnel's whole history out of it. What deleted tunnels carried is
// kept in the server's ledger (metrics.Retire) and counted here.
func fillNetwork(s *SystemStats, tunnels []manage.Tunnel) {
	in, out := metrics.Retired(app.ConfigDir)
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
	s.TotalRecvBytes, s.TotalSentBytes, s.TotalTrafficBytes = in, out, in+out

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
			s.UpBps = float64(cur.sent-prev.sent) / secs
			s.DownBps = float64(cur.recv-prev.recv) / secs
			s.UpSpeed = sysstat.HumanBytes(uint64(s.UpBps)) + "/s"
			s.DownSpeed = sysstat.HumanBytes(uint64(s.DownBps)) + "/s"
		}
	}
	if s.UpSpeed == "" {
		s.UpSpeed, s.DownSpeed = "0 B/s", "0 B/s"
	}
}

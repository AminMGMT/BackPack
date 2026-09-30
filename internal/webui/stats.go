package webui

import (
	"net"
	"strings"
	"sync"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/geo"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/node"
	"github.com/backpack/backpack/internal/quota"
	"github.com/backpack/backpack/internal/sysstat"
)

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

	// ServiceDown says the tunnel is up and delivering into nothing: the
	// service it forwards to, on the machine at the other end, is refusing
	// every connection.
	//
	// State stays "online", because the tunnel is. This is the sentence beside
	// it that says why nothing works anyway — the reading an operator used to
	// have to go and find in the far machine's journal.
	ServiceDown string `json:"serviceDown,omitempty"`

	// Node is the managed server holding this tunnel's other end, when this
	// panel built both. Empty for a tunnel whose far end was set up by hand,
	// which is a real and ordinary thing to have.
	//
	// The panel needs it to know there is a second side it can act on at all —
	// a log to read there, its service to start and stop — without
	// asking the fleet about every tunnel on every poll.
	Node     string `json:"node,omitempty"`
	PeerName string `json:"peerName,omitempty"`

	// From the tunnel's metrics snapshot (empty when none has been written yet).
	Uptime   string `json:"uptime,omitempty"`
	BytesIn  string `json:"bytesIn,omitempty"`
	BytesOut string `json:"bytesOut,omitempty"`
	// InBytes/OutBytes/TotalBytes are the same three as numbers, for the same
	// reason as the system totals above: the panel sorts tunnels by what they
	// have carried and draws each one's share of the busiest, and neither is
	// possible with "200.0 MiB".
	InBytes    uint64 `json:"inBytes,omitempty"`
	OutBytes   uint64 `json:"outBytes,omitempty"`
	TotalBytes uint64 `json:"totalBytes,omitempty"`
	// QuotaLimit is the traffic the tunnel may carry in all, in bytes (0: no
	// limit); QuotaHit is that it has, and is offline until the limit is
	// raised. QuotaSettable is the Iran end, the one a limit is set on.
	QuotaLimit    uint64 `json:"quotaLimit,omitempty"`
	QuotaHit      bool   `json:"quotaHit,omitempty"`
	QuotaSettable bool   `json:"quotaSettable,omitempty"`
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

// gatherTunnels collects per-tunnel info concurrently, including ping and peer
// geo. State reflects *real* connectivity, not just the local systemd unit:
//
//	stopped  — the systemd service is not active
//	offline  — the service is active but the peer is unreachable (e.g. the other
//	           side was stopped); a client stuck reconnecting shows here
//	online   — active and reachable
//
// It takes the fleet runner so a paired tunnel can be asked about its far end.
// nil means "do not ask", which is what every caller without a fleet wants and
// what the tests use.
func gatherTunnels(run node.Runner) []TunnelInfo {
	tunnels := manage.List()
	out := make([]TunnelInfo, len(tunnels))

	// One shared answer for "is it up", from the same code the watchdog and the
	// health check use. Working it out here separately is what made KCP tunnels
	// show as offline: the panel looked for peers in the TCP socket table, and a
	// datagram listener has none.
	health := manage.AllHealth()

	// The peers of every listening tunnel, read once for all of them. See
	// listeningPeers.
	var listenPorts []string
	for _, t := range tunnels {
		if !manage.DialsOut(t) {
			_, p := splitHostPort(t.Addr)
			listenPorts = append(listenPorts, p)
		}
	}
	peersByPort := listeningPeers(listenPorts)

	// When each service started, asked of systemd once for all of them. See
	// serviceuptime.go.
	units := make([]string, 0, len(tunnels))
	for _, t := range tunnels {
		units = append(units, t.Service)
	}
	since := serviceSince(units)

	var wg sync.WaitGroup
	for i, t := range tunnels {
		wg.Add(1)
		go func(i int, t manage.Tunnel) {
			defer wg.Done()
			out[i] = tunnelInfo(t, health[t.Name], peersByPort, run)
			if at, ok := since[t.Service]; ok && out[i].State != "stopped" {
				out[i].Uptime = upFor(time.Since(at))
			}
		}(i, t)
	}
	wg.Wait()
	return out
}

// tunnelInfo is one tunnel's card: what its config says, what its snapshot
// says, and what can be learned about the far end from here.
func tunnelInfo(t manage.Tunnel, h manage.Health, peersByPort map[string][]peerConn, run node.Runner) TunnelInfo {
	// One read serves both the peer fallback and the traffic fields.
	snap, snapErr := metrics.Read(app.ConfigDir, t.Name)
	ports, relayPort, bot := splitBotRelay(t.Ports, "")
	pair, paired := manage.PairFor(t.Name)
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
	if paired {
		info.Node, info.PeerName = pair.Node, pair.PeerName
	}
	// The question is whether this side can ping the far end from its
	// own config, or has to detect whoever connected to it. A reverse
	// server listens and a reverse client dials; a direct tunnel has
	// the same split, with Iran on the dialling side. See DialsOut.
	if !manage.DialsOut(t) {
		observeInbound(&info, t, h, peersByPort, snap, snapErr)
	} else {
		observeOutbound(&info, t, h, pair, paired, run)
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
	// The traffic limit, on the end that can have one. See internal/quota.
	info.QuotaSettable = manage.HoldsPorts(t)
	if q, err := quota.Load(app.ConfigDir, t.Name); err == nil && q.Limit > 0 {
		info.QuotaLimit = q.Limit
		info.QuotaHit = q.Reached(info.TotalBytes)
	}
	fillConfig(&info, t)
	return info
}

// observeInbound fills in the far end of a tunnel this side listens on: the
// peer that dialled in, found in the socket table or, for a datagram listener,
// in the snapshot.
func observeInbound(info *TunnelInfo, t manage.Tunnel, th manage.Health, peersByPort map[string][]peerConn, snap metrics.Snapshot, snapErr error) {
	// Listening side (e.g. the Iran node of a reverse tunnel): we
	// can't ping our own bind_addr,
	// but we can detect the connected client(s) — the kharej peers
	// dialing in — and measure/geo-locate them. This gives the Iran
	// web panel real per-tunnel health + latency to each kharej.
	_, tport := splitHostPort(t.Addr)
	peers := peersByPort[tport]
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
			info.Ping = icmpPingCached(p.IP)
		}
		locate(info, p.IP)
	}
	info.State = th.State
}

// observeOutbound fills in the far end of a tunnel this side dials: the server
// it dials, probed and located from here, or from the managed server holding
// it when this machine cannot reach the geo providers.
func observeOutbound(info *TunnelInfo, t manage.Tunnel, th manage.Health, pair manage.Pair, paired bool, run node.Runner) {
	// Client (e.g. the kharej node): measure and geo-locate the
	// remote server.
	h, port := splitHostPort(t.Addr)
	resolvable := h != "" && h != "0.0.0.0" && h != "::" && h != "[::]"
	datagram := manage.IsDatagram(t.Transport)
	if resolvable {
		ip := resolveIPCached(h)
		// A TCP probe is only meaningful for the TCP-based transports.
		// KCP and UDP listen on a UDP port, so a TCP connect there
		// always fails — using its result for ping (or worse, for
		// liveness) reports a working datagram tunnel as dead, with no
		// ping. ICMP is the only probe left for those, and it is
		// best-effort: many routes drop it while carrying the tunnel.
		if datagram {
			if ip != "" {
				info.Ping = icmpPingCached(ip)
			}
		} else {
			info.Ping = tcpPingCached(h, port)
		}
		if ip != "" {
			locate(info, ip)
		}
	}
	info.State = th.State
	// A failed TCP probe is evidence the tunnel is down; a failed
	// ICMP one is not (it may simply be filtered), so a datagram
	// tunnel's liveness rests on the socket check in AllHealth alone,
	// never on ping.
	if info.State == "online" && resolvable && !datagram && info.Ping < 0 {
		info.State = "offline"
	}
	// Geo is a lookup against providers this machine may not be
	// able to reach — on an Iran server it usually cannot — and
	// when it fails the card shows a dot where a flag belongs and
	// a dash where a location belongs. A managed server holding
	// the other end is outside that route and answers for itself,
	// so ask it rather than guessing again.
	if paired && (info.PeerCountry == "" || info.PeerLocation == "") {
		if n, ok := node.Find(pair.Node); ok {
			if info.PeerCountry == "" {
				info.PeerCountry = n.Info.Country
			}
			if info.PeerLocation == "" {
				info.PeerLocation = strings.TrimSpace(
					strings.TrimSuffix(n.Info.City+", "+n.Info.Country, ", "))
			}
			if info.PeerISP == "" {
				info.PeerISP = n.Info.ISP
			}
		}
	}
	if d := th.ServiceDown; d != nil && info.State == "online" {
		info.ServiceDown = th.Detail
	}
	// When the forwarded service is on the far machine — the Iran end of a
	// direct tunnel dials out while its users arrive here — only that
	// machine can see it refusing. Ask the server holding it, from cache,
	// never blocking this poll. See farservice.go.
	if info.ServiceDown == "" && info.State == "online" && paired {
		info.ServiceDown = farService.lookup(run, pair.Node, pair.PeerName)
	}
}

// locate fills in where an address is, when the geo lookup knows.
func locate(info *TunnelInfo, ip string) {
	// Never waits on the network: the providers are asked in the background
	// and the card fills in on a later poll. See geo.Peek.
	if g := geo.Peek(ip); g != nil {
		info.PeerLocation = strings.TrimSpace(g.City + ", " + g.Country)
		info.PeerISP = g.ISP
		info.PeerCountry = g.Code
	}
}

// TunnelLogs returns the last N journal lines for a tunnel service.
func TunnelLogs(name string) string { return manage.Logs(name, 150) }

// --- helpers ----------------------------------------------------------------

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

// The rate history is kept whether or not anybody is looking.
//
// It used to be fed only by the tunnel list's poll — so only while a browser
// had the panel open. Close the tab for an hour, sign in again, and every
// card's chart started from nothing: the first point was the average over the
// whole hour it had been away (the gap between the last snapshot it had seen
// and the current one), and the live line had to grow again from there. That
// is the "metric pare va reset mishe" on every sign-in.
//
// The panel is a long-running service, so it samples on its own clock. The
// engines write a snapshot every 30 seconds; sampling every 10 means no
// snapshot is missed, and rateTracker ignores a snapshot it has already seen.
const rateSampleEvery = 10 * time.Second

// sampleRates records every tunnel's latest snapshot into the rate history.
// list and read are parameters so a test can hand it tunnels without a
// config directory.
func sampleRates(list func() []manage.Tunnel, read func(name string) (metrics.Snapshot, error)) {
	for _, t := range list() {
		if snap, err := read(t.Name); err == nil {
			rates.sample(t.Name, snap)
		}
	}
}

// runRateSampler keeps the rate history filling until ctx ends.
func runRateSampler(stop <-chan struct{}) {
	read := func(name string) (metrics.Snapshot, error) { return metrics.Read(app.ConfigDir, name) }
	sampleRates(manage.List, read)
	t := time.NewTicker(rateSampleEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			sampleRates(manage.List, read)
		}
	}
}

// fillMetrics copies traffic and link-quality numbers from the tunnel's
// metrics snapshot. A tunnel that has never run has no snapshot; that is not
// an error, the fields just stay empty.
func fillMetrics(info *TunnelInfo, snap metrics.Snapshot) {
	info.Uptime = snap.Uptime
	info.BytesIn = sysstat.HumanBytes(snap.BytesIn)
	info.BytesOut = sysstat.HumanBytes(snap.BytesOut)
	info.BytesTotal = sysstat.HumanBytes(snap.BytesIn + snap.BytesOut)
	info.InBytes, info.OutBytes = snap.BytesIn, snap.BytesOut
	info.TotalBytes = snap.BytesIn + snap.BytesOut
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
// Each keeps its own preset and limits in its own table, so they are read from
// there; a field the table does not have stays empty rather than showing a
// zero that looks like a setting. What it also shows is the certificate, which is the one thing an
// operator of a wss direct tunnel has to keep an eye on.
func fillDirectConfig(info *TunnelInfo, cfg config.Config) {
	if cfg.L3.Enabled() {
		info.MaxConnections = cfg.L3.MaxConnections
		info.BandwidthMbps = cfg.L3.BandwidthMbps
		// A layer-3 tunnel has its own presets (the queue and the socket
		// memory), and the card names the one it was built with.
		if cfg.L3.Preset != "" {
			info.Preset = manage.PresetValueLabel(cfg.L3.Preset)
		}
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

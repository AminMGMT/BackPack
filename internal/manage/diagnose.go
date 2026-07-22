package manage

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// CheckLevel is how a diagnostic turned out.
type CheckLevel int

const (
	CheckOK CheckLevel = iota
	CheckWarn
	CheckFail
	CheckInfo
)

// Check is one diagnostic line: what was tested, how it went, and — when it
// went badly — what the user should do about it.
type Check struct {
	Group  string // "System", "Web Panel", "Tunnel: name", ...
	Name   string
	Level  CheckLevel
	Detail string // the measured value / what was found
	Fix    string // actionable suggestion, empty when nothing to do
}

// Diagnose runs every health check and returns the results grouped in the order
// they should be displayed. It never modifies anything.
func Diagnose() []Check {
	var out []Check
	out = append(out, systemChecks()...)
	out = append(out, panelChecks()...)
	out = append(out, monitorChecks()...)
	out = append(out, tunnelChecks()...)
	return out
}

// CountByLevel summarises a check list.
func CountByLevel(checks []Check) (ok, warn, fail int) {
	for _, c := range checks {
		switch c.Level {
		case CheckOK:
			ok++
		case CheckWarn:
			warn++
		case CheckFail:
			fail++
		}
	}
	return
}

// --- system -----------------------------------------------------------------

func systemChecks() []Check {
	const g = "System"
	var out []Check

	out = append(out, Check{Group: g, Name: "Backpack version", Level: CheckInfo, Detail: app.Version})

	// Binary present and executable.
	if fi, err := os.Stat(app.BinPath); err == nil {
		lvl, fix := CheckOK, ""
		if fi.Mode().Perm()&0111 == 0 {
			lvl, fix = CheckFail, "chmod +x "+app.BinPath
		}
		out = append(out, Check{Group: g, Name: "Binary", Level: lvl, Detail: app.BinPath, Fix: fix})
	} else {
		out = append(out, Check{Group: g, Name: "Binary", Level: CheckFail,
			Detail: "missing at " + app.BinPath, Fix: "reinstall Backpack"})
	}

	// Running as root — everything here needs it.
	if os.Geteuid() == 0 {
		out = append(out, Check{Group: g, Name: "Root privileges", Level: CheckOK, Detail: "running as root"})
	} else {
		out = append(out, Check{Group: g, Name: "Root privileges", Level: CheckFail,
			Detail: "not root", Fix: "run: sudo backpack"})
	}

	// systemd must be usable, otherwise nothing survives a reboot.
	if _, err := exec.LookPath("systemctl"); err == nil {
		out = append(out, Check{Group: g, Name: "systemd", Level: CheckOK, Detail: "available"})
	} else {
		out = append(out, Check{Group: g, Name: "systemd", Level: CheckFail,
			Detail: "systemctl not found", Fix: "Backpack needs systemd to manage services"})
	}

	if runtime.GOOS != "linux" {
		out = append(out, Check{Group: g, Name: "Platform", Level: CheckWarn,
			Detail: runtime.GOOS, Fix: "Backpack is designed for Linux servers"})
		return out
	}

	// Kernel tuning: BBR + queue discipline + buffer ceilings.
	if v := sysctlValue("net.ipv4.tcp_congestion_control"); v != "" {
		if v == "bbr" {
			out = append(out, Check{Group: g, Name: "Congestion control", Level: CheckOK, Detail: "bbr"})
		} else {
			out = append(out, Check{Group: g, Name: "Congestion control", Level: CheckWarn,
				Detail: v + " (bbr gives better throughput)", Fix: "run Optimize from the main menu"})
		}
	}
	if v := sysctlValue("net.core.default_qdisc"); v != "" && v != "fq" {
		out = append(out, Check{Group: g, Name: "Queue discipline", Level: CheckWarn,
			Detail: v + " (fq pairs with bbr)", Fix: "run Optimize from the main menu"})
	}
	if v := sysctlValue("net.core.rmem_max"); v != "" {
		n, _ := strconv.Atoi(v)
		if n >= 16*1024*1024 {
			out = append(out, Check{Group: g, Name: "Socket buffers", Level: CheckOK, Detail: humanSize(n) + " max"})
		} else {
			out = append(out, Check{Group: g, Name: "Socket buffers", Level: CheckWarn,
				Detail: humanSize(n) + " max — small for high-latency links",
				Fix:    "run Optimize from the main menu"})
		}
	}
	if v := sysctlValue("net.ipv4.ip_forward"); v == "0" {
		out = append(out, Check{Group: g, Name: "IP forwarding", Level: CheckWarn,
			Detail: "disabled", Fix: "run Optimize (needed for some forwarding setups)"})
	}

	// Open-file limit — tunnels with many connections need a high ceiling.
	if v := ulimitNofile(); v > 0 {
		if v >= 65536 {
			out = append(out, Check{Group: g, Name: "Open file limit", Level: CheckOK, Detail: strconv.Itoa(v)})
		} else {
			out = append(out, Check{Group: g, Name: "Open file limit", Level: CheckWarn,
				Detail: strconv.Itoa(v) + " — low for many connections",
				Fix:    "run Optimize, then reboot for it to fully apply"})
		}
	}

	// Time sync matters for TLS validity.
	out = append(out, Check{Group: g, Name: "System time", Level: CheckInfo,
		Detail: time.Now().Format("2006-01-02 15:04:05 MST")})

	return out
}

// --- monitor ----------------------------------------------------------------

// monitorChecks reports on the service that runs the watchdog, the Telegram bot
// and the alerts. This one matters more than it looks: when it is down nothing
// visibly breaks, tunnels simply stop being restarted and alerts stop arriving,
// and the only way to find that out is to be told here.
func monitorChecks() []Check {
	const g = "Monitor"
	var out []Check

	if !fileExists(app.ServiceDir + "/" + app.MonitorService) {
		return append(out, Check{Group: g, Name: "Service", Level: CheckWarn,
			Detail: "not installed — no watchdog and no alerts",
			Fix:    "restart the CLI (sudo backpack); it installs the service on launch"})
	}
	if MonitorRunning() {
		return append(out, Check{Group: g, Name: "Service", Level: CheckOK,
			Detail: "running — watchdog and alerts active"})
	}
	return append(out, Check{Group: g, Name: "Service", Level: CheckFail,
		Detail: "installed but not running — dropped tunnels will NOT be restarted",
		Fix:    "systemctl restart " + app.MonitorService + " (logs: journalctl -u " + app.MonitorService + " -n 30)"})
}

// --- web panel --------------------------------------------------------------

func panelChecks() []Check {
	const g = "Web Panel"
	var out []Check

	unit := app.ServiceDir + "/" + app.WebUIService
	if !fileExists(unit) {
		out = append(out, Check{Group: g, Name: "Service", Level: CheckWarn,
			Detail: "not installed", Fix: "open Web Panel in the menu to start it"})
		return out
	}
	if IsActive(app.WebUIService) {
		out = append(out, Check{Group: g, Name: "Service", Level: CheckOK, Detail: "running"})
	} else {
		out = append(out, Check{Group: g, Name: "Service", Level: CheckFail,
			Detail: "installed but not running",
			Fix:    "Web Panel → Restart panel, or check: journalctl -u " + app.WebUIService + " -n 30"})
	}

	port := panelPort()
	if port > 0 {
		if listening(port) {
			out = append(out, Check{Group: g, Name: "Port", Level: CheckOK,
				Detail: fmt.Sprintf("%d listening", port)})
		} else {
			out = append(out, Check{Group: g, Name: "Port", Level: CheckWarn,
				Detail: fmt.Sprintf("%d not listening", port),
				Fix:    "Web Panel → Restart panel"})
		}
		out = append(out, Check{Group: g, Name: "Firewall", Level: CheckInfo,
			Detail: fmt.Sprintf("allow it if unreachable: ufw allow %d", port)})
	}
	return out
}

// panelPort reads the configured web-panel port without importing webui
// (which would create an import cycle).
func panelPort() int {
	data, err := os.ReadFile(app.WebUIConfig)
	if err != nil {
		return app.WebUIPort
	}
	// Tiny hand-parse to stay dependency-free: look for "port": N.
	s := string(data)
	i := strings.Index(s, `"port"`)
	if i < 0 {
		return app.WebUIPort
	}
	rest := s[i+len(`"port"`):]
	rest = strings.TrimLeft(rest, " \t:\r\n")
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if n, err := strconv.Atoi(rest[:j]); err == nil && n > 0 {
		return n
	}
	return app.WebUIPort
}

// --- tunnels ----------------------------------------------------------------

func tunnelChecks() []Check {
	tunnels := List()
	if len(tunnels) == 0 {
		return []Check{{Group: "Tunnels", Name: "Configured tunnels", Level: CheckWarn,
			Detail: "none", Fix: "create one with Setup Server / Setup Client"}}
	}

	pairs := establishedPairs()
	var out []Check
	for _, t := range tunnels {
		g := "Tunnel: " + t.Name
		h := tunnelHealthWith(t, pairs)

		// Service + real connectivity.
		switch h.State {
		case "online":
			out = append(out, Check{Group: g, Name: "State", Level: CheckOK,
				Detail: fmt.Sprintf("online (%s %s)", t.Role, t.Transport)})
		case "offline":
			fix := "check the other side is running and reachable"
			if t.Role == "client" {
				fix = "verify the server address/port and that the same token is set on both sides"
			}
			out = append(out, Check{Group: g, Name: "State", Level: CheckFail,
				Detail: "service running but peer not connected", Fix: fix})
		default:
			out = append(out, Check{Group: g, Name: "State", Level: CheckWarn,
				Detail: h.Detail, Fix: "start it from Manage → Manage Tunnels"})
		}

		// Config parses and is complete.
		spec, err := LoadSpec(t.Name)
		if err != nil {
			out = append(out, Check{Group: g, Name: "Config", Level: CheckFail,
				Detail: "unreadable: " + err.Error(), Fix: "restore from a backup"})
			continue
		}

		if t.Role == "server" {
			// The control port must actually be bound.
			if p := addrPort(spec.BindAddr); p != "" {
				if n, _ := strconv.Atoi(p); n > 0 {
					// UDP-based transports do not appear in the TCP listen
					// table, so a "not listening" verdict would be wrong.
					if listening(n) || isDatagram(spec.Transport) {
						out = append(out, Check{Group: g, Name: "Tunnel port", Level: CheckOK, Detail: p + " listening"})
					} else {
						out = append(out, Check{Group: g, Name: "Tunnel port", Level: CheckFail,
							Detail: p + " not listening",
							Fix:    "the service may have failed to bind — check its log"})
					}
				}
			}
			// Forwarded ports the users actually connect to.
			vis := VisiblePorts(spec.Ports, spec.Token)
			if len(vis) == 0 {
				out = append(out, Check{Group: g, Name: "Forwarded ports", Level: CheckWarn,
					Detail: "none", Fix: "add ports with Manage → Manage Tunnels → Edit"})
			} else {
				out = append(out, Check{Group: g, Name: "Forwarded ports", Level: CheckOK,
					Detail: strings.Join(vis, ", ")})
			}
			if err := validatePortSpecs(vis); err != nil {
				out = append(out, Check{Group: g, Name: "Port syntax", Level: CheckFail,
					Detail: err.Error(), Fix: "fix them with Manage → Manage Tunnels → Edit"})
			}
		} else {
			// Client: can we actually reach the server's tunnel port over TCP?
			host, port := addrHost(spec.RemoteAddr, ""), addrPort(spec.RemoteAddr)
			out = append(out, Check{Group: g, Name: "Server address", Level: CheckInfo, Detail: spec.RemoteAddr})
			switch {
			case host == "" || port == "":
				// Nothing to probe.
			case isDatagram(spec.Transport):
				// There is no connect step to test on UDP: a silent port and a
				// working one look identical from outside. Say so plainly
				// rather than reporting a failure that may not be real.
				out = append(out, Check{Group: g, Name: "Reachability", Level: CheckInfo,
					Detail: "not testable on a UDP transport — trust the tunnel state above",
					Fix:    "if it will not connect, check that UDP " + port + " is open on the server firewall"})
			case reachable(host, port, 4*time.Second):
				out = append(out, Check{Group: g, Name: "Reachability", Level: CheckOK,
					Detail: "TCP connect to " + spec.RemoteAddr + " works"})
			default:
				out = append(out, Check{Group: g, Name: "Reachability", Level: CheckFail,
					Detail: "cannot open TCP to " + spec.RemoteAddr,
					Fix:    "check the server is up, the port matches, and the firewall allows it — or add a fallback address in Edit"})
			}
		}

		// TLS certificate validity for the transports that terminate TLS.
		if t.Role == "server" && needsTLS(spec.Transport) {
			out = append(out, certCheck(g, spec.TLSCert))
		}

		// Token sanity — a default/short token is a real security problem.
		switch {
		case spec.Token == "":
			out = append(out, Check{Group: g, Name: "Token", Level: CheckFail,
				Detail: "empty", Fix: "recreate the tunnel with a generated token"})
		case len(spec.Token) < 16 || spec.Token == "backpack":
			out = append(out, Check{Group: g, Name: "Token", Level: CheckWarn,
				Detail: "weak or default", Fix: "recreate the tunnel to get a 64-char token"})
		default:
			out = append(out, Check{Group: g, Name: "Token", Level: CheckOK,
				Detail: fmt.Sprintf("%d characters", len(spec.Token))})
		}
	}
	return out
}

// certCheck validates a TLS certificate file and reports its expiry.
func certCheck(group, path string) Check {
	if path == "" {
		return Check{Group: group, Name: "TLS certificate", Level: CheckFail,
			Detail: "not configured", Fix: "switch the transport again to auto-generate one"}
	}
	notAfter, err := CertExpiry(path)
	if err != nil {
		return Check{Group: group, Name: "TLS certificate", Level: CheckFail,
			Detail: err.Error(), Fix: "regenerate or point to a valid certificate"}
	}
	left := time.Until(notAfter)
	switch {
	case left <= 0:
		return Check{Group: group, Name: "TLS certificate", Level: CheckFail,
			Detail: "expired " + notAfter.Format("2006-01-02"),
			Fix:    "regenerate it (switch the transport again) or renew your own"}
	case left < 21*24*time.Hour:
		return Check{Group: group, Name: "TLS certificate", Level: CheckWarn,
			Detail: fmt.Sprintf("expires in %d days", int(left.Hours()/24)),
			Fix:    "renew it soon"}
	default:
		return Check{Group: group, Name: "TLS certificate", Level: CheckOK,
			Detail: "valid until " + notAfter.Format("2006-01-02")}
	}
}

// --- helpers ----------------------------------------------------------------

// sysctlValue reads a kernel parameter, or "" when unavailable.
func sysctlValue(key string) string {
	out, err := exec.Command("sysctl", "-n", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(out), "\t", " "))
}

// ulimitNofile returns the current open-file limit, or 0 if unknown.
func ulimitNofile() int {
	out, err := exec.Command("sh", "-c", "ulimit -n").Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

// listening reports whether anything is bound to a local TCP port.
func listening(port int) bool {
	// If we cannot bind it, something already holds it — that is what we want.
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return true
	}
	ln.Close()
	return false
}

// reachable reports whether a TCP connection to host:port can be opened. This
// deliberately uses TCP rather than ICMP: many networks drop ping entirely
// while the tunnel port itself works fine.
func reachable(host, port string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// --- file locations ---------------------------------------------------------

// Location is one file or directory Backpack owns, and whether it exists.
type Location struct {
	Label  string
	Path   string
	Exists bool
	Note   string
}

// Locations lists every file and folder Backpack uses, so a user can see at a
// glance what is installed and where things live.
func Locations() []Location {
	out := []Location{
		{Label: "Binary", Path: app.BinPath},
		{Label: "Install folder", Path: app.InstallDir},
		{Label: "Backups", Path: app.BackupDir},
		{Label: "Snapshots", Path: snapshotRoot()},
		{Label: "Config folder", Path: app.ConfigDir},
		{Label: "Web panel config", Path: app.WebUIConfig},
		{Label: "Telegram config", Path: app.TelegramConfig},
		{Label: "TLS certificates", Path: app.ConfigDir + "/certs"},
		{Label: "Web panel service", Path: app.ServiceDir + "/" + app.WebUIService},
		{Label: "Monitor service", Path: app.ServiceDir + "/" + app.MonitorService},
	}
	for i := range out {
		out[i].Exists = fileExists(out[i].Path)
	}
	for _, t := range List() {
		out = append(out,
			Location{Label: "Tunnel config (" + t.Name + ")", Path: app.ConfigPath(t.Name),
				Exists: fileExists(app.ConfigPath(t.Name))},
			Location{Label: "Tunnel service (" + t.Name + ")", Path: app.ServiceDir + "/" + t.Service,
				Exists: fileExists(app.ServiceDir + "/" + t.Service)},
		)
	}
	return out
}

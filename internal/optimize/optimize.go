// Package optimize applies kernel/network tuning for high-throughput,
// low-latency tunnels. It is used by the "Optimize" menu item and applied
// automatically behind the Best Performance preset.
package optimize

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// sysctls is the tuning table applied to /etc/sysctl.d and the live kernel.
// Values favour many concurrent connections and high throughput.
var sysctls = [][2]string{
	// Buffer sizes (256MB ceilings, kernel auto-tunes within).
	{"net.core.rmem_max", "268435456"},
	{"net.core.wmem_max", "268435456"},
	{"net.core.rmem_default", "16777216"},
	{"net.core.wmem_default", "16777216"},
	{"net.core.optmem_max", "65536"},
	{"net.ipv4.tcp_rmem", "4096 87380 268435456"},
	{"net.ipv4.tcp_wmem", "4096 65536 268435456"},
	// Connection handling.
	{"net.core.somaxconn", "65536"},
	{"net.core.netdev_max_backlog", "250000"},
	{"net.ipv4.tcp_max_syn_backlog", "20480"},
	// The kernel's own range, set explicitly rather than widened.
	//
	// This used to be "1024 65535", on the reasoning that more ephemeral ports
	// means more concurrent connections. What it actually means is that every
	// service port on the machine is inside the range an outgoing connection
	// can be given — and a port held by an outgoing socket cannot be bound by
	// the service that owns it.
	//
	// That was reported from the field as a panel node that would not start:
	// its ports are 62050 and 62051, which sit safely above the default range
	// and inside the widened one. Once something on the box had been given one
	// of them as a source port, the node could not bind it, and `ss -tlnp` —
	// the command anyone would run — showed nothing at all, because the holder
	// was an outgoing connection rather than a listener.
	//
	// 28,000 ephemeral ports with tcp_tw_reuse on is far more than a tunnel
	// needs, and the 4,500 extra the wider range bought are not worth the
	// 61000-65535 band, which is where services like that one live.
	{"net.ipv4.ip_local_port_range", "32768 60999"},
	{"net.ipv4.tcp_tw_reuse", "1"},
	{"net.ipv4.tcp_fin_timeout", "15"},
	{"net.ipv4.tcp_max_tw_buckets", "1440000"},
	// Latency / throughput features.
	{"net.ipv4.tcp_window_scaling", "1"},
	{"net.ipv4.tcp_fastopen", "3"},
	{"net.ipv4.tcp_mtu_probing", "1"},
	{"net.ipv4.tcp_slow_start_after_idle", "0"},
	{"net.ipv4.tcp_notsent_lowat", "131072"},
	// Congestion control — BBR + fq for best tunnel performance.
	{"net.core.default_qdisc", "fq"},
	{"net.ipv4.tcp_congestion_control", "bbr"},
	// Forwarding (reverse tunnels frequently forward traffic).
	{"net.ipv4.ip_forward", "1"},
}

const sysctlFile = "/etc/sysctl.d/99-backpack.conf"

const limitsFile = "/etc/security/limits.d/99-backpack.conf"

const limitsContent = `# Raised by backpack for high connection counts
* soft nofile 1048576
* hard nofile 1048576
root soft nofile 1048576
root hard nofile 1048576
* soft nproc  1048576
* hard nproc  1048576
`

// Apply performs the full optimization with progress output. printf is used so
// the caller can pass a logging function (e.g. tui printer).
//
// reserve is the set of ports this machine listens on and must keep: they are
// put in ip_local_reserved_ports so the kernel never hands one out as the
// source port of an outgoing connection. A port taken that way cannot be bound
// by the service that owns it, and nothing in the usual listener view shows
// why — see the note over ip_local_port_range above. Callers pass the tunnels'
// own ports; nil reserves nothing, which is the old behaviour.
func Apply(logf func(string), reserve []int) {
	if runtime.GOOS != "linux" {
		logf("Optimizations are only supported on Linux — skipping.")
		return
	}

	loadBBRModule(logf)

	// The static table plus whatever this machine has to keep. Built here
	// rather than in the table because it depends on the tunnels configured.
	rows := append([][2]string(nil), sysctls...)
	if list := reservedList(reserve); list != "" {
		rows = append(rows, [2]string{"net.ipv4.ip_local_reserved_ports", list})
		logf("Reserving ports this server listens on: " + list)
	}

	// Persist sysctl settings.
	var b strings.Builder
	b.WriteString("# Managed by backpack — network optimizations\n")
	for _, kv := range rows {
		fmt.Fprintf(&b, "%s = %s\n", kv[0], kv[1])
	}
	if err := os.WriteFile(sysctlFile, []byte(b.String()), 0644); err != nil {
		logf("Could not write " + sysctlFile + ": " + err.Error())
	} else {
		logf("Wrote persistent settings to " + sysctlFile)
	}

	// Apply live (best effort per key so one failure doesn't abort the rest).
	applied := 0
	for _, kv := range rows {
		if err := exec.Command("sysctl", "-w", kv[0]+"="+kv[1]).Run(); err == nil {
			applied++
		}
	}
	logf(fmt.Sprintf("Applied %d/%d kernel parameters live.", applied, len(rows)))

	// Persist file limits.
	if err := os.WriteFile(limitsFile, []byte(limitsContent), 0644); err != nil {
		logf("Could not write " + limitsFile + ": " + err.Error())
	} else {
		logf("Raised open-file / process limits in " + limitsFile)
	}

	verifyBBR(logf)
	logf("Optimization complete.")
}

// ApplyQuiet runs Apply discarding output — used by the Best Performance flow.
func ApplyQuiet(reserve []int) {
	Apply(func(string) {}, reserve)
}

// reservedList formats ports for ip_local_reserved_ports: sorted, without
// duplicates, and with consecutive runs collapsed into ranges, which is how the
// kernel writes it back and keeps the file readable when a tunnel forwards a
// wide range.
//
// Ports outside the ephemeral range cost nothing to list — the kernel simply
// never had them to give away — so nothing here filters by range. Doing that
// would mean this had to know what the range is, and be re-run whenever it
// changed.
func reservedList(ports []int) string {
	seen := make(map[int]bool, len(ports))
	var uniq []int
	for _, p := range ports {
		if p < 1 || p > 65535 || seen[p] {
			continue
		}
		seen[p] = true
		uniq = append(uniq, p)
	}
	if len(uniq) == 0 {
		return ""
	}
	sort.Ints(uniq)

	var parts []string
	start, prev := uniq[0], uniq[0]
	flush := func() {
		if start == prev {
			parts = append(parts, strconv.Itoa(start))
		} else {
			parts = append(parts, strconv.Itoa(start)+"-"+strconv.Itoa(prev))
		}
	}
	for _, p := range uniq[1:] {
		if p == prev+1 {
			prev = p
			continue
		}
		flush()
		start, prev = p, p
	}
	flush()
	return strings.Join(parts, ",")
}

// loadBBRModule attempts to load the tcp_bbr kernel module.
func loadBBRModule(logf func(string)) {
	if err := exec.Command("modprobe", "tcp_bbr").Run(); err != nil {
		logf("Note: could not load tcp_bbr module (may be built-in).")
	}
}

// verifyBBR checks whether BBR is the active congestion control algorithm.
func verifyBBR(logf func(string)) {
	out, err := exec.Command("sysctl", "-n", "net.ipv4.tcp_congestion_control").Output()
	if err != nil {
		return
	}
	if strings.TrimSpace(string(out)) == "bbr" {
		logf("BBR congestion control is active.")
	} else {
		logf("BBR not active — kernel may not support it (needs Linux 4.9+).")
	}
}

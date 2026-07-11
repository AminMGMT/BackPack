// Package optimize applies kernel/network tuning for high-throughput,
// low-latency tunnels. It is used by the "Optimize" menu item and applied
// automatically behind the Best Performance preset.
package optimize

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
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
	{"net.ipv4.ip_local_port_range", "1024 65535"},
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
func Apply(logf func(string)) {
	if runtime.GOOS != "linux" {
		logf("Optimizations are only supported on Linux — skipping.")
		return
	}

	loadBBRModule(logf)

	// Persist sysctl settings.
	var b strings.Builder
	b.WriteString("# Managed by backpack — network optimizations\n")
	for _, kv := range sysctls {
		fmt.Fprintf(&b, "%s = %s\n", kv[0], kv[1])
	}
	if err := os.WriteFile(sysctlFile, []byte(b.String()), 0644); err != nil {
		logf("Could not write " + sysctlFile + ": " + err.Error())
	} else {
		logf("Wrote persistent settings to " + sysctlFile)
	}

	// Apply live (best effort per key so one failure doesn't abort the rest).
	applied := 0
	for _, kv := range sysctls {
		if err := exec.Command("sysctl", "-w", kv[0]+"="+kv[1]).Run(); err == nil {
			applied++
		}
	}
	logf(fmt.Sprintf("Applied %d/%d kernel parameters live.", applied, len(sysctls)))

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
func ApplyQuiet() {
	Apply(func(string) {})
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

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

// engineStartupKeys are the settings a tunnel applies for itself when it starts.
//
// The engine used to carry its own copy of these values, and the two tables
// drifted: Optimize set net.core.rmem_default to 16 MB and the engine set it
// back to 1 MB on the next tunnel start, the same for wmem_default, and
// tcp_notsent_lowat went 128 KB to 32 KB the same way. Every one of those was a
// setting an operator had deliberately applied, undone by a restart, with
// nothing anywhere saying so — the shape of ip_local_port_range, without the
// consequence that made that one visible.
//
// So there is one table now and this is a view of it. The keys here are the
// ones that are safe to apply without being asked: socket buffers, queue
// lengths, and how TCP treats its own connections. What is deliberately absent
// is machine policy — the ephemeral port range, the congestion control
// algorithm, the queue discipline, IP forwarding. Those change how everything
// else on the box behaves, and installing a tunnel is not consent to have them
// changed; they belong to Optimize, which the operator runs on purpose.
var engineStartupKeys = []string{
	"net.core.rmem_max",
	"net.core.wmem_max",
	"net.core.rmem_default",
	"net.core.wmem_default",
	"net.core.somaxconn",
	"net.ipv4.tcp_max_syn_backlog",
	"net.ipv4.tcp_tw_reuse",
	"net.ipv4.tcp_fin_timeout",
	"net.ipv4.tcp_window_scaling",
	"net.ipv4.tcp_fastopen",
	"net.ipv4.tcp_notsent_lowat",
}

// FullTuning returns everything Optimize applies, so a caller can be checked
// against it rather than against a second list written by hand.
func FullTuning() [][2]string {
	return append([][2]string(nil), sysctls...)
}

// EngineStartupTuning returns the settings a starting tunnel applies, taken
// from the same table Optimize writes so the two can never disagree again.
func EngineStartupTuning() [][2]string {
	want := make(map[string]bool, len(engineStartupKeys))
	for _, k := range engineStartupKeys {
		want[k] = true
	}
	out := make([][2]string, 0, len(engineStartupKeys))
	for _, kv := range sysctls {
		if want[kv[0]] {
			out = append(out, kv)
		}
	}
	return out
}

// sysctlFile is where the tuning is persisted, and the one durable trace that
// Optimize has run here. A var rather than a const so a test can point it at a
// temp directory instead of writing to /etc — the same way node.StorePath and
// manage.NodePairPath are overridden.
//
// It is named to come last. The kernel's settings are applied at boot file by
// file in name order, and the last to set a key wins. It was 99-backpack.conf,
// which sorts before 99-sysctl.conf — the link to /etc/sysctl.conf that Debian
// and Ubuntu install, and the file every other installer and "VPS optimizer"
// script writes to. So on the next boot anything those had put there quietly
// replaced what Optimize set, and Health Check went on saying "run Optimize" to
// somebody who had, as many times as they liked. zz- sorts after every
// numbered file.
var sysctlFile = "/etc/sysctl.d/zz-backpack.conf"

// legacySysctlFile is where an older Optimize wrote the same thing. Apply
// removes it, so the two cannot disagree; WasApplied still counts it.
var legacySysctlFile = "/etc/sysctl.d/99-backpack.conf"

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
		if legacySysctlFile != sysctlFile {
			_ = os.Remove(legacySysctlFile)
		}
	}

	// Apply live (best effort per key so one failure doesn't abort the rest).
	//
	// A key the kernel refuses is named, with the kernel's reason. It used to
	// be counted and nothing more — "Applied 21/24" — which left the operator
	// to find out from Health Check which three, and never why. The usual why
	// is a container VPS (OpenVZ, LXC) whose host does not let a guest change
	// the socket buffers or the congestion control; no retry from here fixes
	// that, and saying so saves running Optimize again.
	applied := 0
	for _, kv := range rows {
		out, err := exec.Command("sysctl", "-w", kv[0]+"="+kv[1]).CombinedOutput()
		if err == nil {
			applied++
			continue
		}
		why := strings.TrimSpace(string(out))
		if why == "" {
			why = err.Error()
		}
		logf(fmt.Sprintf("  not applied: %s = %s — %s", kv[0], kv[1], why))
	}
	logf(fmt.Sprintf("Applied %d/%d kernel parameters live.", applied, len(rows)))
	if applied < len(rows) {
		logf("The ones not applied are refused by this server's kernel. On a container VPS " +
			"(OpenVZ, LXC) the host controls them, and only the provider can change them.")
	}

	// Persist file limits.
	if err := os.WriteFile(limitsFile, []byte(limitsContent), 0644); err != nil {
		logf("Could not write " + limitsFile + ": " + err.Error())
	} else {
		logf("Raised open-file / process limits in " + limitsFile)
	}

	verifyBBR(logf)
	logf("Optimization complete.")
}

// WasApplied reports whether Optimize has ever run on this machine, by the one
// durable trace it leaves: the sysctl file it owns.
//
// It exists so an update can repair what an older Optimize wrote without
// applying tuning to a machine that never asked for it. Installing a new
// version is not consent to have the kernel retuned; rewriting a file this
// program put there, to values this version believes in, is a different thing
// and is the only way a server set up before a fix ever receives it — nothing
// else rewrites that file, so the old values would otherwise outlive every
// update.
func WasApplied() bool {
	for _, f := range []string{sysctlFile, legacySysctlFile} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

// Wanted returns the value Optimize sets for key, and whether it sets one.
func Wanted(key string) (string, bool) {
	for _, kv := range sysctls {
		if kv[0] == key {
			return kv[1], true
		}
	}
	return "", false
}

// sysctlDirs are the directories the boot-time sysctl service reads, in the
// order systemd documents; a file name in an earlier one shadows the same
// name in a later one, and all files are then applied in name order.
// procpsConf is /etc/sysctl.conf; a var so a test can move it.
var procpsConf = "/etc/sysctl.conf"

var sysctlDirs = []string{"/etc/sysctl.d", "/run/sysctl.d", "/usr/local/lib/sysctl.d", "/usr/lib/sysctl.d", "/lib/sysctl.d"}

// WhyNot explains why key is not at the value Optimize sets, once Optimize
// has run: which file sets it to something else after ours — so a reboot
// puts that value back — or, when none does, that the kernel refused it or
// something changed it since. Empty when there is nothing to explain.
func WhyNot(key, live string) string {
	want, ok := Wanted(key)
	if !ok || !WasApplied() || sameValue(live, want) {
		return ""
	}
	if f, v := lastSetting(key); f != "" && f != sysctlFile && !sameValue(v, want) {
		return fmt.Sprintf("Optimize set it to %q, but %s sets %q and is applied after it — "+
			"remove or change the line there (it was probably put there by another installer)",
			want, f, v)
	}
	return fmt.Sprintf("Optimize set it to %q, but the kernel is at %q — either this server's "+
		"kernel does not allow it (a container VPS such as OpenVZ or LXC, where only the provider "+
		"can change it) or something changed it after Optimize ran", want, live)
}

// lastSetting returns the file that sets key last at boot, and its value.
func lastSetting(key string) (file, value string) {
	byName := map[string]string{}
	for _, d := range sysctlDirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".conf") {
				continue
			}
			if _, seen := byName[e.Name()]; !seen {
				byName[e.Name()] = d + "/" + e.Name()
			}
		}
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	files := make([]string, 0, len(names)+1)
	for _, n := range names {
		files = append(files, byName[n])
	}
	// Read last by procps' `sysctl --system`, after every directory.
	files = append(files, procpsConf)

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || line[0] == '#' || line[0] == ';' {
				continue
			}
			k, v, found := strings.Cut(strings.TrimPrefix(line, "-"), "=")
			if !found {
				continue
			}
			if strings.ReplaceAll(strings.TrimSpace(k), "/", ".") == key {
				file, value = f, strings.TrimSpace(v)
			}
		}
	}
	return file, value
}

// sameValue compares two sysctl values the way the kernel prints them: runs
// of whitespace (the tabs in a port range) are one space.
func sameValue(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
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

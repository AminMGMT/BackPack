package network

import (
	"os"
	"slices"
	"strings"
)

// Reporting which congestion control the tunnel actually got.
//
// Every TCP socket the tunnel opens asks the kernel for TCPCongestion — BBR by
// default, because on a long path with mild loss the loss-based default reads
// loss as congestion and backs off, and BBR does not. The request is made per
// socket and deliberately ignores its error: a kernel without the algorithm
// returns one, and the connection works perfectly well on whatever it falls
// back to, so failing the connection over it would be worse than useless.
//
// The cost of that choice is silence. setCongestion lists four ways the request
// can quietly do nothing — a kernel built without the bbr module, a container
// that forbids it, an operator who never ran Optimize, a reboot that reset the
// sysctl — and in every one of them the tunnel keeps running, a little slower,
// with nothing said. The performance presets were tuned assuming BBR, so an
// operator can be looking at "Turbo" while the sockets underneath run cubic.
//
// That is exactly the kind of thing the panel exists to say out loud, so the
// answer is read back here rather than assumed.

// TunnelCongestion reports the congestion control algorithm the tunnel's TCP
// sockets end up running under, and the one they asked for.
//
// An empty active value means the question could not be answered — the tunnel
// is not on Linux, or /proc is not readable — which is reported as unknown
// rather than guessed at.
func TunnelCongestion() (active, requested string) {
	requested = TCPCongestion

	available := procSysctl("net/ipv4/tcp_available_congestion_control")
	if available == "" {
		return "", requested
	}
	// The per-socket request succeeds exactly when the kernel offers the
	// algorithm, so this is the same answer setCongestion would get.
	if slices.Contains(strings.Fields(available), requested) {
		return requested, requested
	}
	// It does not, so the sockets keep the system default.
	return procSysctl("net/ipv4/tcp_congestion_control"), requested
}

// procSysctl reads a kernel parameter straight from /proc, returning "" when it
// cannot be read. Reading the file rather than shelling out to sysctl keeps
// this cheap enough to call on every stats poll.
func procSysctl(key string) string {
	b, err := os.ReadFile("/proc/sys/" + key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

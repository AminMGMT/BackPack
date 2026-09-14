package manage

import (
	"strconv"
	"strings"
)

// The ports this machine listens on, for the kernel to keep out of the
// ephemeral range.
//
// A port the kernel may hand out as the source port of an outgoing connection
// cannot be bound by the service that owns it. That is not hypothetical: it was
// reported as a panel node that would not start on 62050, with `ss -tlnp`
// showing nothing, because the holder was an outgoing connection rather than a
// listener. Optimize no longer widens the ephemeral range over the band those
// ports sit in, and what remains inside the range is reserved by name.
//
// This is the caller's side of optimize.Apply: that package cannot read the
// tunnel list itself, because manage imports it and the dependency cannot go
// both ways.

// ReservedPorts returns every port the configured tunnels listen on locally.
//
// Over-reporting is harmless — reserving a port the kernel was never going to
// hand out costs nothing — so this errs towards including a port rather than
// reasoning about which side of a tunnel binds it.
func ReservedPorts() []int {
	var out []int
	for _, t := range List() {
		if p := portNumber(addrPort(t.Addr)); p > 0 {
			out = append(out, p)
		}
		for _, spec := range t.Ports {
			out = append(out, listenPortsOf(spec)...)
		}
	}
	return out
}

// listenPortsOf pulls the ports a forwarded-port entry listens on.
//
// The forms are the ones validPortSpec accepts: "443", "443-450",
// "443=127.0.0.1:2096", "10000-10009=20000-20009" and "1.2.3.4:443=…". Only the
// left of the "=" is local; the right names a service on the far side of the
// tunnel and is nothing this machine binds.
//
// It mirrors expandListenSpec in the transport package rather than sharing it:
// that one is unexported and lives behind the engine, and importing the
// transports here to reach it would tie the management layer to them for the
// sake of a dozen lines. The two are small, and the forms they read are fixed
// by validPortSpec above them.
func listenPortsOf(spec string) []int {
	local := strings.TrimSpace(spec)
	if i := strings.Index(local, "="); i >= 0 {
		local = strings.TrimSpace(local[:i])
	}
	if local == "" {
		return nil
	}

	// An address in front of the port pins the listener; the port is what is
	// reserved either way. IPv6 literals are bracketed, so the separator is the
	// colon after the closing bracket.
	if i := strings.LastIndex(local, "]"); i >= 0 {
		if j := strings.Index(local[i:], ":"); j >= 0 {
			local = local[i+j+1:]
		}
	} else if i := strings.LastIndex(local, ":"); i >= 0 {
		local = local[i+1:]
	}

	lo, hi, isRange := strings.Cut(local, "-")
	first := portNumber(lo)
	if first == 0 {
		return nil
	}
	if !isRange {
		return []int{first}
	}
	last := portNumber(hi)
	if last < first {
		return []int{first}
	}
	// A wide range is still only a list of integers here; optimize collapses
	// consecutive ones back into a range when it writes the sysctl.
	out := make([]int, 0, last-first+1)
	for p := first; p <= last; p++ {
		out = append(out, p)
	}
	return out
}

// The kernel's own ephemeral range, which is what a machine should have unless
// somebody had a reason to change it.
const (
	ephemeralDefaultLow  = 32768
	ephemeralDefaultHigh = 60999
)

// ephemeralRangeIsWide reads an ip_local_port_range value and reports whether it
// reaches past the kernel's default in either direction.
//
// Both ends matter and for the same reason. A lower floor swallows the service
// ports between 1024 and 32768; a higher ceiling swallows 61000-65535, which is
// where a panel node's 62050 and 62051 live — the ports this check exists for.
// A range narrower than the default is somebody's deliberate choice and is left
// alone.
func ephemeralRangeIsWide(v string) (lo, hi int, wide bool) {
	f := strings.Fields(strings.TrimSpace(v))
	if len(f) != 2 {
		return 0, 0, false
	}
	lo, hi = portNumber(f[0]), portNumber(f[1])
	if lo == 0 || hi == 0 || hi < lo {
		return 0, 0, false
	}
	return lo, hi, lo < ephemeralDefaultLow || hi > ephemeralDefaultHigh
}

// portNumber parses a port, returning 0 for anything that is not one.
func portNumber(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}

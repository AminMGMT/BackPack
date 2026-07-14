package manage

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// validPort reports whether s is a valid TCP/UDP port number.
func validPort(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1 && n <= 65535
}

// nameRe restricts tunnel names to characters that are safe in file paths,
// systemd unit names and the web UI (no spaces, quotes or slashes).
var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)

// validName reports whether a tunnel name is acceptable.
func validName(name string) bool {
	return nameRe.MatchString(name)
}

// parsePorts splits a comma-separated port specification into individual
// entries, trimming whitespace and dropping empties. Mapping forms such as
// "443=1.1.1.1:443", ranges "443-450", and plain "443" are all passed through
// to the engine's port parser unchanged.
func parsePorts(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

// validPortSpec reports whether one forwarded-port entry is in a shape the
// engine's port parser accepts: "N", "N-M", "N=addr", "N-M=addr" or
// "ip:port=addr". An invalid entry would make the engine exit fatally and the
// tunnel service crash-loop, so it must be rejected before it reaches a config.
func validPortSpec(spec string) bool {
	spec = strings.TrimSpace(spec)
	parts := strings.SplitN(spec, "=", 2)
	local := strings.TrimSpace(parts[0])
	if local == "" {
		return false
	}
	if len(parts) == 2 && strings.TrimSpace(parts[1]) == "" {
		return false // "443=" — empty destination
	}
	// "ip:port=addr" — a full local address is only valid in mapping form.
	if h, p, err := net.SplitHostPort(local); err == nil && h != "" {
		return len(parts) == 2 && validPort(p)
	}
	// Port range "N-M".
	if strings.Contains(local, "-") {
		r := strings.Split(local, "-")
		if len(r) != 2 {
			return false
		}
		lo, hi := strings.TrimSpace(r[0]), strings.TrimSpace(r[1])
		if !validPort(lo) || !validPort(hi) {
			return false
		}
		a, _ := strconv.Atoi(lo)
		b, _ := strconv.Atoi(hi)
		return b >= a
	}
	// Plain single port.
	return validPort(local)
}

// validatePortSpecs checks every forwarded-port entry, returning a descriptive
// error for the first invalid one.
func validatePortSpecs(ports []string) error {
	for _, p := range ports {
		if !validPortSpec(p) {
			return fmt.Errorf("invalid port entry %q — use forms like 443, 400-450, 443=1.1.1.1:443", strings.TrimSpace(p))
		}
	}
	return nil
}

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

// errBadName is what the two config readers answer a name that could not have
// been written by this program.
//
// app.ConfigPath is plain concatenation — ConfigDir + "/" + name + ".toml" —
// so a name carrying a separator names a file outside the config directory.
// Nothing local produces one: the wizard and the panel form both check the name
// on the way in. What reaches these readers unchecked is a name off the wire —
// a query parameter on /api/tunnel/settings, or the body of a node's OpSettings
// request — and neither had anything between it and the path.
//
// Both callers already require full administrative authority, so this closes a
// gap rather than an escalation. It is worth closing anyway: the check exists,
// it is one line, and the reason it was not here is that nobody wrote it down.
func errBadName(name string) error {
	return fmt.Errorf("%q is not a valid tunnel name (letters, digits, dot, dash and underscore, up to 40)", name)
}

// checkName refuses a name that must not be turned into a path.
func checkName(name string) error {
	if !validName(name) {
		return errBadName(name)
	}
	return nil
}

// ValidName is validName for callers outside this package.
//
// It exists for one job: checking a name that arrived from another machine.
// Names created here go through the wizard or the panel form and are checked
// on the way in, so nothing local needs this. A name read off a managed
// server's config directory is a filename that server chose, and on Linux a
// filename may hold anything but "/" and NUL — quotes, angle brackets, a whole
// script tag. Nothing downstream of that read was treating it as foreign.
func ValidName(name string) bool { return validName(name) }

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
// "ip:port=addr". The engine reports and ignores an entry it cannot read now
// rather than exiting on it, so a bad one no longer takes the tunnel with it —
// but a mapping that is silently doing nothing is still worth refusing here,
// where the operator is looking at it and can fix it.
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

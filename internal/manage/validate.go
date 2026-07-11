package manage

import (
	"strconv"
	"strings"
)

// validPort reports whether s is a valid TCP/UDP port number.
func validPort(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1 && n <= 65535
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

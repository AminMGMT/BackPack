package core

import (
	"fmt"
	"regexp"
	"strings"
)

// The three small decisions that have to live below everything else.
//
// A tunnel's role and its name are read by the listing, which is the lowest
// layer there is — so these cannot sit in the rendering and validation files
// that own the rest of their subject, or core would depend on the packages
// that depend on it.
// nameRe restricts tunnel names to characters that are safe in file paths,
// systemd unit names and the web UI (no spaces, quotes or slashes).
var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)

// ValidName reports whether a tunnel name is acceptable.
func ValidName(name string) bool { return nameRe.MatchString(name) }

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
// ErrBadName is exported for the readers that live above this package.
func ErrBadName(name string) error {
	return fmt.Errorf("%q is not a valid tunnel name (letters, digits, dot, dash and underscore, up to 40)", name)
}

// DirectRole turns the engine's edge/origin into what an operator recognises.
func DirectRole(resolved string) string {
	if resolved == "origin" {
		return "kharej"
	}
	return "iran"
}

// L3Role does the same for the layer-3 tunnel's dial/listen.
func L3Role(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "listen") {
		return "kharej"
	}
	return "iran"
}

// checkName refuses a name that must not be turned into a path.
// CheckName is checkName's exported half; see manage for the callers.
func CheckName(name string) error {
	if !ValidName(name) {
		return ErrBadName(name)
	}
	return nil
}

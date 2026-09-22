package manage

// ValidName is validName for callers outside this package.
//
// It exists for one job: checking a name that arrived from another machine.
// Names created here go through the wizard or the panel form and are checked
// on the way in, so nothing local needs this. A name read off a managed
// server's config directory is a filename that server chose, and on Linux a
// filename may hold anything but "/" and NUL — quotes, angle brackets, a whole
// script tag. Nothing downstream of that read was treating it as foreign.
//
// The port and config-file validation this file used to hold is in
// internal/manage/spec; see spec_alias.go.
func ValidName(name string) bool { return validName(name) }

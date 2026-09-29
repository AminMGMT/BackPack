package transport

import "strings"

// connected reads the one-line status every transport publishes for the panel:
// it is set to "Connected (…)" exactly when the control channel is established
// and cleared when it is not. That is the same fact a transport-fallback chain
// needs — see internal/tunnel/chain — so it is read here rather than invented
// again.
func connected(status string) bool { return strings.HasPrefix(status, "Connected") }

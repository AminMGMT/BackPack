package transport

import "strings"

// connected reads the one-line panel status as "is a control channel up". Every
// transport sets it to "Connected (…)" exactly when its control channel is
// established and clears it when it is not; lifecycle.Running is built on it,
// and the transport-fallback chain (internal/tunnel/chain) asks that.
func connected(status string) bool { return strings.HasPrefix(status, "Connected") }

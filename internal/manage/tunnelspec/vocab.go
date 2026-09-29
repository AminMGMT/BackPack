package tunnelspec

import (
	"github.com/backpack/backpack/internal/manage/core"
	"github.com/backpack/backpack/internal/manage/spec"
)

// The transport and address vocabulary, under the names the code in this
// package was written with. See internal/manage/spec.
var (
	isMux                 = spec.IsMux
	isKCP                 = spec.IsKCP
	isWS                  = spec.IsWS
	needsTLS              = spec.NeedsTLS
	supportsProxyProtocol = spec.SupportsProxyProtocol
	addrPort              = spec.AddrPort
	quote                 = spec.Quote
)

// Tunnel is a discovered tunnel derived from a config file on disk.
type Tunnel = core.Tunnel

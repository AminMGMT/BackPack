package manage

import "github.com/backpack/backpack/internal/manage/spec"

// The vocabulary this package is written in now lives in internal/manage/spec:
// what a transport is, what an address and a port look like, and where a tunnel
// binds. See that package's doc for why it was the first thing to lift out.
//
// The names are re-declared here under exactly the names they had, the same way
// core_alias.go re-exports internal/manage/core. Sixty-two files in this
// package call them and six packages call into this one, so a move that renamed
// anything would be a move that touched all of them — and a refactor whose diff
// is every call site is a refactor nobody can review.

// tunnelBind is a control port as the operator wrote it.
type tunnelBind = spec.TunnelBind

var (
	// What a transport is.
	isMux                 = spec.IsMux
	isKCP                 = spec.IsKCP
	isDatagram            = spec.IsDatagram
	isWS                  = spec.IsWS
	needsTLS              = spec.NeedsTLS
	validTransport        = spec.ValidTransport
	supportsProxyProtocol = spec.SupportsProxyProtocol

	// Addresses and ports.
	addrHost          = spec.AddrHost
	addrPort          = spec.AddrPort
	isWildcardBind    = spec.IsWildcardBind
	quote             = spec.Quote
	validPort         = spec.ValidPort
	parsePorts        = spec.ParsePorts
	validatePortSpecs = spec.ValidatePortSpecs

	// Where the tunnel's own control port listens.
	parseTunnelBind = spec.ParseTunnelBind
	bindHostOf      = spec.BindHostOf
	localAddrExists = spec.LocalAddrExists
)

// ValidateConfigFile reports what is wrong with a tunnel configuration on disk,
// without starting anything. See spec.ValidateConfigFile.
func ValidateConfigFile(path string) []string { return spec.ValidateConfigFile(path) }

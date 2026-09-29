package health

import "github.com/backpack/backpack/internal/manage/spec"

// The transport and address vocabulary, under the names the code in this
// package was written with. See internal/manage/spec.
var (
	isDatagram        = spec.IsDatagram
	needsTLS          = spec.NeedsTLS
	addrHost          = spec.AddrHost
	addrPort          = spec.AddrPort
	bindHostOf        = spec.BindHostOf
	localAddrExists   = spec.LocalAddrExists
	validatePortSpecs = spec.ValidatePortSpecs
)

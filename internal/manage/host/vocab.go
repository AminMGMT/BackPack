package host

import (
	"github.com/backpack/backpack/internal/manage/core"
	"github.com/backpack/backpack/internal/manage/spec"
)

// The tunnel listing and the address vocabulary, under the names the code in
// this package was written with.
type tunnel = core.Tunnel

var (
	listTunnels    = core.List
	fileExists     = core.FileExists
	addrPort       = spec.AddrPort
	isDatagram     = spec.IsDatagram
	isWildcardBind = spec.IsWildcardBind
)

package manage

import "github.com/backpack/backpack/internal/manage/host"

// What this machine is — its addresses, the ports it already holds, its TLS
// certificates, its reverse-path filter, whether UDP leaves it — now lives in
// internal/manage/host. See that package's doc.
//
// As with core_alias.go, the names are re-declared here exactly as they were,
// so no caller in this package or outside it changed.

// UDPEgress is what a probe learned about UDP leaving this machine.
type UDPEgress = host.UDPEgress

// Where a public address came from.
const (
	SourceInterface = host.SourceInterface
	SourceEcho      = host.SourceEcho

	ephemeralDefaultHigh = host.EphemeralDefaultHigh
	ephemeralDefaultLow  = host.EphemeralDefaultLow
)

var (
	PublicIPv4       = host.PublicIPv4
	PublicIPv6       = host.PublicIPv6
	PublicIPv4Detail = host.PublicIPv4Detail
	PublicIPv6Detail = host.PublicIPv6Detail
	PortInUse        = host.PortInUse
	TunnelPortInUse  = host.TunnelPortInUse
	ReservedPorts    = host.ReservedPorts
	portClash        = host.PortClash

	ephemeralRangeIsWide = host.EphemeralRangeIsWide

	CertExpiry           = host.CertExpiry
	EnsureSelfSignedCert = host.EnsureSelfSignedCert
	EnsurePanelCert      = host.EnsurePanelCert
	validCertPair        = host.ValidCertPair

	OfferRelaxRPFilter = host.OfferRelaxRPFilter
	ProbeUDPEgress     = host.ProbeUDPEgress
)

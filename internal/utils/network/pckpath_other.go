//go:build !linux

package network

import (
	"errors"
	"net"
)

// The paqet-based PCK engine is Linux-only. These exist so that code which
// names them builds everywhere and fails with a message where it runs.

var errPckPathLinuxOnly = errors.New("pck: the packet carrier is available on Linux only")

// PckPath is how a frame leaves this machine toward the peer.
type PckPath struct {
	Iface           string
	LocalIP         net.IP
	SrcMAC, NextHop net.HardwareAddr
	Ethernet        bool
}

// DiscoverPckPath is Linux-only.
func DiscoverPckPath(net.IP, string, string) (*PckPath, error) { return nil, errPckPathLinuxOnly }

// PckLease is Linux-only.
type PckLease struct{}

// LeasePckPorts is Linux-only.
func LeasePckPorts(string, bool, uint16, *PckPath, net.IP) (*PckLease, error) {
	return nil, errPckPathLinuxOnly
}

// ClaimPort is Linux-only.
func (*PckLease) ClaimPort() (uint16, error) { return 0, errPckPathLinuxOnly }

// ReleasePort is Linux-only.
func (*PckLease) ReleasePort(uint16) {}

// PortRange is Linux-only.
func (*PckLease) PortRange() (lo, hi uint16) { return 0, 0 }

// GuardInstalled is Linux-only.
func (*PckLease) GuardInstalled() bool { return false }

// Close is Linux-only.
func (*PckLease) Close() {}

// PckGuardStatus is what this host has for a PCK tunnel's firewall guard.
type PckGuardStatus struct {
	Iptables bool
	Rules    int
	Err      error
}

// PckGuardWant is the number of rules a whole guard has.
const PckGuardWant = 3

// PckGuardState is Linux-only.
func PckGuardState(string) PckGuardStatus { return PckGuardStatus{Err: errPckPathLinuxOnly} }

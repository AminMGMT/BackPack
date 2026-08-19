//go:build !linux

package mssclamp

import "github.com/sirupsen/logrus"

// Clamping is iptables work, and iptables is Linux. The rule arithmetic in
// mssclamp.go is portable and tested everywhere; only installing it is not.

func Apply(kind, iface string, mtu, configured int, log *logrus.Logger) {}

func Remove(kind, iface string, mtu, configured int) {}

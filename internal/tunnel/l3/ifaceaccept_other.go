//go:build !linux

package l3

import "github.com/sirupsen/logrus"

type ifaceAccept struct{}

func installIfaceAccept(string, *logrus.Logger) *ifaceAccept { return nil }

func (a *ifaceAccept) remove() {}

// CleanupIfaceAccept is a no-op off Linux.
func CleanupIfaceAccept(string) {}

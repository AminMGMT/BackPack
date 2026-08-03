//go:build !linux

package network

import "errors"

// errNotLinux is returned rather than silently ignored. Both of these are Linux
// socket options; a configuration that asks for one on another system has asked
// for something that cannot happen, and saying so is better than dialling out
// by a route the operator did not choose.
//
// Backpack's servers are Linux. This exists so local builds on macOS and
// Windows compile, and so a configuration written for one is not quietly
// half-applied on the other.
var errNotLinux = errors.New("only available on Linux")

func bindToInterface(fd uintptr, name string) error { return errNotLinux }

func setFirewallMark(fd uintptr, mark int) error { return errNotLinux }

//go:build !linux

package network

// CleanupPckGuards is a no-op where PCK cannot install kernel rules.
func CleanupPckGuards(...string) {}

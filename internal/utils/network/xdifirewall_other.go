//go:build !linux

package network

// XdiFirewallNote has nothing to read off Linux.
func XdiFirewallNote(server bool) string { return "" }

package spec

import (
	"net"
	"strconv"
	"strings"
)

// AddrHost returns the host part of a host:port address, or fallback when the
// address does not have one.
func AddrHost(addr, fallback string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil && h != "" {
		return h
	}
	return fallback
}

// AddrPort returns the port part of a host:port address, or "".
func AddrPort(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return ""
}

// IsWildcardBind reports whether a host means "every interface". The brackets
// are trimmed because an IPv6 bind address arrives written as "[::]".
func IsWildcardBind(host string) bool {
	switch strings.Trim(host, "[]") {
	case "", "0.0.0.0", "::":
		return true
	}
	return false
}

// Quote renders a string as a TOML string literal.
func Quote(s string) string { return strconv.Quote(s) }

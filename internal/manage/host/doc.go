// Package host is what this machine is, as far as setting up a tunnel is
// concerned: its public addresses, the ports it already holds and the ones the
// kernel hands out, its TLS certificates, its reverse-path filter, and whether
// UDP can leave it.
//
// Every question here is answered by this machine and nothing else, which is
// why it is apart from the tunnel spec (tunnelspec) and the health checks
// (health) that ask them. It sits on core and spec.
//
// The names are re-declared in internal/manage exactly as they were
// (host_alias.go), so no caller changed.
package host

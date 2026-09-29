// Package config is the tunnel configuration file's schema: the TOML tables
// [server], [client], [direct] and [l3], their keys, and the few rules that
// belong to the schema itself (which transports exist, what a fallback chain
// may name). Defaults and the checks that need the machine are applied by the
// engine (package cmd); docs/config-reference.md is generated from here.
package config

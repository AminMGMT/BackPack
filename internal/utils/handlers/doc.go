// Package handlers is the relay: it carries bytes between a user's connection
// and the tunnel, with buffers from a pool, kernel splice where Linux allows it
// (zero-copy, behind a flag), the PROXY protocol header when the operator asks
// for the client's address, and per-port usage for the panel.
package handlers

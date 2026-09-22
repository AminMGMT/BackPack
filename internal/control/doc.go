// Package control owns the things the panel operates on, as distinct from the
// panel that displays them.
//
// # Why this package exists
//
// internal/webui served HTML and owned the fleet. The runner that reaches every
// managed server, the loop that measures the path to each of them, and the
// state of every long-running operation all lived in package-level variables
// next to HTTP handlers. Nothing was broken by that, and everything built on
// top of it made the eventual separation harder: each new operations feature —
// a fleet-wide action, a staged rollout, health defined by throughput — added
// one more package-level variable to a package whose job is to render a page.
//
// This is a **package** boundary and not a service boundary. There is no new
// process, no socket, no daemon: the single-binary install is untouched. What
// changes is that the fleet and the jobs have an owner that is not an HTTP
// server, so a second reader — a CLI, an API, the monitor — is a caller rather
// than a rewrite.
//
// # What is in here
//
//   - Jobs: one abstraction for operations that outlive the request that
//     started them. See job.go for why there were three hand-rolled ones.
//   - Net: the path measurement to every managed server.
//   - Fleet: the runner that reaches them.
//   - Desired: what each managed server is supposed to be running, and the
//     comparison against what it reports. See desired.go.
//
// # What is deliberately not in here
//
// Remediation. Desired state detects a difference and stops there, and that is
// a design decision with a reason particular to this product rather than an
// unfinished feature: a reconciler that re-applies on its own is a loop that
// can fight an operator mid-change, and what it would be fighting over is the
// tunnel that operator is reaching the machine through. See desired.go.
package control

// Package core is the bottom of internal/manage: the tunnel listing, the
// systemd operations, the two service units this product installs beside the
// tunnels, and the few helpers all of them rest on.
//
// # Why it was cut here
//
// internal/manage was 28,000 lines in one package — wizards, editing, backup,
// restore, update, migration, diagnosis, presets, the share-link
// codec and the web API adapters — and every other package imports it
// wholesale. The cost is not confusion: it is well organised inside. The cost
// is that a change to how a systemd unit is written is a change to the package
// the panel, the CLI, the monitor and the node RPC all depend on.
//
// This was the one seam in that package that could be cut without untangling
// anything first: everything above sits on it and it sits on nothing above.
// The candidates above it — configuration, diagnosis — were mutually entangled
// through the tunnel-spec lifecycle, and were cut later, once the few edges
// that held the cycle together had been turned round: see tunnelspec, host and
// health. The layers are now core and spec at the bottom, then host,
// tunnelspec and backup, then health, then the wizards, the panel's API,
// update and migration in internal/manage itself (docs/adr/0003).
//
// # The names did not move
//
// manage.List, manage.Tunnel and manage.RestartService are called from six
// packages. They are re-exported from internal/manage (see core_alias.go), so
// the public surface is exactly what it was and the seam is internal. That is
// deliberate: a split that moved the names would be a split that touched every
// caller, which is the change nobody can review.
//
// # One inversion
//
// Delete used to call ForgetNodePair, which lives above this package. It is a
// hook now (OnDelete), registered by the package that owns the pairing record —
// so core is told what to clean up rather than reaching up to do it.
package core

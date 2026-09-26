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
// This is the one seam in that package that could be cut without untangling
// anything first: everything above sits on it and it sits on nothing above.
// Every other candidate — configuration, update, diagnosis — is mutually
// entangled through the tunnel-spec lifecycle, where config rendering, editing
// and direct-tunnel rendering each call into the other two. Cutting one of
// those means breaking a real cycle, which is design work rather than moving
// files, and doing it as one sweep is how a working install breaks quietly.
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

// Package tunnelspec is what a tunnel's configuration says and how a change to
// it is made safely: the reverse tunnel's Spec and the direct and layer-3
// tunnels' own specs, rendering them to TOML and loading them back, applying a
// change with revert-if-it-will-not-start, the performance presets, and the
// history of configurations a change replaced.
//
// # Why it was cut here
//
// core's doc named the reason internal/manage could not be split further: the
// tunnel-spec lifecycle — rendering, editing, direct-tunnel rendering — sat in
// one cycle with the wizards, the health checks and the web API, and 41 files
// reached each other. The cycle was held together by a handful of small edges
// pointing the wrong way: the renderer reading which machine it was for out of
// the direct-setup wizard, the spec loader asking the health code how to wait
// for a unit, the preset list shaped as an anonymous struct only its own
// package could read. With those turned the right way round this package sits
// on core and spec and nothing above them, and everything that edits a tunnel —
// the menu, the panel, the bot, the benchmark — sits on it.
//
// # The names did not move
//
// As with core, every name is re-declared in internal/manage exactly as it was
// (tunnelspec_alias.go), so no caller changed.
package tunnelspec

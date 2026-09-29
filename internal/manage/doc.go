// Package manage is the management work every front end shares — the menu,
// the panel, the Telegram bot, the CLI and the fleet RPC all call it: the
// setup wizards, editing a tunnel, share links, the panel's API adapters,
// update and rollback, migration, benchmarks.
//
// It sits on layers of its own (docs/adr/0003): core and spec (the tunnel
// listing, systemd, the transport vocabulary), host (this machine), tunnelspec
// (a tunnel's configuration and applying a change safely), backup, and health
// (whether the tunnels work). Their names are re-exported here unchanged, so a
// caller sees one package.
package manage

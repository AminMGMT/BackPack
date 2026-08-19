package manage

import (
	"fmt"

	"github.com/backpack/backpack/internal/tui"
)

// The two ways into setup.
//
// The menu used to offer "Setup Server" and "Setup Client". Those names were
// already a little confusing — in a reverse tunnel the Iran machine is the
// server — and with a direct tunnel they become actively wrong, because there
// the Iran machine is the one that dials. A name that means the opposite of
// what it says is worse than a name nobody recognises.
//
// So the entries are geographic, which is the thing that does not change with
// the direction: Iran exposes the ports either way, kharej holds the real
// service either way. Only who reaches out first differs, and that is asked
// rather than encoded in the name.
//
// The order is deliberate. An operator always knows which machine they are
// sitting on; they may not yet know which direction they need. Asking the
// certain question first means the uncertain one arrives with the two options
// described side by side, at the moment it can be answered.
//
// # What this does not do
//
// It dispatches. SetupServer and SetupClient are called unchanged, so a
// reverse tunnel is built by exactly the code that has always built one — this
// adds a question in front of them and nothing else.

// SetupIran is the entry point for the Iran server: the machine users connect
// to, which exposes the forwarded ports in both directions.
func SetupIran() {
	switch askDirection("Iran") {
	case directionReverse:
		// The Iran side of a reverse tunnel is, in the config, the server.
		SetupServer()
	case directionDirect:
		setupDirectFor(sideIran)
	}
}

// SetupKharej is the entry point for the server abroad: the machine that holds
// the real service.
func SetupKharej() {
	switch askDirection("Kharej") {
	case directionReverse:
		// The kharej side of a reverse tunnel is, in the config, the client.
		SetupClient()
	case directionDirect:
		setupDirectFor(sideKharej)
	}
}

type tunnelDirection int

const (
	directionCancelled tunnelDirection = iota
	directionReverse
	directionDirect
)

// askDirection is the one question that decides which engine builds the
// tunnel. Both options are described by the situation that calls for them,
// because "reverse" and "direct" mean nothing to somebody setting up their
// first tunnel.
func askDirection(machine string) tunnelDirection {
	tui.Clear()
	tui.Title("Setup " + machine)
	tui.Warn("Both directions expose the same ports on Iran and keep the real")
	tui.Warn("service on kharej. What changes is which machine reaches out first.")
	fmt.Println()

	switch tui.ChooseOpt("Which direction should the tunnel be built in?", []tui.Option{
		{
			Title: "Reverse",
			Desc:  "kharej dials Iran — the usual choice, and what to try first",
		},
		{
			Title: "Direct",
			Desc:  "Iran dials kharej — use it when an inbound connection to Iran does not get through",
		},
	}) {
	case 0:
		return directionReverse
	case 1:
		return directionDirect
	}
	return directionCancelled
}

// setupDirectFor runs the direct wizard for a machine already chosen, so the
// question the menu just answered is not asked a second time.
func setupDirectFor(side directSide) {
	tui.Clear()
	tui.Title("Direct Tunnel — " + sideName(side))
	tui.Warn("The Iran server dials out to kharej, instead of waiting to be dialled.")
	fmt.Println()

	// Straight to how it travels. There is no kind to choose any more: a direct
	// tunnel is a full IP tunnel wrapped in Backpack's own GRE, and the only
	// open question is which carrier gets it across.
	setupL3(side)
}

func sideName(s directSide) string {
	if s == sideIran {
		return "Iran"
	}
	return "Kharej"
}

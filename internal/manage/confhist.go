package manage

import (
	"fmt"

	"github.com/backpack/backpack/internal/tui"
)

// editConfigHistory is the menu entry: what this tunnel used to be, and the
// chance to put one of them back.
//
// It offers the moments rather than the contents. A stored configuration holds
// the tunnel's token, and printing a list of them to a terminal — which is very
// often a terminal somebody is sharing a screenshot of — would put it where it
// does not need to go. What was changed is visible after restoring, from the
// screen that shows the settings.
func editConfigHistory(name string) {
	hist := ConfigHistory(name)
	if len(hist) == 0 {
		fmt.Println()
		tui.Info("No Earlier Config Yet.")
		tui.PressEnter()
		return
	}

	tui.Clear()
	tui.Title("Undo A Change · " + name)
	fmt.Println()
	tui.Info("Each Entry Is The Config Before That Change.")
	fmt.Println()

	opts := make([]tui.Option, 0, len(hist))
	for _, c := range hist {
		desc := "the configuration in place before this"
		if c.Note != "" {
			desc = c.Note
		}
		opts = append(opts, tui.Option{
			Title: "Before " + c.At.Format("2 Jan 15:04:05"),
			Desc:  desc,
		})
	}

	idx := tui.ChooseOpt("Restore", opts)
	if idx < 0 || idx >= len(hist) {
		return
	}
	chosen := hist[idx]

	fmt.Println()
	tui.Warn("The Tunnel Restarts.")
	fmt.Println()
	if !tui.Confirm("Restore The Config From Before "+chosen.At.Format("2 Jan 15:04:05"), false) {
		return
	}
	if err := RestoreConfigFrom(name, chosen.At); err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Restored — Running.")
	tui.PressEnter()
}

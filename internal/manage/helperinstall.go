package manage

import (
	"time"

	"github.com/backpack/backpack/internal/helpers"
	"github.com/backpack/backpack/internal/tui"
)

// managedHelperPath is where the pinned helper tool lives, the path managed
// tunnels are configured with.
func managedHelperPath(tool string) string { return helpers.Path(tool) }

// ensureHelper is tool's pinned helper at its usual place, installed first
// when it is missing: a missing helper is the one thing a managed HTTPS
// tunnel cannot make for itself, and the installer adds helpers only on
// request. GitHub is reached as Update reaches it, directly or through a
// tunnel relay.
var ensureHelper = func(tool string, logf func(string)) (string, error) {
	path := managedHelperPath(tool)
	if executable(path) {
		return path, nil
	}
	var via []helpers.Source
	for _, s := range sources(3 * time.Minute) {
		via = append(via, helpers.Source{Name: s.name, Client: s.client})
	}
	return helpers.Install(tool, via, logf)
}

// helperFor is the helper binary a wizard suggests: current, an installed
// one, or the pinned one, installed first when none is found. A path of the
// operator's own is left as it is.
func helperFor(tool, current string) string {
	binary := defaultString(current, managedHelperPath(tool))
	if tool == "xray" {
		binary = visionBinary(current)
	}
	if executable(binary) || (current != "" && current != managedHelperPath(tool)) {
		return binary
	}
	name := helperName(tool)
	tui.Info("Installing The " + name + " Helper...")
	path, err := ensureHelper(tool, func(line string) { tui.Info(line) })
	if err != nil {
		tui.Error("Could Not Install " + name + ": " + err.Error())
		return binary
	}
	return path
}

func helperName(tool string) string {
	switch tool {
	case "xray":
		return "Xray"
	case "naive":
		return "NaiveProxy"
	case "sing-box":
		return "Sing-Box"
	}
	return tool
}

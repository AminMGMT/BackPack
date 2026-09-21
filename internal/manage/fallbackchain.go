package manage

import (
	"fmt"
	"strings"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tui"
)

// The transport fallback chain, from the operator's side.
//
// FallbackAddrs answers "this address stopped answering". This answers the
// other half: "this carrier stopped getting through" — the case where the
// server is perfectly reachable and it is the protocol on the wire that is
// being filtered. The two are independent and a tunnel may use both.

// SetFallbackTransports replaces a tunnel's fallback chain. An empty list
// clears it, which returns the tunnel to a single transport.
//
// It applies to either role, and it has to: a chain on one end alone does
// nothing, because the other end is still listening for — or dialling — one
// carrier. That asymmetry is the most likely way to configure this wrongly, so
// it is stated in the caller's output rather than left to be discovered.
func SetFallbackTransports(name string, list []string, dwell int) error {
	s, err := LoadSpec(name)
	if err != nil {
		return err
	}
	clean, err := cleanChain(s.Transport, list)
	if err != nil {
		return err
	}
	if dwell < 0 {
		return fmt.Errorf("dwell cannot be negative")
	}
	s.FallbackTransports = clean
	s.FallbackDwell = dwell
	return applySpec(s)
}

// cleanChain normalises and validates a chain against the tunnel's own
// transport. The primary is dropped if it is named again — the chain always
// starts there, so repeating it is harmless but misleading in the file.
func cleanChain(primary string, list []string) ([]string, error) {
	var out []config.TransportType
	var clean []string
	seen := map[string]bool{strings.TrimSpace(primary): true}
	for _, t := range list {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, config.TransportType(t))
		clean = append(clean, t)
	}
	if err := config.ValidateFallbackTransports(config.TransportType(primary), out); err != nil {
		return nil, err
	}
	return clean, nil
}

// chainSummary renders a chain for a menu line.
func chainSummary(primary string, list []string) string {
	if len(list) == 0 {
		return "off (" + transportLabel(primary) + " only)"
	}
	return strings.Join(append([]string{primary}, list...), " → ")
}

// changeFallbackTransports is the menu screen. It is deliberately explicit
// about the one thing that makes this configuration fail silently: both ends
// need the same list, and nothing on this machine can check the other end.
func changeFallbackTransports(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Title("Transport fallback chain")
	tui.Warn("Backup addresses handle a server IP that stops answering. This handles")
	tui.Warn("the other case: the server is reachable but the carrier itself is being")
	tui.Warn("filtered. The tunnel then tries the next carrier on the list by itself.")
	fmt.Println()
	tui.Info("Transport now : " + transportLabel(spec.Transport))
	tui.Info("Chain now     : " + chainSummary(spec.Transport, spec.FallbackTransports))
	fmt.Println()
	tui.Warn("BOTH ENDS must carry the same list in the same order. They never tell")
	tui.Warn("each other where they are — they meet because the server holds each")
	tui.Warn("carrier while the other end tries the whole list. A list on one end")
	tui.Warn("only will leave the tunnel down.")
	fmt.Println()
	tui.Warn("Enter the carriers to fall back to, comma separated, in order, e.g.:")
	tui.Warn("    wss, quic, kcp")
	tui.Warn("Leave empty to switch fallback off.")
	fmt.Println()

	raw := tui.Prompt("Fallback carriers: ")
	var list []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			list = append(list, p)
		}
	}

	dwell := spec.FallbackDwell
	if len(list) > 0 {
		fmt.Println()
		tui.Warn("How long to hold each carrier before moving on, in seconds.")
		tui.Warn("Too short and a slow path looks blocked; too long and recovery drags.")
		tui.Warn(fmt.Sprintf("Leave empty for the default (%d).",
			int(config.DefaultFallbackDwell.Seconds())))
		if v := strings.TrimSpace(tui.Prompt("Seconds: ")); v != "" {
			n := 0
			if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
				tui.Error("Not a number of seconds.")
				tui.PressEnter()
				return
			}
			dwell = n
		}
	}

	if err := SetFallbackTransports(name, list, dwell); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	if len(list) == 0 {
		tui.Success("Transport fallback switched off — the tunnel restarted.")
		tui.PressEnter()
		return
	}
	tui.Success("Chain saved — the tunnel restarted: " + chainSummary(spec.Transport, list))
	fmt.Println()
	tui.Warn("Now set the SAME chain on the other end, or this tunnel will spend")
	tui.Warn("its time on carriers the other end is not listening for.")
	tui.PressEnter()
}

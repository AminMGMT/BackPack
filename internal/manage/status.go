package manage

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/tui"
)

// StatusLive renders a continuously refreshing status table of all tunnels
// until the user presses Ctrl+C.
func StatusLive() {
	tunnels := List()
	if len(tunnels) == 0 {
		tui.Warn("No tunnels configured yet.")
		tui.PressEnter()
		return
	}

	// Host-wide addresses fetched once.
	ipv4 := PublicIPv4()
	ipv6 := PublicIPv6()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	render := func() {
		tui.Clear()
		tui.Colorize(tui.Cyan, "Live Tunnel Status", true)
		fmt.Printf("%sIPv4:%s %s    %sIPv6:%s %s\n",
			tui.Gray, tui.Reset, ipv4, tui.Gray, tui.Reset, ipv6)
		fmt.Printf("%sUpdated: %s   (press Ctrl+C to return)%s\n\n",
			tui.Gray, time.Now().Format("15:04:05"), tui.Reset)
		printStatusTable(List())
	}

	render()
	for {
		select {
		case <-sig:
			fmt.Println()
			return
		case <-ticker.C:
			render()
		}
	}
}

// printStatusTable prints a formatted table of tunnel states.
func printStatusTable(tunnels []Tunnel) {
	header := fmt.Sprintf("%-16s %-8s %-8s %-8s %s", "NAME", "ROLE", "TRANSP", "STATE", "PORTS / REMOTE")
	fmt.Println(tui.Bold + header + tui.Reset)
	fmt.Println(strings.Repeat("─", 72))

	for _, t := range tunnels {
		plainState, color := "stopped", tui.Red
		if IsActive(t.Service) {
			plainState, color = "running", tui.Green
		}
		state := colorPad(tui.Color(color, plainState), plainState, 8)

		detail := t.Addr
		if t.Role == "server" && len(t.Ports) > 0 {
			detail = strings.Join(t.Ports, ",")
		}
		fmt.Printf("%-16s %-8s %-8s %s %s\n",
			truncate(t.Name, 16), t.Role, t.Transport, state, detail)
	}
}

// colorPad returns a colored value padded to width based on its plain length,
// so ANSI escapes don't distort column alignment.
func colorPad(colored, plain string, width int) string {
	pad := width - len(plain)
	if pad < 0 {
		pad = 0
	}
	return colored + strings.Repeat(" ", pad)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

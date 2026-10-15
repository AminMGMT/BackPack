package manage

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/backpack/backpack/internal/tui"
)

func ctChooseHosts() []string {
	addresses := ConnTestAddresses()
	byFamily := map[string][]string{}
	for _, a := range addresses {
		fmt.Printf("  %s: %s (%s)\n", a.Family, a.Host, a.Interface)
		byFamily[a.Family] = append(byFamily[a.Family], a.Host)
	}
	tui.Info("Detected addresses are candidates; Kharej checks actual reachability. NAT/private addresses can be entered manually.")
	modes := []string{}
	options := []tui.Option{}
	if len(byFamily["IPv4"]) > 0 && len(byFamily["IPv6"]) > 0 {
		modes = append(modes, "both")
		options = append(options, tui.Option{Title: "Both IPv4 and IPv6", Desc: "sequential tests, separate results"})
	}
	for _, f := range []string{"IPv4", "IPv6"} {
		if len(byFamily[f]) > 0 {
			modes = append(modes, f)
			options = append(options, tui.Option{Title: "Only " + f, Desc: "choose this server's address"})
		}
	}
	modes = append(modes, "manual")
	options = append(options, tui.Option{Title: "Enter an address", Desc: "one IP/hostname, or a pair of addresses"})
	pick := tui.ChooseOpt("Paths to test", options)
	if pick < 0 {
		return nil
	}
	mode := modes[pick]
	choose := func(f string) string {
		ips := byFamily[f]
		if len(ips) == 1 {
			return ips[0]
		}
		opts := make([]tui.Option, len(ips))
		for i, ip := range ips {
			opts[i] = tui.Option{Title: ip}
		}
		i := tui.ChooseOpt(f+" address", opts)
		if i < 0 {
			return ""
		}
		return ips[i]
	}
	var host, v4, v6 string
	switch mode {
	case "both":
		v4, v6 = choose("IPv4"), choose("IPv6")
		if v4 == "" || v6 == "" {
			return nil
		}
	case "IPv4":
		mode = "ipv4"
		v4 = choose("IPv4")
		if v4 == "" {
			return nil
		}
	case "IPv6":
		mode = "ipv6"
		v6 = choose("IPv6")
		if v6 == "" {
			return nil
		}
	case "manual":
		i := tui.ChooseOpt("Enter", []tui.Option{{Title: "One address"}, {Title: "Both IPv4 and IPv6"}})
		if i < 0 {
			return nil
		}
		if i == 0 {
			host = tui.Prompt("This Server's IP or hostname: ")
		} else {
			mode = "both"
			v4 = tui.Prompt("IPv4 address or hostname: ")
			v6 = tui.Prompt("IPv6 address or hostname: ")
		}
	}
	hosts, err := ConnTestHosts(context.Background(), mode, host, v4, v6)
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return nil
	}
	return hosts
}

type ctPathBoard struct {
	board    *ctBoard
	index    int
	finished map[int]bool
	failed   map[int]bool
}

func (b *ctPathBoard) abandon() {
	if b.board != nil {
		b.board.abandon()
		b.board = nil
	}
}

func (b *ctPathBoard) update(i int, p ConnTestPath) {
	if b.finished == nil {
		b.finished = map[int]bool{}
		b.failed = map[int]bool{}
	}
	if p.State == "unavailable" && !b.failed[i] {
		b.abandon()
		tui.Warn(p.Family + " · " + p.Host + ": " + p.Error)
		b.failed[i] = true
	}
	if p.State != "running" && p.State != "done" {
		return
	}
	if b.finished[i] {
		return
	}
	if b.board == nil || b.index != i {
		b.abandon()
		fmt.Println()
		b.board = newCTBoard(os.Stdout, p.Family+" · "+p.Host)
		b.index = i
	}
	b.board.setAll(p.Rows)
	if p.State == "done" {
		b.board.finish(p.Rows, p.Best)
		b.finished[i] = true
		b.board = nil
	}
}

func ctSuiteSummary(out io.Writer, paths []ConnTestPath) {
	fmt.Fprintln(out, "\nIP path comparison:")
	for _, p := range paths {
		fmt.Fprintf(out, "  %s · %s · %s", p.Family, p.Host, p.State)
		if p.Error != "" {
			fmt.Fprintf(out, " · %s", p.Error)
		}
		if p.Best.Transport != "" {
			fmt.Fprintf(out, " · %s · down %.2f / up %.2f Mb/s · %d ms", p.Best.Transport, p.Best.Mbps, p.Best.UploadMbps, p.Best.RTTms)
		}
		fmt.Fprintln(out)
	}
	best := ConnTestSuiteBest(paths)
	if best.Transport != "" {
		fmt.Fprintf(out, "Recommended: %s · %s · %s\n", best.Family, best.Host, best.Transport)
	}
}

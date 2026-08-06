package manage

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/spooftest"
	"github.com/backpack/backpack/internal/tui"
)

// SpoofTest is the interactive spoof-capability tester: it discovers which
// forged source IPs actually cross the network between two nodes. Run the
// receiver on one node and the sender on the other, then swap to map the reverse
// direction. The forged sources that pass become the spoof_src_pool for the
// matching end of a spoof tunnel.
func SpoofTest() {
	tui.Clear()
	tui.Title("IP Spoofing Tester")

	if runtime.GOOS != "linux" {
		tui.Error("The spoof tester needs raw sockets and only runs on Linux.")
		tui.PressEnter()
		return
	}
	tui.Warn("Finds which forged source IPs traverse the firewall between two nodes.")
	tui.Warn("Run the RECEIVER on one node and the SENDER on the other, then swap to")
	tui.Warn("map the other direction. Needs root (CAP_NET_RAW) on the sender.")
	fmt.Println()

	switch tui.ChooseOpt("Role", []tui.Option{
		{Title: "Receiver", Desc: "listen and tally which forged sources arrive (run this first)"},
		{Title: "Sender", Desc: "emit probes from a list/range/CIDR of forged sources"},
	}) {
	case 0:
		spoofTestReceiver()
	case 1:
		spoofTestSender()
	default:
		return
	}
}

func spoofTestReceiver() {
	token := tui.PromptDefault("Shared token (must match the sender)", "backpack")
	port := tui.PromptInt("Listen UDP port", 45000)
	attempts := tui.PromptInt("Probes the sender emits per IP", 5)
	windowSec := tui.PromptInt("Capture window (seconds)", 30)
	maxLoss := tui.PromptInt("Report IPs with loss%% at or below", 20)
	outFile := strings.TrimSpace(tui.PromptDefault("Write passing IPs to file (optional)", ""))

	tui.Info(fmt.Sprintf("Listening on udp/%d for %ds…", port, windowSec))
	results, err := spooftest.RunReceiver(spooftest.ReceiverConfig{
		Token:    token,
		Port:     uint16(port),
		Attempts: attempts,
		Window:   time.Duration(windowSec) * time.Second,
	})
	if err != nil {
		tui.Error(err.Error())
		tui.PressEnter()
		return
	}
	if len(results) == 0 {
		tui.Warn("No probes arrived. Check the sender ran, the token/port match, and")
		tui.Warn("that this node's real IP is what the sender targeted.")
		tui.PressEnter()
		return
	}

	fmt.Println()
	fmt.Printf("  %-18s %-10s %s\n", "SPOOF_IP", "ARRIVED", "LOSS%")
	for _, r := range results {
		fmt.Printf("  %-18s %d/%-8d %.1f\n", r.IP, r.Arrived, r.Attempts, r.LossPercent())
	}

	passing := spooftest.Passing(results, float64(maxLoss))
	fmt.Println()
	tui.Success(fmt.Sprintf("%d IP(s) passed at ≤ %d%% loss.", len(passing), maxLoss))
	if outFile != "" && len(passing) > 0 {
		if err := writePassingIPs(outFile, passing); err != nil {
			tui.Error("Could not write file: " + err.Error())
		} else {
			tui.Success("Wrote passing IPs to " + outFile)
		}
	}
	if len(passing) > 0 {
		tui.Info("Use these as spoof_src_pool on the SENDER's end of the tunnel.")
	}
	tui.PressEnter()
}

func spoofTestSender() {
	if os.Geteuid() != 0 {
		tui.Error("The sender needs root or CAP_NET_RAW to forge packet sources.")
		tui.PressEnter()
		return
	}
	token := tui.PromptDefault("Shared token (must match the receiver)", "backpack")

	var target net.IP
	for {
		raw := strings.TrimSpace(tui.Prompt("Receiver node's REAL IPv4: "))
		if target = net.ParseIP(raw); target != nil && target.To4() != nil {
			break
		}
		tui.Error("Enter a valid IPv4 address.")
	}
	port := tui.PromptInt("Receiver's listen UDP port", 45000)
	attempts := tui.PromptInt("Probes per candidate IP", 5)

	tui.Info("Candidate forged sources: single, range or CIDR, comma-separated,")
	tui.Info("or @file with one spec per line. e.g. 1.0.0.0/24,8.8.4.4,9.9.9.0-9.9.9.255")
	var ips []net.IP
	for {
		spec := strings.TrimSpace(tui.Prompt("Candidate IPs: "))
		expanded, err := spooftest.ExpandList(spec)
		if err != nil {
			tui.Error(err.Error())
			continue
		}
		if len(expanded) == 0 {
			tui.Error("No IPs to test.")
			continue
		}
		ips = expanded
		break
	}
	iface := strings.TrimSpace(tui.PromptDefault("Send interface (optional)", ""))

	tui.Info(fmt.Sprintf("Sending %d probes to %d candidate IP(s)… start the receiver first.", attempts, len(ips)))
	err := spooftest.RunSender(spooftest.SenderConfig{
		Iface:    iface,
		Token:    token,
		TargetIP: target,
		DstPort:  uint16(port),
		Attempts: attempts,
		Delay:    5 * time.Millisecond,
		IPs:      ips,
		Progress: func(done, total int) {
			if done == total || done%64 == 0 {
				fmt.Printf("\r  sent %d/%d", done, total)
			}
		},
	})
	fmt.Println()
	if err != nil {
		tui.Error(err.Error())
	} else {
		tui.Success("Done. Read the results on the receiver node.")
	}
	tui.PressEnter()
}

func writePassingIPs(path string, ips []net.IP) error {
	var b strings.Builder
	for _, ip := range ips {
		b.WriteString(ip.String())
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

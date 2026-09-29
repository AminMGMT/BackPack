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
	tui.Warn("Receiver On One Server, Sender On The Other; Swap For The Other Direction.")
	fmt.Println()

	switch tui.ChooseOpt("Role", []tui.Option{
		{Title: "Receiver", Desc: "start this first"},
		{Title: "Sender", Desc: "needs root"},
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
	token := tui.PromptDefault("Shared Token (Same On Both)", "backpack")
	port := tui.PromptInt("UDP Port", 45000)
	attempts := tui.PromptInt("Probes Per IP (Same On Both)", 5)
	windowSec := tui.PromptInt("Listen For (Seconds)", 30)
	maxLoss := tui.PromptInt("Pass At Loss % Or Below", 20)
	outFile := strings.TrimSpace(tui.PromptDefault("Save Passing IPs To File (Optional)", ""))

	tui.Info(fmt.Sprintf("Listening On udp/%d For %ds…", port, windowSec))
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
		tui.Warn("Nothing Arrived — Check The Sender, Token, Port And Target IP.")
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
	tui.Success(fmt.Sprintf("%d IP(s) Passed At ≤ %d%% Loss.", len(passing), maxLoss))
	if outFile != "" && len(passing) > 0 {
		if err := writePassingIPs(outFile, passing); err != nil {
			tui.Error("Could not write file: " + err.Error())
		} else {
			tui.Success("Saved To " + outFile)
		}
	}
	if len(passing) > 0 {
		tui.Info("Use These As The Forged Source On The Sender's Side.")
	}
	tui.PressEnter()
}

func spoofTestSender() {
	if os.Geteuid() != 0 {
		tui.Error("The sender needs root or CAP_NET_RAW to forge packet sources.")
		tui.PressEnter()
		return
	}
	token := tui.PromptDefault("Shared Token (Same On Both)", "backpack")

	var target net.IP
	for {
		raw := strings.TrimSpace(tui.Prompt("Receiver Real IPv4: "))
		if target = net.ParseIP(raw); target != nil && target.To4() != nil {
			break
		}
		tui.Error("Enter a valid IPv4 address.")
		tui.StopIfInputGone()
	}
	port := tui.PromptInt("UDP Port", 45000)
	attempts := tui.PromptInt("Probes Per IP (Same On Both)", 5)

	tui.Info("IPs, Ranges Or CIDRs, Comma Separated, Or @file — e.g. 1.0.0.0/24,8.8.4.4")
	var ips []net.IP
	for {
		spec := strings.TrimSpace(tui.Prompt("Forged Sources: "))
		expanded, err := spooftest.ExpandList(spec)
		if err != nil {
			tui.Error(err.Error())
			tui.StopIfInputGone()
			continue
		}
		if len(expanded) == 0 {
			tui.Error("No IPs to test.")
			tui.StopIfInputGone()
			continue
		}
		ips = expanded
		break
	}
	iface := strings.TrimSpace(tui.PromptDefault("Interface (Optional)", ""))

	tui.Info(fmt.Sprintf("Sending %d Probes From %d IP(s)…", attempts, len(ips)))
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
		tui.Success("Done — Read The Results On The Receiver.")
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

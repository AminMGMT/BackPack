package manage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/tunnel/naive"
)

type visionSetupProbe func(context.Context, string, string, string) error

// These are suggestions, never assumed to work on the operator's route.
var visionCoverSuggestions = []string{"www.microsoft.com:443", "www.bing.com:443", "www.apple.com:443"}

type visionCoverResult struct {
	target string
	delay  time.Duration
	err    error
}

func probeVisionSetup(ctx context.Context, target, sni, binary string) error {
	log, err := os.CreateTemp("", "backpack-vision-check-*.log")
	if err != nil {
		return err
	}
	defer os.Remove(log.Name())
	defer log.Close()
	ctx = naive.WithHelperOutput(ctx, log)
	if err := probeVisionTLS(ctx, target, sni, nil); err != nil {
		return err
	}
	return probeVisionTransport(ctx, target, sni, binary)
}

// Bounded parallel checks give a menu of authenticated covers, including the
// failure reasons. They create only temporary loopback helper listeners.
func scanVisionCovers(ctx context.Context, targets []string, binary string, probe visionSetupProbe) []visionCoverResult {
	results := make([]visionCoverResult, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()
			host, _, err := net.SplitHostPort(target)
			start := time.Now()
			if err == nil {
				attempt, cancel := context.WithTimeout(ctx, connTestCoverWait)
				err = probe(attempt, target, host, binary)
				if err == nil {
					err = attempt.Err()
				}
				cancel()
			}
			results[i] = visionCoverResult{target: target, delay: time.Since(start), err: err}
		}(i, target)
	}
	wg.Wait()
	// Preserve the suggestion order: an Apple endpoint is an explicit fallback,
	// not the recommendation merely because it answered a few ms faster.
	sort.SliceStable(results, func(i, j int) bool { return results[i].err == nil && results[j].err != nil })
	return results
}

func visionPublicPort() string {
	if !portHeld("0.0.0.0:443") {
		return "443"
	}
	for _, p := range []string{"8443", "2053", "2083"} {
		if !portHeld("0.0.0.0:" + p) {
			return p
		}
	}
	return strconv.Itoa(ctPickPort(map[int]bool{}, false))
}

func visionInternalPort(public string, ports []string) string {
	used := map[int]bool{}
	if p, err := strconv.Atoi(addrPort(public)); err == nil {
		used[p] = true
	}
	if !used[3080] && !portHeld("127.0.0.1:3080") && len(reverseBusyPorts(ports, "127.0.0.1:3080", "tcp")) == 0 {
		return "3080"
	}
	for tries := 0; tries < 500; tries++ {
		p := ctPickPort(used, false)
		if p == 0 {
			break
		}
		if len(reverseBusyPorts(ports, net.JoinHostPort("127.0.0.1", strconv.Itoa(p)), "tcp")) == 0 {
			return strconv.Itoa(p)
		}
	}
	return "" // leave the field editable; validation refuses an empty port
}

func visionBinary(current string) string {
	if current != "" {
		return current
	}
	for _, candidate := range []string{managedHelperPath("xray"), "/usr/local/lib/backpack/xray", "/usr/local/bin/xray"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return candidate
		}
	}
	if path, err := exec.LookPath("xray"); err == nil {
		return path
	}
	return managedHelperPath("xray")
}

func manualVisionCover(target, sni, binary string, probe visionSetupProbe) (string, string, bool) {
	base, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		previousTarget := target
		target = tui.PromptDefault("Cover Site (Host:Port)", defaultString(target, visionCoverSuggestions[0]))
		if target != previousTarget {
			sni = ""
		}
		host, port, err := net.SplitHostPort(target)
		if err != nil || host == "" || !validPort(port) {
			tui.Error("Use A Site And Port, Such As www.microsoft.com:443.")
			tui.StopIfInputGone()
			continue
		}
		sni = tui.PromptDefault("SNI", defaultString(sni, host))
		tui.Info("Checking The Site From This Server...")
		ctx, cancel := context.WithTimeout(base, connTestCoverWait)
		err = probe(ctx, target, sni, binary)
		if err == nil {
			err = ctx.Err()
		}
		cancel()
		if err == nil {
			return target, sni, true
		}
		if base.Err() != nil {
			tui.Warn("Cover Check Cancelled.")
			return "", "", false
		}
		tui.Error("This Site Did Not Pass: " + err.Error())
		if !tui.Confirm("Try Another Site", true) {
			return "", "", false
		}
	}
}

func selectVisionCover(current config.XrayServerConfig, binary string, probe visionSetupProbe) (string, string, bool) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if current.Vision() && current.Target != "" && current.ServerName != "" {
		switch tui.ChooseOptDefault("Cover Site", []tui.Option{
			{Title: "Keep The Current Site", Desc: current.Target + " · SNI " + current.ServerName},
			{Title: "Check The Suggested Sites", Desc: "pick one that works from this server"},
			{Title: "Another Site", Desc: "type a site and check it"},
		}, 0) {
		case 0:
			return current.Target, current.ServerName, true
		case 1:
		case 2:
			return manualVisionCover(current.Target, current.ServerName, binary, probe)
		default:
			return "", "", false
		}
	}
	for {
		tui.Title("Checking Cover Sites")
		tui.Info("Vision Shows A Real Site's TLS To Anyone Looking. Checking Which Sites Answer From Here...")
		results := scanVisionCovers(ctx, visionCoverSuggestions, binary, probe)
		if ctx.Err() != nil {
			tui.Warn("Cover Check Cancelled.")
			return "", "", false
		}
		var options []tui.Option
		var targets []string
		for _, result := range results {
			if result.err != nil {
				tui.Warn(result.target + " · Did Not Pass: " + result.err.Error())
				continue
			}
			options = append(options, tui.Option{Title: result.target, Desc: fmt.Sprintf("works · %d ms", result.delay.Milliseconds())})
			targets = append(targets, result.target)
		}
		options = append(options, tui.Option{Title: "Another Site", Desc: "type a site and check it"}, tui.Option{Title: "Check Again", Desc: "retry the suggested sites"})
		if len(targets) == 0 {
			tui.Error("No Suggested Site Passed. Try Another Site Or Check Again.")
		}
		choice := tui.ChooseOptDefault("Cover Site", options, 0)
		if choice < 0 {
			return "", "", false
		}
		if choice < len(targets) {
			target := targets[choice]
			host, _, _ := net.SplitHostPort(target)
			return target, host, true
		}
		if choice == len(targets) {
			return manualVisionCover(current.Target, "", binary, probe)
		}
	}
}

// visionHelperFor is the Xray helper Vision runs: the installed one, or
// the pinned one installed now. Its path is asked for only when neither works.
func visionHelperFor(current string) (string, bool) {
	binary := helperFor("xray", current)
	for !executable(binary) {
		tui.Error("The Xray Helper Is Not Installed. Enter Its Path.")
		binary = strings.TrimSpace(tui.PromptDefault("Xray Binary", binary))
		tui.StopIfInputGone()
		if !executable(binary) && !tui.Confirm("Try Another Path", true) {
			return "", false
		}
	}
	return binary, true
}

// configureVision is Vision's own part of the wizard. A new Iran end asks
// one thing — which checked cover site to borrow — and makes the rest: the
// helper is found, the UUID, key pair and Short ID generated. The Kharej end
// takes all of it from the setup link; typed by hand, it asks for the four
// values the link would carry.
func configureVision(n *TunnelSpec, old TunnelSpec, public string, editing bool, probe visionSetupProbe) bool {
	tui.Title("Vision")
	if n.Role == "client" {
		binary, ok := visionHelperFor(old.XrayClient.Binary)
		if !ok {
			return false
		}
		tui.Info("The Iran Setup Link Fills These In. Typed By Hand, They Must Match Iran.")
		x := old.XrayClient
		x.Mode, x.Server, x.Binary = config.XrayModeVision, public, binary
		x.Path, x.Host, x.CAFile = "", "", ""
		x.UUID = tui.PromptDefault("UUID (From Iran)", x.UUID)
		x.ServerName = tui.PromptDefault("Cover Site (From Iran)", x.ServerName)
		x.ShortID = tui.PromptDefault("Short ID (From Iran)", x.ShortID)
		x.PublicKey = tui.PromptDefault("Public Key (From Iran)", x.PublicKey)
		n.XrayClient = x
		return true
	}
	binary, ok := visionHelperFor(old.XrayServer.Binary)
	if !ok {
		return false
	}
	x := old.XrayServer
	x.Mode, x.Listen, x.Binary = config.XrayModeVision, public, binary
	x.Path, x.Host, x.Certificate, x.Key = "", "", "", ""
	x.Target, x.ServerName, ok = selectVisionCover(old.XrayServer, x.Binary, probe)
	if !ok {
		return false
	}
	var err error
	if x.UUID == "" {
		x.UUID, err = managedUUID()
	}
	var publicKey string
	if err == nil {
		x.PrivateKey, publicKey, err = managedVisionKey(x.PrivateKey)
	}
	if err != nil {
		tui.Error(err.Error())
		return false
	}
	if x.ShortID == "" {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			tui.Error(err.Error())
			return false
		}
		x.ShortID = hex.EncodeToString(raw[:])
	}
	// A new tunnel's identity is generated and goes to the Kharej in the
	// setup link; only an existing one is offered the change.
	if editing && tui.Confirm("Change The Identity (Token, UUID, Key, Short ID)", false) {
		n.Token = managedSecret("Security Token", n.Token, "")
		x.UUID = tui.PromptDefault("UUID", x.UUID)
		x.ShortID = tui.PromptDefault("Short ID", x.ShortID)
		key := tui.Prompt("Private Key (Enter = Keep, generate = New Key): ")
		if key != "" {
			if key == "generate" {
				key = ""
			}
			x.PrivateKey, publicKey, err = managedVisionKey(key)
			if err != nil {
				tui.Error(err.Error())
				return false
			}
		}
	}
	if x.Target == "" || x.ServerName == "" {
		tui.Error("A Checked Cover Site Is Required.")
		return false
	}
	tui.Success("Identity Ready — The Setup Link Carries It To The Kharej.")
	tui.Info("Public Key (Only For A Kharej Set Up By Hand): " + publicKey)
	n.XrayServer = x
	return true
}

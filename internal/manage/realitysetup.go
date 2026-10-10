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
	"sync"
	"syscall"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tui"
	"github.com/backpack/backpack/internal/tunnel/naive"
)

type realitySetupProbe func(context.Context, string, string, string) error

// These are suggestions, never assumed to work on the operator's route.
var realityCoverSuggestions = []string{"www.microsoft.com:443", "www.bing.com:443", "www.apple.com:443"}

type realityCoverResult struct {
	target string
	delay  time.Duration
	err    error
}

func probeRealitySetup(ctx context.Context, target, sni, binary string) error {
	log, err := os.CreateTemp("", "backpack-reality-check-*.log")
	if err != nil {
		return err
	}
	defer os.Remove(log.Name())
	defer log.Close()
	ctx = naive.WithHelperOutput(ctx, log)
	if err := probeRealityTLS(ctx, target, sni, nil); err != nil {
		return err
	}
	return probeRealityTransport(ctx, target, sni, binary)
}

// Bounded parallel checks give a menu of authenticated covers, including the
// failure reasons. They create only temporary loopback helper listeners.
func scanRealityCovers(ctx context.Context, targets []string, binary string, probe realitySetupProbe) []realityCoverResult {
	results := make([]realityCoverResult, len(targets))
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
			results[i] = realityCoverResult{target: target, delay: time.Since(start), err: err}
		}(i, target)
	}
	wg.Wait()
	// Preserve the suggestion order: an Apple endpoint is an explicit fallback,
	// not the recommendation merely because it answered a few ms faster.
	sort.SliceStable(results, func(i, j int) bool { return results[i].err == nil && results[j].err != nil })
	return results
}

func realityPublicPort() string {
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

func realityInternalPort(public string, ports []string) string {
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

func realityBinary(current string) string {
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

func manualRealityCover(target, sni, binary string, probe realitySetupProbe) (string, string, bool) {
	base, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		previousTarget := target
		target = tui.PromptDefault("Cover endpoint (hostname:port)", defaultString(target, realityCoverSuggestions[0]))
		if target != previousTarget {
			sni = ""
		}
		host, port, err := net.SplitHostPort(target)
		if err != nil || host == "" || !validPort(port) {
			tui.Error("Use a cover hostname and port, such as www.microsoft.com:443.")
			tui.StopIfInputGone()
			continue
		}
		sni = tui.PromptDefault("SNI (cover hostname)", defaultString(sni, host))
		tui.Info("Checking TLS and an authenticated REALITY connection...")
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
			tui.Warn("Cover check cancelled.")
			return "", "", false
		}
		tui.Error("Cover check failed: " + err.Error())
		if !tui.Confirm("Try another cover", true) {
			return "", "", false
		}
	}
}

func selectRealityCover(current config.XrayServerConfig, binary string, probe realitySetupProbe) (string, string, bool) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if current.Mode == "reality" && current.Target != "" && current.ServerName != "" {
		switch tui.ChooseOptDefault("REALITY cover", []tui.Option{
			{Title: "Keep current cover", Desc: current.Target + " · SNI " + current.ServerName},
			{Title: "Test suggested covers", Desc: "choose from endpoints that work from this server"},
			{Title: "Custom endpoint and SNI", Desc: "test your own cover before saving"},
		}, 0) {
		case 0:
			return current.Target, current.ServerName, true
		case 1:
		case 2:
			return manualRealityCover(current.Target, current.ServerName, binary, probe)
		default:
			return "", "", false
		}
	}
	for {
		tui.Title("Checking REALITY covers")
		tui.Warn("Testing TLS 1.3, HTTP/2 and authenticated data; this checks the cover, not the Iran/Kharej path.")
		results := scanRealityCovers(ctx, realityCoverSuggestions, binary, probe)
		if ctx.Err() != nil {
			tui.Warn("Cover check cancelled.")
			return "", "", false
		}
		var options []tui.Option
		var targets []string
		for _, result := range results {
			if result.err != nil {
				tui.Warn(result.target + " · unavailable: " + result.err.Error())
				continue
			}
			options = append(options, tui.Option{Title: result.target, Desc: fmt.Sprintf("verified · %d ms · SNI set automatically", result.delay.Milliseconds())})
			targets = append(targets, result.target)
		}
		options = append(options, tui.Option{Title: "Custom endpoint and SNI", Desc: "enter and test your own cover"}, tui.Option{Title: "Test again", Desc: "retry the suggested endpoints"})
		if len(targets) == 0 {
			tui.Error("No suggested cover passed. Try a custom endpoint or test again.")
		}
		choice := tui.ChooseOptDefault("Select a REALITY cover", options, 0)
		if choice < 0 {
			return "", "", false
		}
		if choice < len(targets) {
			target := targets[choice]
			host, _, _ := net.SplitHostPort(target)
			return target, host, true
		}
		if choice == len(targets) {
			return manualRealityCover(current.Target, "", binary, probe)
		}
	}
}

func configureReality(n *TunnelSpec, old TunnelSpec, public string, probe realitySetupProbe) bool {
	tui.Title("REALITY / Vision")
	tui.Warn("Space/Enter keeps each suggestion. Type a replacement and Enter to change it.")
	if n.Role == "client" {
		tui.Info("The Iran setup link fills UUID, SNI, Short ID and public key automatically. Manual values must match Iran.")
		x := old.XrayClient
		x.Mode, x.Server = "reality", public
		x.Path, x.Host, x.CAFile = "", "", ""
		x.Binary = tui.PromptDefault("Xray binary", realityBinary(x.Binary))
		x.UUID = tui.PromptDefault("UUID (from Iran)", x.UUID)
		x.ServerName = tui.PromptDefault("SNI (from Iran)", x.ServerName)
		x.ShortID = tui.PromptDefault("Short ID (from Iran)", x.ShortID)
		x.PublicKey = tui.PromptDefault("Public key (from Iran)", x.PublicKey)
		n.XrayClient = x
		return true
	}
	x := old.XrayServer
	x.Mode, x.Listen = "reality", public
	x.Path, x.Host, x.Certificate, x.Key = "", "", "", ""
	x.Binary = tui.PromptDefault("Xray binary", realityBinary(x.Binary))
	info, err := os.Stat(x.Binary)
	if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
		tui.Error("Xray is not executable at this path. Install the Xray helper or enter its installed path.")
		return false
	}
	var ok bool
	x.Target, x.ServerName, ok = selectRealityCover(old.XrayServer, x.Binary, probe)
	if !ok {
		return false
	}
	if x.UUID == "" {
		x.UUID, err = managedUUID()
	}
	if err == nil {
		x.PrivateKey, _, err = managedRealityKey(x.PrivateKey)
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
	tui.Info("Security token, UUID, key pair and Short ID are ready. The Kharej setup link carries the matching public settings.")
	if tui.Confirm("Change generated identity settings", false) {
		n.Token = managedSecret("Security token", n.Token, "")
		x.UUID = tui.PromptDefault("UUID", x.UUID)
		x.ShortID = tui.PromptDefault("Short ID", x.ShortID)
		key := tui.Prompt("Private key (Enter = keep, generate = new key): ")
		if key != "" {
			if key == "generate" {
				key = ""
			}
			x.PrivateKey, _, err = managedRealityKey(key)
			if err != nil {
				tui.Error(err.Error())
				return false
			}
		}
	}
	if x.Target == "" || x.ServerName == "" {
		tui.Error("A verified cover is required.")
		return false
	}
	n.XrayServer = x
	return true
}

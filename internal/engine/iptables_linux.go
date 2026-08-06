//go:build linux

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/instanceid"
	"golang.org/x/sys/unix"
)

type iptablesProvider struct{}

func init()                                 { Register(config.EngineIPTables, iptablesProvider{}) }
func (iptablesProvider) Metadata() Metadata { return Metadata{Name: "iptables", Mode: "direct"} }

type familyTools struct {
	family             string
	cmd, save, restore string
	backend            string
}

type expandedRule struct {
	family, proto, listen, target string
	listenPort, targetPort        uint16
}

type generation struct {
	num    uint64
	key    string
	chains map[string]map[string]string // family -> N/F/P -> chain
}

type createdChain struct {
	family, purpose, name string
}

type commandRunner interface {
	Combined(context.Context, string, ...string) ([]byte, error)
}
type osRunner struct{}

func (osRunner) Combined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

var nfRunner commandRunner = osRunner{}

const netfilterLockPath = "/run/backpack/netfilter.lock"

// RemoveRuntimeArtifacts removes the global netfilter lock after a full
// uninstall. Callers must stop all Backpack services and clean every owned
// instance first; normal instance stop/delete deliberately leaves this shared
// lock in place for the other instances.
func RemoveRuntimeArtifacts() error {
	if err := os.Remove(netfilterLockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove netfilter lock: %w", err)
	}
	if err := os.Remove(filepath.Dir(netfilterLockPath)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove netfilter runtime directory: %w", err)
	}
	return nil
}

func withNetfilterLock(fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(netfilterLockPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(netfilterLockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return fn()
}

func neededFamilies(cfg *config.Config) []string {
	set := map[string]bool{}
	for _, m := range cfg.Forward.Mappings {
		ip := net.ParseIP(m.ListenAddress)
		if ip != nil && ip.To4() != nil {
			set["ipv4"] = true
		} else {
			set["ipv6"] = true
		}
	}
	var out []string
	for _, f := range []string{"ipv4", "ipv6"} {
		if set[f] {
			out = append(out, f)
		}
	}
	return out
}

func detectTools(ctx context.Context, cfg *config.Config) (map[string]familyTools, error) {
	out := map[string]familyTools{}
	for _, family := range neededFamilies(cfg) {
		prefix := "iptables"
		if family == "ipv6" {
			prefix = "ip6tables"
		}
		t := familyTools{family: family, cmd: prefix, save: prefix + "-save", restore: prefix + "-restore"}
		for _, bin := range []string{t.cmd, t.save, t.restore} {
			if _, err := exec.LookPath(bin); err != nil {
				return nil, fmt.Errorf("%s forward requires %s: %w", family, bin, err)
			}
		}
		versions := map[string]bool{}
		for _, bin := range []string{t.cmd, t.save, t.restore} {
			b, err := nfRunner.Combined(ctx, bin, "--version")
			if err != nil {
				return nil, fmt.Errorf("cannot run %s --version: %s", bin, strings.TrimSpace(string(b)))
			}
			v := strings.ToLower(string(b))
			backend := "legacy"
			if strings.Contains(v, "nf_tables") {
				backend = "nft"
			}
			versions[backend] = true
		}
		if len(versions) != 1 {
			return nil, fmt.Errorf("%s command/save/restore tools use mixed backends", family)
		}
		for v := range versions {
			t.backend = v
		}
		if t.backend == "nft" {
			if _, err := exec.LookPath("nft"); err != nil {
				return nil, fmt.Errorf("iptables-nft conflict inspection requires the nft command")
			}
		}
		out[family] = t
	}
	return out, nil
}

func augmentOptionalTools(ctx context.Context, tools map[string]familyTools) map[string]familyTools {
	out := map[string]familyTools{}
	for k, v := range tools {
		out[k] = v
	}
	for _, family := range []string{"ipv4", "ipv6"} {
		if _, exists := out[family]; exists {
			continue
		}
		listen, target := "0.0.0.0", "192.0.2.1"
		if family == "ipv6" {
			listen, target = "::", "2001:db8::1"
		}
		dummy := &config.Config{Engine: config.EngineIPTables, Forward: config.ForwardConfig{Mappings: []config.ForwardMapping{{ListenAddress: listen, ListenPorts: "1", TargetAddress: target, TargetPorts: "1", Protocols: []string{"tcp"}}}}}
		if detected, err := detectTools(ctx, dummy); err == nil {
			for k, v := range detected {
				out[k] = v
			}
		}
	}
	return out
}

func checkCapabilities(ctx context.Context, tools map[string]familyTools) error {
	for _, t := range tools {
		for _, args := range [][]string{{"-m", "conntrack", "-h"}, {"-m", "comment", "-h"}, {"-j", "CONNMARK", "-h"}, {"-t", "nat", "-j", "DNAT", "-h"}, {"-t", "nat", "-j", "MASQUERADE", "-h"}} {
			b, err := nfRunner.Combined(ctx, t.cmd, args...)
			if err != nil {
				return fmt.Errorf("%s backend lacks %s: %s", t.family, strings.Join(args, " "), strings.TrimSpace(string(b)))
			}
		}
	}
	return nil
}

func expand(cfg *config.Config) ([]expandedRule, error) {
	var out []expandedRule
	for _, m := range cfg.Forward.Mappings {
		lr, tr, err := m.Ranges()
		if err != nil {
			return nil, err
		}
		family := "ipv6"
		if net.ParseIP(m.ListenAddress).To4() != nil {
			family = "ipv4"
		}
		for _, raw := range m.Protocols {
			p := strings.ToLower(strings.TrimSpace(raw))
			for n := 0; n < lr.Len(); n++ {
				out = append(out, expandedRule{family: family, proto: p, listen: net.ParseIP(m.ListenAddress).String(), target: net.ParseIP(m.TargetAddress).String(), listenPort: lr.Start + uint16(n), targetPort: tr.Start + uint16(n)})
			}
		}
	}
	return out, nil
}

func desiredHash(cfg *config.Config) string {
	b, _ := json.Marshal(cfg.Forward)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func genKey(n uint64) (string, error) {
	const max = 36*36*36*36 - 1
	if n > max {
		return "", fmt.Errorf("netfilter generation limit reached")
	}
	s := strings.ToLower(strconv.FormatUint(n, 36))
	return strings.Repeat("0", 4-len(s)) + s, nil
}

func makeGeneration(id instanceid.Identity, n uint64, families []string) (generation, error) {
	key, err := genKey(n)
	if err != nil {
		return generation{}, err
	}
	g := generation{num: n, key: key, chains: map[string]map[string]string{}}
	h := instanceid.Hash80(id.InstanceID)
	for _, f := range families {
		digit := "4"
		if f == "ipv6" {
			digit = "6"
		}
		g.chains[f] = map[string]string{}
		for _, p := range []string{"N", "F", "P"} {
			g.chains[f][p] = "B" + digit + p + h + key
		}
	}
	return g, nil
}

func comment(id instanceid.Identity, purpose, gen string) string {
	return "backpack:" + id.InstanceID + ":" + purpose + ":" + gen
}
func markText(mark uint32) string { return fmt.Sprintf("0x%x/0xffffffff", mark) }

func runNF(ctx context.Context, bin string, args ...string) error {
	b, err := nfRunner.Combined(ctx, bin, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, strings.TrimSpace(string(b)))
	}
	return nil
}

func appendRule(ctx context.Context, t familyTools, table, chain string, args ...string) error {
	a := []string{"-w", "5", "-t", table, "-A", chain}
	a = append(a, args...)
	return runNF(ctx, t.cmd, a...)
}

func targetArg(r expandedRule) string {
	if r.family == "ipv6" {
		return fmt.Sprintf("[%s]:%d", r.target, r.targetPort)
	}
	return fmt.Sprintf("%s:%d", r.target, r.targetPort)
}

func buildDetached(ctx context.Context, id instanceid.Identity, g generation, tools map[string]familyTools, rules []expandedRule) error {
	var created []createdChain
	rollbackCreated := func() {
		for i := len(created) - 1; i >= 0; i-- {
			item := created[i]
			table := "nat"
			if item.purpose == "F" {
				table = "filter"
			}
			t := tools[item.family]
			_ = runNF(ctx, t.cmd, "-w", "5", "-t", table, "-F", item.name)
			_ = runNF(ctx, t.cmd, "-w", "5", "-t", table, "-X", item.name)
		}
	}
	for family, chains := range g.chains {
		t := tools[family]
		for purpose, chain := range chains {
			table := "nat"
			if purpose == "F" {
				table = "filter"
			}
			if err := runNF(ctx, t.cmd, "-w", "5", "-t", table, "-N", chain); err != nil {
				rollbackCreated()
				return err
			}
			created = append(created, createdChain{family: family, purpose: purpose, name: chain})
		}
	}
	for _, r := range rules {
		t, c := tools[r.family], g.chains[r.family]
		base := []string{"-p", r.proto}
		if !net.ParseIP(r.listen).IsUnspecified() {
			base = append(base, "-d", r.listen)
		}
		base = append(base, "--dport", strconv.Itoa(int(r.listenPort)), "-m", "conntrack", "--ctstate", "NEW")
		markRule := append(append([]string{}, base...), "-m", "comment", "--comment", comment(id, "mark", g.key), "-j", "CONNMARK", "--set-xmark", markText(id.Connmark))
		if err := appendRule(ctx, t, "nat", c["N"], markRule...); err != nil {
			rollbackCreated()
			return err
		}
		dnat := append(append([]string{}, base...), "-m", "connmark", "--mark", markText(id.Connmark), "-m", "comment", "--comment", comment(id, "dnat", g.key), "-j", "DNAT", "--to-destination", targetArg(r))
		if err := appendRule(ctx, t, "nat", c["N"], dnat...); err != nil {
			rollbackCreated()
			return err
		}

		rx := []string{"-m", "connmark", "--mark", markText(id.Connmark), "-p", r.proto, "-d", r.target, "--dport", strconv.Itoa(int(r.targetPort)), "-m", "conntrack", "--ctstate", "NEW,ESTABLISHED,RELATED", "-m", "comment", "--comment", comment(id, "acct-rx", g.key), "-j", "ACCEPT"}
		if err := appendRule(ctx, t, "filter", c["F"], rx...); err != nil {
			rollbackCreated()
			return err
		}
		tx := []string{"-m", "connmark", "--mark", markText(id.Connmark), "-p", r.proto, "-s", r.target, "--sport", strconv.Itoa(int(r.targetPort)), "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-m", "comment", "--comment", comment(id, "acct-tx", g.key), "-j", "ACCEPT"}
		if err := appendRule(ctx, t, "filter", c["F"], tx...); err != nil {
			rollbackCreated()
			return err
		}
		masq := []string{"-m", "connmark", "--mark", markText(id.Connmark), "-p", r.proto, "-d", r.target, "--dport", strconv.Itoa(int(r.targetPort)), "-m", "comment", "--comment", comment(id, "masquerade", g.key), "-j", "MASQUERADE"}
		if err := appendRule(ctx, t, "nat", c["P"], masq...); err != nil {
			rollbackCreated()
			return err
		}
	}
	return nil
}

func installHooks(ctx context.Context, id instanceid.Identity, g generation, tools map[string]familyTools) error {
	for family, c := range g.chains {
		t := tools[family]
		if err := runNF(ctx, t.cmd, "-w", "5", "-t", "nat", "-I", "POSTROUTING", "1", "-m", "connmark", "--mark", markText(id.Connmark), "-m", "comment", "--comment", comment(id, "hook-postrouting", g.key), "-j", c["P"]); err != nil {
			return err
		}
		if err := runNF(ctx, t.cmd, "-w", "5", "-t", "filter", "-I", "FORWARD", "1", "-m", "connmark", "--mark", markText(id.Connmark), "-m", "comment", "--comment", comment(id, "hook-forward", g.key), "-j", c["F"]); err != nil {
			return err
		}
		// PREROUTING is the ingress gate and is deliberately installed last.
		if err := runNF(ctx, t.cmd, "-w", "5", "-t", "nat", "-I", "PREROUTING", "1", "-m", "comment", "--comment", comment(id, "hook-prerouting", g.key), "-j", c["N"]); err != nil {
			return err
		}
	}
	return nil
}

func saveTable(ctx context.Context, t familyTools, table string) (string, error) {
	b, err := nfRunner.Combined(ctx, t.save, "-c", "-t", table)
	if err != nil {
		return "", fmt.Errorf("%s -c -t %s: %w: %s", t.save, table, err, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}

func verifyGeneration(ctx context.Context, id instanceid.Identity, g generation, tools map[string]familyTools, rules []expandedRule, hooked bool) error {
	for family, c := range g.chains {
		t := tools[family]
		nat, err := saveTable(ctx, t, "nat")
		if err != nil {
			return err
		}
		filter, err := saveTable(ctx, t, "filter")
		if err != nil {
			return err
		}
		for _, ch := range []string{c["N"], c["P"]} {
			if !strings.Contains(nat, ":"+ch+" ") {
				return fmt.Errorf("verification failed: nat chain %s missing", ch)
			}
		}
		if !strings.Contains(filter, ":"+c["F"]+" ") {
			return fmt.Errorf("verification failed: filter chain %s missing", c["F"])
		}
		expected := 0
		for _, r := range rules {
			if r.family == family {
				expected++
			}
		}
		for purpose, source := range map[string]string{
			"mark": nat, "dnat": nat, "masquerade": nat, "acct-rx": filter, "acct-tx": filter,
		} {
			if got := strings.Count(source, comment(id, purpose, g.key)); got != expected {
				return fmt.Errorf("verification failed: generation %s %s has %d rules, want %d", g.key, purpose, got, expected)
			}
		}
		if hooked {
			for purpose, source := range map[string]string{"hook-prerouting": nat, "hook-postrouting": nat, "hook-forward": filter} {
				if got := strings.Count(source, comment(id, purpose, g.key)); got != 1 {
					return fmt.Errorf("verification failed: generation %s %s count is %d", g.key, purpose, got)
				}
			}
		}
	}
	return nil
}

func liveRulesHash(ctx context.Context, id instanceid.Identity, g generation, tools map[string]familyTools) (string, error) {
	var lines []string
	for family, t := range tools {
		owned := map[string]bool{}
		for _, ch := range g.chains[family] {
			owned[ch] = true
		}
		for _, table := range []string{"nat", "filter"} {
			raw, err := saveTable(ctx, t, table)
			if err != nil {
				return "", err
			}
			for _, line := range strings.Split(raw, "\n") {
				normal := line
				if i := strings.Index(normal, "] "); i >= 0 && strings.HasPrefix(normal, "[") {
					normal = normal[i+2:]
				}
				f := splitRule(normal)
				isOwnedChain := len(f) >= 2 && f[0] == "-A" && owned[f[1]]
				isHook := strings.Contains(normal, "backpack:"+id.InstanceID+":hook-") && strings.Contains(normal, ":"+g.key)
				if isOwnedChain || isHook {
					lines = append(lines, family+"/"+table+":"+normal)
				}
			}
		}
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

var counterLine = regexp.MustCompile(`^\[(\d+):(\d+)\].*--comment "?backpack:([^:\s"]+):(acct-rx|acct-tx):([^\s"]+)"?`)

func scrape(ctx context.Context, id instanceid.Identity, tools map[string]familyTools) (map[string]kernelCount, error) {
	out := map[string]kernelCount{}
	for _, t := range tools {
		s, err := saveTable(ctx, t, "filter")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(s, "\n") {
			m := counterLine.FindStringSubmatch(line)
			if len(m) != 6 || m[3] != id.InstanceID {
				continue
			}
			pk, _ := strconv.ParseUint(m[1], 10, 64)
			by, _ := strconv.ParseUint(m[2], 10, 64)
			c := out[m[5]]
			if m[4] == "acct-rx" {
				c.RXPackets += pk
				c.RXBytes += by
			} else {
				c.TXPackets += pk
				c.TXBytes += by
			}
			out[m[5]] = c
		}
	}
	return out, nil
}

func updateCountersLocked(ctx context.Context, r Request, id instanceid.Identity, tools map[string]familyTools, s *forwardState) error {
	return updateCountersLockedMode(ctx, r, id, tools, s, true)
}

func updateCountersLockedMode(ctx context.Context, r Request, id instanceid.Identity, tools map[string]familyTools, s *forwardState, includeSession bool) error {
	current, err := scrape(ctx, id, tools)
	if err != nil {
		return err
	}
	for gen, c := range current {
		prev := s.Last[gen]
		addDelta(&s.Cumulative, prev, c)
		if includeSession {
			addDelta(&s.Session, prev, c)
		}
		s.Last[gen] = c
	}
	if err := saveForwardState(r.ConfigPath, *s); err != nil {
		return err
	}
	return persistMetrics(r, *s)
}

func requireStateTools(s forwardState, tools map[string]familyTools) error {
	for _, family := range s.Families {
		if _, ok := tools[family]; !ok {
			name := "iptables"
			if family == "ipv6" {
				name = "ip6tables"
			}
			return fmt.Errorf("persisted generation uses %s but compatible %s command/save/restore tools are unavailable", family, name)
		}
	}
	return nil
}

// splitRule is sufficient for iptables-save output (quoted comments contain no
// spaces in Backpack ownership strings).
func splitRule(s string) []string { return strings.Fields(strings.ReplaceAll(s, "\"", "")) }

func deleteOwnedHooks(ctx context.Context, id instanceid.Identity, tools map[string]familyTools, purpose string) error {
	for _, t := range tools {
		for _, table := range []string{"nat", "filter"} {
			s, err := saveTable(ctx, t, table)
			if err != nil {
				return err
			}
			for _, line := range strings.Split(s, "\n") {
				if !strings.HasPrefix(line, "[") || !strings.Contains(line, "backpack:"+id.InstanceID+":"+purpose+":") {
					continue
				}
				i := strings.Index(line, "] ")
				if i < 0 {
					continue
				}
				args := splitRule(line[i+2:])
				if len(args) < 2 || args[0] != "-A" {
					continue
				}
				args[0] = "-D"
				cmd := append([]string{"-w", "5", "-t", table}, args...)
				if err := runNF(ctx, t.cmd, cmd...); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func deleteGenerationHooks(ctx context.Context, id instanceid.Identity, tools map[string]familyTools, purpose, gen string) error {
	needle := "backpack:" + id.InstanceID + ":" + purpose + ":" + gen
	for _, t := range tools {
		for _, table := range []string{"nat", "filter"} {
			s, err := saveTable(ctx, t, table)
			if err != nil {
				return err
			}
			for _, line := range strings.Split(s, "\n") {
				if !strings.HasPrefix(line, "[") || !strings.Contains(line, needle) {
					continue
				}
				i := strings.Index(line, "] ")
				if i < 0 {
					continue
				}
				args := splitRule(line[i+2:])
				if len(args) < 2 || args[0] != "-A" {
					continue
				}
				args[0] = "-D"
				cmd := append([]string{"-w", "5", "-t", table}, args...)
				if err := runNF(ctx, t.cmd, cmd...); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func deleteOtherGenerationHooks(ctx context.Context, id instanceid.Identity, tools map[string]familyTools, purpose, keepGen string) error {
	prefix := "backpack:" + id.InstanceID + ":" + purpose + ":"
	keep := prefix + keepGen
	for _, t := range tools {
		for _, table := range []string{"nat", "filter"} {
			s, err := saveTable(ctx, t, table)
			if err != nil {
				return err
			}
			for _, line := range strings.Split(s, "\n") {
				if !strings.HasPrefix(line, "[") || !strings.Contains(line, prefix) || strings.Contains(line, keep) {
					continue
				}
				i := strings.Index(line, "] ")
				if i < 0 {
					continue
				}
				args := splitRule(line[i+2:])
				if len(args) < 2 || args[0] != "-A" {
					continue
				}
				args[0] = "-D"
				cmd := append([]string{"-w", "5", "-t", table}, args...)
				if err := runNF(ctx, t.cmd, cmd...); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func ownedChains(ctx context.Context, id instanceid.Identity, tools map[string]familyTools) (map[string]map[string][]string, error) {
	out := map[string]map[string][]string{}
	prefixes := map[string]string{}
	h := instanceid.Hash80(id.InstanceID)
	for _, f := range []string{"ipv4", "ipv6"} {
		digit := "4"
		if f == "ipv6" {
			digit = "6"
		}
		for _, p := range []string{"N", "F", "P"} {
			prefixes["B"+digit+p+h] = p
		}
	}
	for family, t := range tools {
		out[family] = map[string][]string{"nat": {}, "filter": {}}
		for _, table := range []string{"nat", "filter"} {
			s, err := saveTable(ctx, t, table)
			if err != nil {
				return nil, err
			}
			candidates := map[string]bool{}
			valid, invalid := map[string]bool{}, map[string]bool{}
			for _, line := range strings.Split(s, "\n") {
				if !strings.HasPrefix(line, ":") {
					if strings.Contains(line, " -A ") || strings.HasPrefix(line, "-A ") {
						ruleText := line
						if i := strings.Index(ruleText, "] "); i >= 0 {
							ruleText = ruleText[i+2:]
						}
						f := splitRule(ruleText)
						if len(f) >= 2 && f[0] == "-A" && candidates[f[1]] {
							if strings.Contains(line, "backpack:"+id.InstanceID+":") {
								valid[f[1]] = true
							} else {
								invalid[f[1]] = true
							}
						}
					}
					continue
				}
				name := strings.Fields(strings.TrimPrefix(line, ":"))[0]
				for pre, p := range prefixes {
					if strings.HasPrefix(name, pre) {
						expected := "nat"
						if p == "F" {
							expected = "filter"
						}
						if table == expected {
							candidates[name] = true
						}
					}
				}
			}
			// A hash-shaped name alone is never sufficient ownership. At least one
			// rule must carry the full instance ID and no rule may be unowned.
			for name := range candidates {
				if valid[name] && !invalid[name] {
					out[family][table] = append(out[family][table], name)
				}
			}
		}
	}
	return out, nil
}

func removeOwnedChains(ctx context.Context, id instanceid.Identity, tools map[string]familyTools) error {
	chains, err := ownedChains(ctx, id, tools)
	if err != nil {
		return err
	}
	for family, tables := range chains {
		t := tools[family]
		for _, table := range []string{"filter", "nat"} {
			for _, ch := range tables[table] {
				_ = runNF(ctx, t.cmd, "-w", "5", "-t", table, "-F", ch)
				if err := runNF(ctx, t.cmd, "-w", "5", "-t", table, "-X", ch); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func removeOtherChains(ctx context.Context, id instanceid.Identity, tools map[string]familyTools, keep generation) error {
	chains, err := ownedChains(ctx, id, tools)
	if err != nil {
		return err
	}
	for family, tables := range chains {
		current := map[string]bool{}
		for _, ch := range keep.chains[family] {
			current[ch] = true
		}
		t := tools[family]
		for table, list := range tables {
			for _, ch := range list {
				if current[ch] {
					continue
				}
				_ = runNF(ctx, t.cmd, "-w", "5", "-t", table, "-F", ch)
				if err := runNF(ctx, t.cmd, "-w", "5", "-t", table, "-X", ch); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func reconcileCrashLeftovers(ctx context.Context, id instanceid.Identity, tools map[string]familyTools, s forwardState, families []string) error {
	if s.Generation == 0 {
		_ = deleteOwnedHooks(ctx, id, tools, "hook-prerouting")
		_ = deleteOwnedHooks(ctx, id, tools, "hook-forward")
		_ = deleteOwnedHooks(ctx, id, tools, "hook-postrouting")
		return removeOwnedChains(ctx, id, tools)
	}
	keep, err := makeGeneration(id, s.Generation, families)
	if err != nil {
		return err
	}
	if err = deleteOtherGenerationHooks(ctx, id, tools, "hook-prerouting", keep.key); err != nil {
		return err
	}
	if err = deleteOtherGenerationHooks(ctx, id, tools, "hook-forward", keep.key); err != nil {
		return err
	}
	if err = deleteOtherGenerationHooks(ctx, id, tools, "hook-postrouting", keep.key); err != nil {
		return err
	}
	return removeOtherChains(ctx, id, tools, keep)
}

func rollbackGeneration(ctx context.Context, id instanceid.Identity, g generation, tools map[string]familyTools) {
	_ = deleteGenerationHooks(ctx, id, tools, "hook-prerouting", g.key)
	_ = deleteGenerationHooks(ctx, id, tools, "hook-forward", g.key)
	_ = deleteGenerationHooks(ctx, id, tools, "hook-postrouting", g.key)
	for family, c := range g.chains {
		t := tools[family]
		for p, ch := range c {
			table := "nat"
			if p == "F" {
				table = "filter"
			}
			_ = runNF(ctx, t.cmd, "-w", "5", "-t", table, "-F", ch)
			_ = runNF(ctx, t.cmd, "-w", "5", "-t", table, "-X", ch)
		}
	}
}

func localAddressPresent(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil || ip.IsUnspecified() {
		return true
	}
	ifaces, _ := net.Interfaces()
	for _, ifi := range ifaces {
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			raw := a.String()
			if h, _, e := net.ParseCIDR(raw); e == nil && h.Equal(ip) {
				return true
			}
		}
	}
	return false
}

func overlapAddr(a, b net.IP) bool            { return a.IsUnspecified() || b.IsUnspecified() || a.Equal(b) }
func overlapRange(a, b config.PortRange) bool { return a.Start <= b.End && b.Start <= a.End }

func conflictConfigs(r Request) error {
	dir := filepath.Dir(r.ConfigPath)
	files, _ := filepath.Glob(filepath.Join(dir, "*.toml"))
	for _, p := range files {
		if filepath.Clean(p) == filepath.Clean(r.ConfigPath) {
			continue
		}
		other, err := config.LoadFile(p)
		if err != nil {
			return fmt.Errorf("cannot safely analyse Backpack config %s: %w", p, err)
		}
		if other.EffectiveEngine() != config.EngineIPTables {
			continue
		}
		for _, a := range r.Config.Forward.Mappings {
			ar, _, _ := a.Ranges()
			ai := net.ParseIP(a.ListenAddress)
			for _, b := range other.Forward.Mappings {
				br, _, _ := b.Ranges()
				bi := net.ParseIP(b.ListenAddress)
				if (ai.To4() != nil) != (bi.To4() != nil) || !overlapAddr(ai, bi) || !overlapRange(ar, br) {
					continue
				}
				for _, ap := range a.Protocols {
					for _, bp := range b.Protocols {
						if strings.EqualFold(ap, bp) {
							return fmt.Errorf("%s %s ports %s conflict with Backpack instance %s", map[bool]string{true: "IPv4", false: "IPv6"}[ai.To4() != nil], strings.ToLower(ap), a.ListenPorts, instanceid.Name(p))
						}
					}
				}
			}
		}
	}
	return nil
}

func parseHostPortLoose(raw string) (net.IP, config.PortRange, bool) {
	h, p, err := net.SplitHostPort(raw)
	if err != nil {
		return nil, config.PortRange{}, false
	}
	if p == "*" {
		return net.ParseIP(strings.Trim(h, "[]")), config.PortRange{Start: 1, End: 65535}, true
	}
	r, err := config.ParsePortRange(strings.ReplaceAll(p, ":", "-"))
	return net.ParseIP(strings.Trim(h, "[]")), r, err == nil
}

func conflictListeners(ctx context.Context, rules []expandedRule) error {
	v6only := "0"
	if b, e := nfRunner.Combined(ctx, "sysctl", "-n", "net.ipv6.bindv6only"); e == nil {
		v6only = strings.TrimSpace(string(b))
	}
	for _, proto := range []string{"tcp", "udp"} {
		flag := "-H -ln"
		if proto == "tcp" {
			flag += "t"
		} else {
			flag += "u"
		}
		b, err := nfRunner.Combined(ctx, "ss", strings.Fields(flag)...)
		if err != nil {
			return fmt.Errorf("cannot inspect local %s listeners: %w", proto, err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			ip, pr, ok := parseHostPortLoose(f[len(f)-2])
			if !ok {
				continue
			}
			for _, r := range rules {
				if r.proto != proto || uint16(r.listenPort) < pr.Start || uint16(r.listenPort) > pr.End {
					continue
				}
				want := net.ParseIP(r.listen)
				if ip == nil {
					return fmt.Errorf("%s %s %s:%d conflicts with wildcard local listener %q", r.family, proto, r.listen, r.listenPort, line)
				}
				same := (want.To4() != nil) == (ip != nil && ip.To4() != nil)
				dual := ip != nil && ip.To4() == nil && ip.IsUnspecified() && v6only == "0" && want.To4() != nil
				if (same && overlapAddr(want, ip)) || dual {
					return fmt.Errorf("%s %s %s:%d conflicts with local listener %q", r.family, proto, r.listen, r.listenPort, line)
				}
			}
		}
	}
	return nil
}

func findArg(f []string, key string) string {
	for i := 0; i+1 < len(f); i++ {
		if f[i] == key {
			return f[i+1]
		}
	}
	return ""
}

func ruleUsesConnmark(line string, wanted uint32) bool {
	f := splitRule(line)
	for _, key := range []string{"--mark", "--set-mark", "--set-xmark"} {
		raw := findArg(f, key)
		if raw == "" {
			continue
		}
		n, err := strconv.ParseUint(strings.SplitN(raw, "/", 2)[0], 0, 32)
		if err == nil && uint32(n) == wanted {
			return true
		}
	}
	return false
}

func detectChainHashCollision(ctx context.Context, id instanceid.Identity, tools map[string]familyTools) error {
	hash := instanceid.Hash80(id.InstanceID)
	for family, t := range tools {
		familyDigit := "4"
		if family == "ipv6" {
			familyDigit = "6"
		}
		for _, table := range []string{"nat", "filter"} {
			raw, err := saveTable(ctx, t, table)
			if err != nil {
				return err
			}
			for _, line := range strings.Split(raw, "\n") {
				fields := splitRule(line)
				if len(fields) == 0 {
					continue
				}
				name := ""
				if strings.HasPrefix(line, ":") {
					name = strings.TrimPrefix(fields[0], ":")
				} else if len(fields) >= 2 && fields[0] == "-A" {
					name = fields[1]
				} else if len(fields) >= 3 && strings.HasPrefix(fields[0], "[") && fields[1] == "-A" {
					name = fields[2]
				}
				if name == "" {
					continue
				}
				for _, purpose := range []string{"N", "F", "P"} {
					if strings.HasPrefix(name, "B"+familyDigit+purpose+hash) && strings.Contains(line, "backpack:") && !strings.Contains(line, "backpack:"+id.InstanceID+":") {
						return fmt.Errorf("chain hash collision in %s/%s chain %s: ownership comment belongs to a different instance", family, table, name)
					}
				}
			}
		}
	}
	return nil
}

func conflictDNAT(ctx context.Context, id instanceid.Identity, tools map[string]familyTools, rules []expandedRule) error {
	for family, t := range tools {
		for _, table := range []string{"nat", "filter", "mangle"} {
			raw, err := saveTable(ctx, t, table)
			if err != nil {
				return err
			}
			for _, line := range strings.Split(raw, "\n") {
				if ruleUsesConnmark(line, id.Connmark) && !strings.Contains(line, "backpack:"+id.InstanceID+":") {
					return fmt.Errorf("connmark %#x conflicts with %s/%s rule: %s", id.Connmark, family, table, line)
				}
			}
		}
		s, err := saveTable(ctx, t, "nat")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(s, "\n") {
			if !strings.Contains(line, "-j DNAT") || strings.Contains(line, "backpack:"+id.InstanceID+":") {
				continue
			}
			f := splitRule(line)
			proto, port, dst := findArg(f, "-p"), findArg(f, "--dport"), findArg(f, "-d")
			if proto == "" || port == "" || strings.Contains(line, "--match-set") || strings.Contains(line, "multiport") {
				return fmt.Errorf("cannot safely analyse possible %s DNAT conflict in nat rule: %s", family, line)
			}
			pr, e := config.ParsePortRange(strings.ReplaceAll(port, ":", "-"))
			if e != nil {
				return fmt.Errorf("cannot safely analyse DNAT port in %s rule: %s", family, line)
			}
			var dip net.IP
			var dnet *net.IPNet
			if dst != "" {
				if x, network, e := net.ParseCIDR(dst); e == nil {
					dip = x
					dnet = network
				} else {
					dip = net.ParseIP(dst)
				}
			}
			for _, r := range rules {
				if r.family != family || r.proto != proto || r.listenPort < pr.Start || r.listenPort > pr.End {
					continue
				}
				listen := net.ParseIP(r.listen)
				if dip == nil || dip.IsUnspecified() || overlapAddr(listen, dip) || (dnet != nil && (listen.IsUnspecified() || dnet.Contains(listen))) {
					return fmt.Errorf("%s %s port %d conflicts with nat rule: %s", family, proto, r.listenPort, line)
				}
			}
		}
		if t.backend == "nft" {
			b, e := nfRunner.Combined(ctx, "nft", "-j", "list", "ruleset")
			if e != nil {
				return fmt.Errorf("cannot inspect native nftables rules: %w", e)
			}
			var root any
			if json.Unmarshal(b, &root) != nil {
				return fmt.Errorf("cannot parse nft JSON ruleset")
			}
			if conflict := nativeDNATConflict(root, family, id.InstanceID); conflict != "" {
				return fmt.Errorf("%s native nftables DNAT expression cannot be safely proven non-overlapping: %s", family, conflict)
			}
		}
	}
	return nil
}

func nativeDNATConflict(v any, family, currentID string) string {
	switch x := v.(type) {
	case map[string]any:
		if rule, ok := x["rule"]; ok {
			b, _ := json.Marshal(rule)
			s := string(b)
			matchesFamily := true
			if rm, ok := rule.(map[string]any); ok {
				if raw, ok := rm["family"].(string); ok {
					matchesFamily = raw == "inet" || (family == "ipv4" && raw == "ip") || (family == "ipv6" && raw == "ip6")
				}
			}
			if matchesFamily && strings.Contains(s, "\"dnat\"") && !strings.Contains(s, "backpack:"+currentID+":") {
				return s
			}
		}
		for _, z := range x {
			if conflict := nativeDNATConflict(z, family, currentID); conflict != "" {
				return conflict
			}
		}
	case []any:
		for _, z := range x {
			if conflict := nativeDNATConflict(z, family, currentID); conflict != "" {
				return conflict
			}
		}
	}
	return ""
}

func conflictIdentity(configPath string, id instanceid.Identity) error {
	files, _ := filepath.Glob(filepath.Join(instanceid.Dir(configPath), "*.json"))
	for _, p := range files {
		if filepath.Clean(p) == filepath.Clean(instanceid.Path(configPath)) {
			continue
		}
		b, e := os.ReadFile(p)
		if e != nil {
			continue
		}
		var other instanceid.Identity
		if json.Unmarshal(b, &other) != nil {
			continue
		}
		if other.InstanceID == id.InstanceID {
			return fmt.Errorf("instance identity collision with %s", p)
		}
		if other.Connmark == id.Connmark {
			return fmt.Errorf("connmark %#x collides with %s", id.Connmark, p)
		}
	}
	return nil
}

func validateSystem(ctx context.Context, r Request, id instanceid.Identity, tools map[string]familyTools, rules []expandedRule) error {
	for _, m := range r.Config.Forward.Mappings {
		if !localAddressPresent(m.ListenAddress) {
			return fmt.Errorf("listen address %s is not assigned to a local interface", m.ListenAddress)
		}
		familyFlag := "-4"
		if net.ParseIP(m.TargetAddress).To4() == nil {
			familyFlag = "-6"
		}
		if route, routeErr := nfRunner.Combined(ctx, "ip", familyFlag, "route", "get", m.TargetAddress); routeErr == nil {
			fields := strings.Fields(strings.ToLower(string(route)))
			for _, field := range fields {
				if field == "broadcast" || field == "multicast" {
					return fmt.Errorf("target_address %s resolves to a kernel %s route and is not unicast", m.TargetAddress, field)
				}
			}
		}
	}
	if err := conflictIdentity(r.ConfigPath, id); err != nil {
		return err
	}
	if err := detectChainHashCollision(ctx, id, tools); err != nil {
		return err
	}
	if err := conflictConfigs(r); err != nil {
		return err
	}
	if err := conflictListeners(ctx, rules); err != nil {
		return err
	}
	return conflictDNAT(ctx, id, tools, rules)
}

func setForwarding(ctx context.Context, families []string) error {
	for _, f := range families {
		key := "net.ipv4.ip_forward"
		if f == "ipv6" {
			key = "net.ipv6.conf.all.forwarding"
		}
		b, e := nfRunner.Combined(ctx, "sysctl", "-w", key+"=1")
		if e != nil {
			return fmt.Errorf("enable %s: %w: %s", key, e, strings.TrimSpace(string(b)))
		}
		b, e = nfRunner.Combined(ctx, "sysctl", "-n", key)
		if e != nil || strings.TrimSpace(string(b)) != "1" {
			return fmt.Errorf("%s did not remain enabled", key)
		}
	}
	return nil
}

func (iptablesProvider) Validate(ctx context.Context, r Request) error {
	if r.Config == nil {
		return fmt.Errorf("nil iptables configuration")
	}
	if err := r.Config.ValidateStructure(); err != nil {
		return err
	}
	tools, err := detectTools(ctx, r.Config)
	if err != nil {
		return err
	}
	if err = checkCapabilities(ctx, tools); err != nil {
		return err
	}
	rules, err := expand(r.Config)
	if err != nil {
		return err
	}
	id, err := instanceid.Resolve(r.ConfigPath, false)
	if err != nil {
		return err
	}
	return withNetfilterLock(func() error { return validateSystem(ctx, r, id, tools, rules) })
}

func (iptablesProvider) Run(ctx context.Context, r Request) error {
	if r.Config == nil {
		return fmt.Errorf("nil iptables configuration")
	}
	if err := r.Config.ValidateStructure(); err != nil {
		return err
	}
	tools, err := detectTools(ctx, r.Config)
	if err != nil {
		return err
	}
	if err = checkCapabilities(ctx, tools); err != nil {
		return err
	}
	var backendSummary []string
	for family, tool := range tools {
		backendSummary = append(backendSummary, family+"=iptables-"+tool.backend)
	}
	sort.Strings(backendSummary)
	log.Printf("backpack direct engine: instance=%s backend=%s", instanceid.Name(r.ConfigPath), strings.Join(backendSummary, ","))
	rules, err := expand(r.Config)
	if err != nil {
		return err
	}
	id, err := instanceid.Resolve(r.ConfigPath, true)
	if err != nil {
		return err
	}
	families := neededFamilies(r.Config)
	cleanupTools := augmentOptionalTools(ctx, tools)
	var state forwardState
	err = withNetfilterLock(func() error {
		if err := validateSystem(ctx, r, id, tools, rules); err != nil {
			return err
		}
		state = loadForwardState(r.ConfigPath, id)
		if err := requireStateTools(state, cleanupTools); err != nil {
			return err
		}
		if err := updateCountersLockedMode(ctx, r, id, cleanupTools, &state, false); err != nil {
			return fmt.Errorf("scrape previous generation counters: %w", err)
		}
		state.Session = kernelCount{}
		state.StartedAt = time.Now()
		if err := reconcileCrashLeftovers(ctx, id, cleanupTools, state, families); err != nil {
			return err
		}
		if err := setForwarding(ctx, families); err != nil {
			return err
		}
		g, err := makeGeneration(id, state.Generation+1, families)
		if err != nil {
			return err
		}
		if err = buildDetached(ctx, id, g, tools, rules); err != nil {
			return err
		}
		if err = verifyGeneration(ctx, id, g, tools, rules, false); err != nil {
			rollbackGeneration(ctx, id, g, tools)
			return err
		}
		// Conflict state may have changed while detached chains were being built.
		if err = validateSystem(ctx, r, id, tools, rules); err != nil {
			rollbackGeneration(ctx, id, g, tools)
			return err
		}
		if err = installHooks(ctx, id, g, tools); err != nil {
			rollbackGeneration(ctx, id, g, tools)
			return err
		}
		if err = verifyGeneration(ctx, id, g, tools, rules, true); err != nil {
			rollbackGeneration(ctx, id, g, tools)
			return err
		}
		state.Generation = g.num
		state.Families = append([]string(nil), families...)
		state.DesiredHash = desiredHash(r.Config)
		state.Last[g.key] = kernelCount{}
		if err = saveForwardState(r.ConfigPath, state); err != nil {
			rollbackGeneration(ctx, id, g, tools)
			return err
		}
		// New ingress is live. Retire older hooks without ever detaching the
		// verified current generation.
		if err = deleteOtherGenerationHooks(ctx, id, cleanupTools, "hook-prerouting", g.key); err != nil {
			return err
		}
		if err = updateCountersLocked(ctx, r, id, cleanupTools, &state); err != nil {
			return err
		}
		if err = deleteOtherGenerationHooks(ctx, id, cleanupTools, "hook-forward", g.key); err != nil {
			return err
		}
		if err = deleteOtherGenerationHooks(ctx, id, cleanupTools, "hook-postrouting", g.key); err != nil {
			return err
		}
		// Remove old chains only; preserve the current generation.
		chains, e := ownedChains(ctx, id, cleanupTools)
		if e != nil {
			return e
		}
		for family, tables := range chains {
			t := cleanupTools[family]
			for table, list := range tables {
				for _, ch := range list {
					keep := false
					for _, cur := range g.chains[family] {
						if ch == cur {
							keep = true
						}
					}
					if keep {
						continue
					}
					_ = runNF(ctx, t.cmd, "-w", "5", "-t", table, "-F", ch)
					if e = runNF(ctx, t.cmd, "-w", "5", "-t", table, "-X", ch); e != nil {
						return e
					}
				}
			}
		}
		if err = verifyGeneration(ctx, id, g, tools, rules, true); err != nil {
			return err
		}
		state.RulesHash, err = liveRulesHash(ctx, id, g, tools)
		if err != nil {
			return err
		}
		return saveForwardState(r.ConfigPath, state)
	})
	if err != nil {
		return err
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return withNetfilterLock(func() error {
				// Quiesce ingress, scrape, then remove the remaining hooks and chains.
				var cleanupErrs []error
				cleanupErrs = append(cleanupErrs, deleteOwnedHooks(context.Background(), id, cleanupTools, "hook-prerouting"))
				cleanupErrs = append(cleanupErrs, updateCountersLocked(context.Background(), r, id, cleanupTools, &state))
				cleanupErrs = append(cleanupErrs, deleteOwnedHooks(context.Background(), id, cleanupTools, "hook-forward"))
				cleanupErrs = append(cleanupErrs, deleteOwnedHooks(context.Background(), id, cleanupTools, "hook-postrouting"))
				cleanupErrs = append(cleanupErrs, removeOwnedChains(context.Background(), id, cleanupTools))
				if joined := errors.Join(cleanupErrs...); joined != nil {
					return joined
				}
				state.Families = nil
				state.RulesHash = ""
				return saveForwardState(r.ConfigPath, state)
			})
		case <-ticker.C:
			_ = withNetfilterLock(func() error { return updateCountersLocked(ctx, r, id, cleanupTools, &state) })
		}
	}
}

func (iptablesProvider) Counters(ctx context.Context, r Request) (Counters, error) {
	id, err := instanceid.Resolve(r.ConfigPath, false)
	if err != nil {
		return Counters{}, err
	}
	tools := map[string]familyTools{}
	if r.Config != nil {
		tools, err = detectTools(ctx, r.Config)
		if err != nil {
			return Counters{}, err
		}
	}
	tools = augmentOptionalTools(ctx, tools)
	var result Counters
	err = withNetfilterLock(func() error {
		s := loadForwardState(r.ConfigPath, id)
		if e := requireStateTools(s, tools); e != nil {
			return e
		}
		if len(tools) > 0 {
			if e := updateCountersLocked(ctx, r, id, tools, &s); e != nil {
				return e
			}
		}
		result = Counters{RXBytes: s.Cumulative.RXBytes, TXBytes: s.Cumulative.TXBytes, RXPackets: s.Cumulative.RXPackets, TXPackets: s.Cumulative.TXPackets}
		return nil
	})
	return result, err
}

func (iptablesProvider) Health(ctx context.Context, r Request) (Health, error) {
	var result Health
	err := withNetfilterLock(func() error {
		var inner error
		result, inner = iptablesHealthLocked(ctx, r)
		return inner
	})
	return result, err
}

func iptablesHealthLocked(ctx context.Context, r Request) (Health, error) {
	if r.Config == nil {
		return Health{}, fmt.Errorf("nil config")
	}
	tools, err := detectTools(ctx, r.Config)
	if err != nil {
		return Health{Detail: err.Error()}, nil
	}
	if err := checkCapabilities(ctx, tools); err != nil {
		return Health{Detail: err.Error(), Drift: []string{err.Error()}}, nil
	}
	inspectionTools := augmentOptionalTools(ctx, tools)
	id, _ := instanceid.Resolve(r.ConfigPath, false)
	s := loadForwardState(r.ConfigPath, id)
	if err := requireStateTools(s, inspectionTools); err != nil {
		return Health{Detail: err.Error(), Drift: []string{err.Error()}}, nil
	}
	families := neededFamilies(r.Config)
	g, e := makeGeneration(id, s.Generation, families)
	if e != nil {
		return Health{}, e
	}
	var drift []string
	for _, f := range families {
		key := "net.ipv4.ip_forward"
		if f == "ipv6" {
			key = "net.ipv6.conf.all.forwarding"
		}
		b, e := nfRunner.Combined(ctx, "sysctl", "-n", key)
		if e != nil || strings.TrimSpace(string(b)) != "1" {
			drift = append(drift, key+" is not enabled")
		}
	}
	if s.DesiredHash != desiredHash(r.Config) {
		drift = append(drift, "desired-state hash differs from persisted generation")
	}
	if live, le := liveRulesHash(ctx, id, g, inspectionTools); le != nil {
		drift = append(drift, le.Error())
	} else if s.RulesHash == "" || live != s.RulesHash {
		drift = append(drift, "live rule-set differs from the desired-state hash")
	}
	rules, expandErr := expand(r.Config)
	if expandErr != nil {
		drift = append(drift, expandErr.Error())
	}
	if e = verifyGeneration(ctx, id, g, tools, rules, true); e != nil {
		drift = append(drift, e.Error())
	}
	if chains, ce := ownedChains(ctx, id, inspectionTools); ce == nil {
		for family, tables := range chains {
			current := map[string]bool{}
			for _, ch := range g.chains[family] {
				current[ch] = true
			}
			for _, list := range tables {
				for _, ch := range list {
					if !current[ch] {
						drift = append(drift, "stale generation chain "+ch+" remains")
					}
				}
			}
		}
	} else {
		drift = append(drift, ce.Error())
	}
	for _, t := range inspectionTools {
		for _, table := range []string{"nat", "filter"} {
			raw, he := saveTable(ctx, t, table)
			if he != nil {
				continue
			}
			for _, line := range strings.Split(raw, "\n") {
				if strings.Contains(line, "backpack:"+id.InstanceID+":hook-") && !strings.Contains(line, ":"+g.key) {
					drift = append(drift, "stale generation hook remains in "+table)
					break
				}
			}
		}
	}
	backs := map[string]bool{}
	for _, t := range tools {
		backs[t.backend] = true
	}
	var names []string
	for b := range backs {
		names = append(names, "iptables-"+b)
	}
	sort.Strings(names)
	return Health{Ready: len(drift) == 0, Backend: strings.Join(names, ","), Detail: map[bool]string{true: "local netfilter desired state is ready", false: "local netfilter drift detected"}[len(drift) == 0], Drift: drift}, nil
}

func (iptablesProvider) Cleanup(ctx context.Context, r Request) error {
	tools := map[string]familyTools{}
	var err error
	if r.Config != nil {
		tools, err = detectTools(ctx, r.Config)
		if err != nil {
			return err
		}
	}
	tools = augmentOptionalTools(ctx, tools)
	if len(tools) == 0 {
		return fmt.Errorf("cleanup cannot inspect either iptables family")
	}
	id, err := instanceid.Resolve(r.ConfigPath, false)
	if err != nil {
		return err
	}
	return withNetfilterLock(func() error {
		s := loadForwardState(r.ConfigPath, id)
		if err := requireStateTools(s, tools); err != nil {
			return err
		}
		var errs []error
		errs = append(errs, deleteOwnedHooks(ctx, id, tools, "hook-prerouting"))
		errs = append(errs, updateCountersLocked(ctx, r, id, tools, &s))
		errs = append(errs, deleteOwnedHooks(ctx, id, tools, "hook-forward"))
		errs = append(errs, deleteOwnedHooks(ctx, id, tools, "hook-postrouting"))
		errs = append(errs, removeOwnedChains(ctx, id, tools))
		if joined := errors.Join(errs...); joined != nil {
			return joined
		}
		s.Families = nil
		s.RulesHash = ""
		return saveForwardState(r.ConfigPath, s)
	})
}

var _ Provider = iptablesProvider{}

func cleanupOrphans(ctx context.Context, configDir string, all bool) error {
	files, _ := filepath.Glob(filepath.Join(configDir, "instances", "*.json"))
	var errs []error
	for _, identityPath := range files {
		name := strings.TrimSuffix(filepath.Base(identityPath), ".json")
		configPath := filepath.Join(configDir, name+".toml")
		if !all {
			if _, err := os.Stat(configPath); err == nil {
				continue
			}
		}
		b, err := os.ReadFile(identityPath)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var id instanceid.Identity
		if json.Unmarshal(b, &id) != nil || id.InstanceID == "" || id.Connmark == 0 {
			continue
		}

		// Probe each family independently: absence of ip6tables must not prevent
		// IPv4 orphan cleanup, and vice versa.
		tools := map[string]familyTools{}
		for _, f := range []string{"ipv4", "ipv6"} {
			listen, target := "0.0.0.0", "192.0.2.1"
			if f == "ipv6" {
				listen, target = "::", "2001:db8::1"
			}
			dummy := &config.Config{Engine: config.EngineIPTables, Forward: config.ForwardConfig{Mappings: []config.ForwardMapping{{ListenAddress: listen, ListenPorts: "1", TargetAddress: target, TargetPorts: "1", Protocols: []string{"tcp"}}}}}
			if detected, e := detectTools(ctx, dummy); e == nil {
				for k, t := range detected {
					tools[k] = t
				}
			}
		}
		if len(tools) == 0 {
			errs = append(errs, fmt.Errorf("cannot inspect orphan %s: no compatible netfilter tools", name))
			continue
		}
		r := Request{ConfigPath: configPath, Config: &config.Config{Engine: config.EngineIPTables}}
		s := loadForwardState(configPath, id)
		err = withNetfilterLock(func() error {
			if toolErr := requireStateTools(s, tools); toolErr != nil {
				return toolErr
			}
			var cleanupErrs []error
			cleanupErrs = append(cleanupErrs, deleteOwnedHooks(ctx, id, tools, "hook-prerouting"))
			cleanupErrs = append(cleanupErrs, updateCountersLocked(ctx, r, id, tools, &s))
			cleanupErrs = append(cleanupErrs, deleteOwnedHooks(ctx, id, tools, "hook-forward"))
			cleanupErrs = append(cleanupErrs, deleteOwnedHooks(ctx, id, tools, "hook-postrouting"))
			cleanupErrs = append(cleanupErrs, removeOwnedChains(ctx, id, tools))
			return errors.Join(cleanupErrs...)
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("cleanup orphan %s: %w", name, err))
			continue
		}
		_ = os.Remove(identityPath)
		_ = os.Remove(statePath(configPath, id.InstanceID))
	}
	return errors.Join(errs...)
}

package manage

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/backpack/backpack/config"
)

// validPort reports whether s is a valid TCP/UDP port number.
func validPort(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1 && n <= 65535
}

// ValidName is validName for callers outside this package.
//
// It exists for one job: checking a name that arrived from another machine.
// Names created here go through the wizard or the panel form and are checked
// on the way in, so nothing local needs this. A name read off a managed
// server's config directory is a filename that server chose, and on Linux a
// filename may hold anything but "/" and NUL — quotes, angle brackets, a whole
// script tag. Nothing downstream of that read was treating it as foreign.
func ValidName(name string) bool { return validName(name) }

// parsePorts splits a comma-separated port specification into individual
// entries, trimming whitespace and dropping empties. Mapping forms such as
// "443=1.1.1.1:443", ranges "443-450", and plain "443" are all passed through
// to the engine's port parser unchanged.
func parsePorts(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

// validPortSpec reports whether one forwarded-port entry is in a shape the
// engine's port parser accepts: "N", "N-M", "N=addr", "N-M=addr" or
// "ip:port=addr". The engine reports and ignores an entry it cannot read now
// rather than exiting on it, so a bad one no longer takes the tunnel with it —
// but a mapping that is silently doing nothing is still worth refusing here,
// where the operator is looking at it and can fix it.
func validPortSpec(spec string) bool {
	spec = strings.TrimSpace(spec)
	parts := strings.SplitN(spec, "=", 2)
	local := strings.TrimSpace(parts[0])
	if local == "" {
		return false
	}
	if len(parts) == 2 && strings.TrimSpace(parts[1]) == "" {
		return false // "443=" — empty destination
	}
	// "ip:port=addr" — a full local address is only valid in mapping form.
	if h, p, err := net.SplitHostPort(local); err == nil && h != "" {
		return len(parts) == 2 && validPort(p)
	}
	// Port range "N-M".
	if strings.Contains(local, "-") {
		r := strings.Split(local, "-")
		if len(r) != 2 {
			return false
		}
		lo, hi := strings.TrimSpace(r[0]), strings.TrimSpace(r[1])
		if !validPort(lo) || !validPort(hi) {
			return false
		}
		a, _ := strconv.Atoi(lo)
		b, _ := strconv.Atoi(hi)
		return b >= a
	}
	// Plain single port.
	return validPort(local)
}

// validatePortSpecs checks every forwarded-port entry, returning a descriptive
// error for the first invalid one.
func validatePortSpecs(ports []string) error {
	for _, p := range ports {
		if !validPortSpec(p) {
			return fmt.Errorf("invalid port entry %q — use forms like 443, 400-450, 443=1.1.1.1:443", strings.TrimSpace(p))
		}
	}
	return nil
}

// ValidateConfigFile reports what is wrong with a tunnel configuration on disk,
// without starting anything.
//
// It exists because there was no way to ask. The engine validates thoroughly at
// load — the transport, the carrier's privileges, the proxy settings, the flag
// lists — and it does so by exiting, which is right for a service supervisor and
// useless for a person who has just hand-edited a file and would like to know
// before they restart a tunnel that is currently working. The reload path is
// careful about this in the other direction: a file that does not parse is
// ignored and the tunnel keeps running, quietly, with the operator none the
// wiser that their edit did nothing.
//
// What this covers is deliberately bounded and it says so rather than implying
// more: the file parses, it describes exactly one kind of tunnel, the addresses
// and ports are addresses and ports, and the forwarded-port syntax is the
// syntax. The checks that need the machine — whether a raw socket can be
// opened, whether an interface exists, whether iptables is installed — stay
// where they are, in the engine, because their answers are only true on the
// host the tunnel will run on.
func ValidateConfigFile(path string) []string {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	var cfg config.Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return []string{"the file does not parse: " + err.Error()}
	}

	kinds := 0
	for _, on := range []bool{cfg.Server.BindAddr != "", cfg.Client.RemoteAddr != "",
		cfg.L3.Enabled(), cfg.Direct.Enabled()} {
		if on {
			kinds++
		}
	}
	switch {
	case kinds == 0:
		add("this file describes no tunnel: it has no [server] bind_addr, no [client] " +
			"remote_addr, no [l3] mode and no [direct] mode")
	case kinds > 1:
		add("this file describes %d tunnels at once. One config is one tunnel — the "+
			"engine picks [l3], then [direct], then [server]/[client], and the others "+
			"are silently ignored", kinds)
	}

	if cfg.Server.BindAddr != "" {
		if _, err := parseTunnelBind(cfg.Server.BindAddr); err != nil {
			add("bind_addr: %v", err)
		}
		if len(cfg.Server.Ports) == 0 {
			add("a server tunnel with no ports forwards nothing")
		}
		if err := validatePortSpecs(cfg.Server.Ports); err != nil {
			add("ports: %v", err)
		}
		if strings.TrimSpace(cfg.Server.Token) == "" {
			add("token is empty; both ends must carry the same one")
		}
	}
	if cfg.Client.RemoteAddr != "" {
		if h, p, err := net.SplitHostPort(cfg.Client.RemoteAddr); err != nil {
			add("remote_addr %q is not host:port: %v", cfg.Client.RemoteAddr, err)
		} else if h == "" || !validPort(p) {
			add("remote_addr %q has no host, or a port outside 1-65535", cfg.Client.RemoteAddr)
		}
		if strings.TrimSpace(cfg.Client.Token) == "" {
			add("token is empty; both ends must carry the same one")
		}
	}
	if cfg.L3.Enabled() {
		if m := strings.ToLower(strings.TrimSpace(cfg.L3.Mode)); m != "dial" && m != "listen" {
			add("[l3] mode is %q; it has to be \"dial\" or \"listen\"", cfg.L3.Mode)
		}
		if _, _, err := net.SplitHostPort(cfg.L3.Addr); err != nil {
			add("[l3] addr %q is not host:port: %v", cfg.L3.Addr, err)
		}
		if strings.TrimSpace(cfg.L3.Token) == "" {
			add("[l3] token is empty")
		}
		if err := validatePortSpecs(cfg.L3.Ports); err != nil {
			add("[l3] ports: %v", err)
		}
	}
	return problems
}

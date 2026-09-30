package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/backpack/backpack/internal/localproxy"
	"github.com/backpack/backpack/internal/manage"
)

// `backpack proxy` — the menu's Built-in Proxy, without the menu.
//
// The proxy belongs on the tunnel's exit side, which for a reverse tunnel is
// the kharej. The panel lives on the Iran server, so when it wires a tunnel's
// port to the proxy it can do its own half and has to hand the other half over
// as one line to paste on the kharej — this is that line.

// enableProxy and disableProxy are variables so a test can see what would
// have been done without a systemd to do it with.
var (
	enableProxy  = manage.EnableProxyService
	disableProxy = manage.DisableProxyService
	proxyRunning = manage.ProxyRunning
)

func runProxy(args []string) Result {
	if len(args) == 0 {
		return fail(CodeUsage, "proxy needs a subcommand: enable, disable or status\n")
	}
	asJSON, rest := takeJSONFlag(args[1:])
	switch args[0] {
	case "status":
		c := localproxy.Load()
		on := c.Enabled && proxyRunning()
		if asJSON {
			return jsonResult(map[string]any{"enabled": c.Enabled, "running": on,
				"type": c.Type, "port": c.Port, "auth": c.Username != ""})
		}
		if !on {
			return ok("proxy: off\n")
		}
		return ok(fmt.Sprintf("proxy: %s on 127.0.0.1:%d%s\n", c.Type, c.Port,
			map[bool]string{true: " (username and password)", false: " (open)"}[c.Username != ""]))
	case "disable":
		if !isRoot() {
			return fail(CodeFailed, "proxy disable removes a service: run it as root (sudo)\n")
		}
		if err := disableProxy(); err != nil {
			return fail(CodeFailed, "could not disable the proxy: %v\n", err)
		}
		return ok("✓ The built-in proxy is off.\n")
	case "enable":
		return proxyEnable(rest)
	}
	return fail(CodeUsage, "unknown proxy subcommand %q\n", args[0])
}

// proxyEnable is `proxy enable <socks5|http> <port> [--user U --pass P]`.
func proxyEnable(args []string) Result {
	c := localproxy.Load()
	var pos []string
	user, pass := "", ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--user" || a == "--pass":
			if i+1 >= len(args) {
				return fail(CodeUsage, "%s needs a value\n", a)
			}
			if a == "--user" {
				user = args[i+1]
			} else {
				pass = args[i+1]
			}
			i++
		case strings.HasPrefix(a, "--user="):
			user = strings.TrimPrefix(a, "--user=")
		case strings.HasPrefix(a, "--pass="):
			pass = strings.TrimPrefix(a, "--pass=")
		case strings.HasPrefix(a, "-"):
			return fail(CodeUsage, "unknown option %q\n", a)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) != 2 {
		return fail(CodeUsage, "usage: backpack proxy enable <socks5|http> <port> [--user U --pass P]\n")
	}
	switch localproxy.Kind(strings.ToLower(pos[0])) {
	case localproxy.SOCKS5:
		c.Type = localproxy.SOCKS5
	case localproxy.HTTP:
		c.Type = localproxy.HTTP
	default:
		return fail(CodeUsage, "the proxy type is socks5 or http, not %q\n", pos[0])
	}
	port, err := strconv.Atoi(pos[1])
	if err != nil || port < 1 || port > 65535 {
		return fail(CodeUsage, "the port must be 1–65535\n")
	}
	if (user == "") != (pass == "") {
		return fail(CodeUsage, "give both --user and --pass, or neither\n")
	}
	if !isRoot() {
		return fail(CodeFailed, "proxy enable installs a service: run it as root (sudo)\n")
	}
	if (port != c.Port || !proxyRunning()) && manage.PortInUse(strconv.Itoa(port)) {
		return fail(CodeFailed, "port %d is already in use on this server\n", port)
	}
	c.Port, c.Username, c.Password = port, user, pass
	if err := enableProxy(c); err != nil {
		return fail(CodeFailed, "could not enable the proxy: %v\n", err)
	}
	auth := "open — anyone who reaches the tunnel's port can use it"
	if user != "" {
		auth = "username " + user
	}
	return ok(fmt.Sprintf("✓ Built-in proxy is on: %s on 127.0.0.1:%d (%s).\n", c.Type, port, auth))
}

// The built-in SOCKS5 proxy screen.

package menu

import (
	"fmt"

	"github.com/backpack/backpack/internal/localproxy"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/tui"
)

// proxyLabel summarises the built-in proxy for the menu row.
func proxyLabel() string {
	c := localproxy.Load()
	if !c.Enabled || !manage.ProxyRunning() {
		return "off"
	}
	return fmt.Sprintf("%s on :%d", c.Type, c.Port)
}

// builtinProxyMenu configures the optional built-in SOCKS5/HTTP proxy: this
// node becomes its own backend, so nothing separate (xray, a panel) has to be
// installed behind the tunnel. The operator picks the port — there is no
// assumed default — and forwards a tunnel port to 127.0.0.1:<that port>.
func builtinProxyMenu() {
	tui.Clear()
	tui.Title("Built-in Proxy")
	tui.Warn("This node serves its own SOCKS5 or HTTP proxy on a loopback port.")
	tui.Warn("Point a forwarded port at 127.0.0.1:<that port> and the tunnel exit")
	tui.Warn("is the proxy itself — no separate backend to install or keep running.")
	fmt.Println()

	c := localproxy.Load()
	if c.Enabled && manage.ProxyRunning() {
		tui.Info(fmt.Sprintf("Currently: %s proxy on 127.0.0.1:%d", c.Type, c.Port))
	} else {
		tui.Info("Currently: off")
	}
	fmt.Println()

	idx := tui.ChooseOpt("Choose:", []tui.Option{
		{Title: "Enable / reconfigure", Desc: "pick SOCKS5 or HTTP, a port, and optional auth"},
		{Title: "Disable", Desc: "stop and remove the proxy service"},
		{Title: "Back", Desc: ""},
	})
	switch idx {
	case 0:
		configureProxy(c)
	case 1:
		if err := manage.DisableProxyService(); err != nil {
			tui.Error("Could not disable: " + err.Error())
		} else {
			tui.Success("Built-in proxy disabled.")
		}
		tui.PressEnter()
	}
}

func configureProxy(c localproxy.Config) {
	kind := tui.ChooseOpt("Proxy type:", []tui.Option{
		{Title: "SOCKS5", Desc: "works for most apps; carries UDP too"},
		{Title: "HTTP", Desc: "for clients that only take an HTTP proxy (browsers)"},
	})
	switch kind {
	case 0:
		c.Type = localproxy.SOCKS5
	case 1:
		c.Type = localproxy.HTTP
	default:
		return
	}

	// The operator chooses the port; nothing is assumed.
	c.Port = tui.PromptInt("Port to listen on (loopback)", c.Port)
	if c.Port <= 0 || c.Port > 65535 {
		tui.Error("Port must be between 1 and 65535.")
		tui.PressEnter()
		return
	}

	if tui.Confirm("Require a username/password", c.Username != "") {
		c.Username = tui.PromptDefault("Username", c.Username)
		c.Password = tui.PromptDefault("Password", c.Password)
	} else {
		c.Username, c.Password = "", ""
		tui.Warn("No auth — safe here: the proxy binds loopback and is only")
		tui.Warn("reachable through the token-authenticated tunnel.")
	}

	if err := manage.EnableProxyService(c); err != nil {
		tui.Error("Could not enable: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success(fmt.Sprintf("%s proxy running on 127.0.0.1:%d.", c.Type, c.Port))
	tui.Info(fmt.Sprintf("Now forward a tunnel port to 127.0.0.1:%d "+
		"(e.g. in Setup Server: 443=127.0.0.1:%d).", c.Port, c.Port))
	tui.PressEnter()
}

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
	tui.Warn("A SOCKS5 Or HTTP Proxy On 127.0.0.1 — Forward A Tunnel Port To It.")
	fmt.Println()

	c := localproxy.Load()
	if c.Enabled && manage.ProxyRunning() {
		tui.Info(fmt.Sprintf("Now: %s On 127.0.0.1:%d", c.Type, c.Port))
	} else {
		tui.Info("Now: Off")
	}
	fmt.Println()

	idx := tui.ChooseOpt("Built-in Proxy", []tui.Option{
		{Title: "Enable / Change", Desc: "SOCKS5 or HTTP, port, auth"},
		{Title: "Disable", Desc: ""},
		{Title: "Back", Desc: ""},
	})
	switch idx {
	case 0:
		configureProxy(c)
	case 1:
		if err := manage.DisableProxyService(); err != nil {
			tui.Error("Could not disable: " + err.Error())
		} else {
			tui.Success("Proxy Disabled.")
		}
		tui.PressEnter()
	}
}

func configureProxy(c localproxy.Config) {
	kind := tui.ChooseOpt("Proxy Type", []tui.Option{
		{Title: "SOCKS5", Desc: "most apps; UDP too"},
		{Title: "HTTP", Desc: "browsers"},
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
	c.Port = tui.PromptInt("Port", c.Port)
	if c.Port <= 0 || c.Port > 65535 {
		tui.Error("Invalid port (1-65535).")
		tui.PressEnter()
		return
	}

	if tui.Confirm("Username And Password", c.Username != "") {
		c.Username = tui.PromptDefault("Username", c.Username)
		c.Password = tui.PromptDefault("Password", c.Password)
	} else {
		c.Username, c.Password = "", ""
		tui.Warn("No Auth — Anyone Reaching The Forwarded Port Can Use It.")
	}

	if err := manage.EnableProxyService(c); err != nil {
		tui.Error("Could not enable: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success(fmt.Sprintf("%s Proxy On 127.0.0.1:%d.", c.Type, c.Port))
	tui.Info(fmt.Sprintf("Forward A Tunnel Port To It, e.g. 443=127.0.0.1:%d", c.Port))
	tui.PressEnter()
}

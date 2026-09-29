// The Manage screen: everything that acts on a tunnel that already exists.

package menu

import (
	"fmt"

	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/tui"
)

// manageMenu is main-menu item 3.
func manageMenu() {
	for {
		tui.Clear()
		idx := tui.ChooseOpt("Manage", []tui.Option{
			{Title: "Manage Tunnels", Desc: "edit, start/stop, log, delete"},
			{Title: "Set Up From A Link", Desc: "paste the other server's link"},
			{Title: "Status", Desc: "live tunnel table"},
			{Title: "Health Check", Desc: "problems and fixes"},
			{Title: "Link Test", Desc: "latency, loss, a transport for it"},
			{Title: "Exit Health", Desc: "rank the Iran addresses"},
			{Title: "IP Spoofing Tester", Desc: "which forged sources pass"},
			{Title: "Tunnel Metrics", Desc: "traffic, loss, FEC"},
			{Title: "Restart All", Desc: "every tunnel at once"},
			{Title: "Auto Refresh", Desc: "restart all every N hours — " + refreshLabel()},
			{Title: "Built-in Proxy", Desc: "SOCKS5/HTTP backend — " + proxyLabel()},
			{Title: "File Locations", Desc: "configs, services, backups"},
		})

		switch idx {
		case 0:
			manage.ManageTunnels()
		case 1:
			manage.SetupFromLink()
		case 2:
			manage.StatusLive()
		case 3:
			manage.HealthCheck()
		case 4:
			manage.LinkTest()
		case 5:
			manage.ExitHealth()
		case 6:
			manage.SpoofTest()
		case 7:
			manage.TunnelMetrics()
		case 8:
			ok, failed := manage.RestartAll()
			tui.Success(fmt.Sprintf("Restarted %d Tunnels (%d Failed).", ok, failed))
			tui.PressEnter()
		case 9:
			autoRefreshMenu()
		case 10:
			builtinProxyMenu()
		case 11:
			manage.FileLocations()
		default:
			return
		}
	}
}

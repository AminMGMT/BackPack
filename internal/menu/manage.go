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
			{Title: "Manage Tunnels", Desc: "edit ports & transport, start/stop, live log, delete"},
			{Title: "Set up from a link", Desc: "paste the link from the other server — nothing retyped"},
			{Title: "Status", Desc: "live tunnel table"},
			{Title: "Health Check", Desc: "find problems and get a fix for each one"},
			{Title: "Link Test", Desc: "measure the link and get a transport recommendation"},
			{Title: "Exit Health", Desc: "score & rank every server address, pin the healthiest (multi-exit failover)"},
			{Title: "IP Spoofing Tester", Desc: "find which forged source IPs cross the firewall (for a direct tunnel on the spoof carrier)"},
			{Title: "Tunnel Metrics", Desc: "traffic, packet loss and error correction per tunnel"},
			{Title: "Restart ALL", Desc: "restart every tunnel at once"},
			{Title: "Auto Refresh", Desc: "restart all tunnels every N hours — " + refreshLabel()},
			{Title: "Built-in Proxy", Desc: "be your own SOCKS5/HTTP backend — " + proxyLabel()},
			{Title: "File Locations", Desc: "where every config, service and backup lives"},
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
			tui.Success(fmt.Sprintf("Restarted %d tunnels (%d failed).", ok, failed))
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

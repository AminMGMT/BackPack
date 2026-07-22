// Package monitor runs everything that has to keep working whether or not
// anybody is looking: the watchdog that restarts dropped tunnels, the Telegram
// bot, and the alerts.
//
// It is a separate process from the web panel on purpose. These jobs used to
// live inside the panel, which meant that stopping the panel also stopped the
// watchdog and every alert — without saying so. A monitor that fails silently
// is worse than no monitor, because it is trusted.
package monitor

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/socks"
	"github.com/backpack/backpack/internal/telegram"
	"github.com/backpack/backpack/internal/tunhist"
	"github.com/backpack/backpack/internal/utils"
	"github.com/sirupsen/logrus"
)

// Run starts the watchdog, the bot and the alert loop, and blocks until the
// process is asked to stop.
//
// Each job runs in its own goroutine because they fail independently: the bot's
// long poll can hang on a blocked network for the length of its timeout, and
// the watchdog must keep restarting tunnels regardless. None of them return
// under normal operation.
func Run() {
	logger := utils.NewLogger("info")
	logger.Info("backpack monitor started")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startSocksRelays(ctx, logger)

	var wg sync.WaitGroup
	jobs := []struct {
		name string
		fn   func(context.Context)
	}{
		{"watchdog", manage.RunWatchdog},
		{"telegram bot", telegram.RunBot},
		{"alerts", telegram.RunAlerts},
		{"history sampler", tunhist.Run},
		{"auto-backup", manage.RunAutoBackup},
	}
	for _, job := range jobs {
		wg.Add(1)
		go func(name string, fn func(context.Context)) {
			defer wg.Done()
			fn(ctx)
			logger.Infof("%s stopped", name)
		}(job.name, job.fn)
	}

	// The monitor is a long-lived service; systemd stops it with SIGTERM.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	logger.Info("backpack monitor stopping")
	cancel()
	wg.Wait()
}

// startSocksRelays opens the loopback endpoints the Telegram relay hands off to.
//
// This is what lets the far side reach the internet on behalf of a server that
// cannot: the Iran node exposes a tunnel port mapped to one of these addresses
// on the peer, and the proxy here makes the outbound connection.
//
// Two things it deliberately does not do quietly:
//
//   - Bind failures are logged. They used to be discarded, and since port 1080
//     is the well-known SOCKS port it is frequently already taken. The relay
//     then never started, and the only symptom was the Telegram bot failing
//     with a bare "EOF" that pointed at the wrong machine entirely.
//   - It listens on the legacy fixed port as well as the derived ones, so a
//     tunnel configured by an older version keeps working after an upgrade.
func startSocksRelays(ctx context.Context, logger *logrus.Logger) {
	auth := func(_, pass string) bool { return manage.TokenMatches(pass) }

	ports := map[int]string{app.SocksInternalPort: "legacy"}
	for _, t := range manage.List() {
		if tok := manage.TunnelToken(t.Name); tok != "" {
			ports[app.SocksPortForToken(tok)] = t.Name
		}
	}

	for port, why := range ports {
		go func(port int, why string) {
			addr := fmt.Sprintf("127.0.0.1:%d", port)
			if err := socks.Serve(ctx, addr, auth); err != nil {
				// Not fatal: another tunnel's endpoint may still work, and the
				// legacy port being taken is expected on a busy server.
				logger.Warnf("SOCKS relay for %s could not listen on %s: %v", why, addr, err)
				return
			}
			logger.Infof("SOCKS relay for %s listening on %s", why, addr)
		}(port, why)
	}
}

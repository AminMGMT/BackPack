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
	"time"

	"github.com/backpack/backpack/internal/alerthist"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/manage/core"
	"github.com/backpack/backpack/internal/socks"
	"github.com/backpack/backpack/internal/telegram"
	"github.com/backpack/backpack/internal/tunhist"
	"github.com/backpack/backpack/internal/utils"
	"github.com/sirupsen/logrus"
)

// How a job that panicked is brought back.
//
// Containing a panic per job was already right: without it a bug in the
// Telegram bot would take the watchdog down with it, and tunnels would stop
// being restarted for a reason that has nothing to do with tunnels.
//
// What was missing is what happens next. The failed job stopped for the life of
// the process, and the only trace was one line in a log nobody is reading at
// the time. If that job is the watchdog, self-healing is silently off from then
// on — which is the worst thing this service can do quietly, because the whole
// point of it is to be the thing that notices.
//
// So a panicking job is restarted, with a backoff so that one panicking in a
// tight loop does not become the new problem, and with a ceiling so that a job
// which cannot run at all is eventually left down and said so once, rather than
// restarted every five minutes for ever. The announcement goes into the alert
// history as well as the log, because a log line is not a notification.
const (
	// jobRestartFirst and jobRestartMax bound the backoff. Seconds rather than
	// milliseconds: what is being waited for is a bug clearing on its own,
	// which it usually will not — the delay is there to keep the log and the
	// processor usable while that becomes apparent.
	// jobRestartLimit is how many times one job is brought back before it is
	// left alone. A job that has panicked this many times is not going to
	// succeed on the next attempt.
	jobRestartLimit = 8
)

// Variables rather than constants so a test can drive the supervisor at test
// speed. Five seconds is right in production and would make the test for this
// take a minute, which is how a test ends up not being written.
var (
	jobRestartFirst = 5 * time.Second
	jobRestartMax   = 5 * time.Minute
)

// superviseJob runs one job, restarting it if it panics.
func superviseJob(ctx context.Context, logger *logrus.Logger, name string, fn func(context.Context)) {
	delay := jobRestartFirst
	for attempt := 1; ; attempt++ {
		if !runJobOnce(logger, name, fn, ctx) {
			logger.Infof("%s stopped", name)
			return
		}
		if ctx.Err() != nil {
			return // shutting down; nothing is worth restarting
		}
		if attempt >= jobRestartLimit {
			msg := fmt.Sprintf("monitor: %s has panicked %d times and will not be restarted "+
				"again — the other monitor jobs keep running, this one is down until the "+
				"service restarts", name, attempt)
			logger.Error(msg)
			alerthist.RecordEvent(msg)
			return
		}
		logger.Errorf("%s panicked; restarting it in %s (attempt %d of %d)",
			name, delay, attempt+1, jobRestartLimit)
		alerthist.RecordEvent(fmt.Sprintf("monitor: %s panicked and is being restarted", name))

		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if delay *= 2; delay > jobRestartMax {
			delay = jobRestartMax
		}
	}
}

// runJobOnce runs a job to completion, reporting whether it panicked.
//
// Its own function so the recover has somewhere to put an answer: a deferred
// recover in the loop above could contain the panic but could not tell the loop
// that one had happened.
func runJobOnce(logger *logrus.Logger, name string, fn func(context.Context), ctx context.Context) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			logger.Errorf("%s panicked: %v", name, r)
		}
	}()
	fn(ctx)
	return false
}

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
			superviseJob(ctx, logger, name, fn)
		}(job.name, job.fn)
	}

	// Tell systemd the service is up, and keep telling it.
	//
	// The unit is Type=notify with a WatchdogSec, so systemd learns the
	// difference between a process that exists and one that is working — a
	// monitor wedged on a job that never returns is a process systemd is
	// otherwise perfectly happy with. See internal/manage/core/sdnotify.go.
	core.NotifyReady()
	if every, ok := core.WatchdogInterval(); ok {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					core.NotifyAlive()
				}
			}
		}()
	}

	// Say, regularly, that this is still running.
	//
	// Nothing watched the watchdog. A crash brings it back — the unit says
	// Restart=always — but a crash loop, a service somebody stopped to debug
	// something and left stopped, and a machine old enough never to have had
	// the unit all produce the same silence, and silence from a watchdog reads
	// exactly like a fleet with nothing wrong.
	//
	// A heartbeat rather than a second watcher, because a second watcher needs
	// a third. See manage.RecordMonitorHeartbeat.
	wg.Add(1)
	go func() {
		defer wg.Done()
		manage.RecordMonitorHeartbeat()
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				manage.RecordMonitorHeartbeat()
			}
		}
	}()

	// The monitor is a long-lived service; systemd stops it with SIGTERM.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	logger.Info("backpack monitor stopping")
	// So the time spent closing things is not mistaken for a hang.
	core.NotifyStopping()
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

	tunnels := manage.List()

	ports := map[int]string{}
	// The well-known 1080 is bound only when a tunnel actually still maps to
	// it. Binding it on every install — which is what used to happen — squats
	// the port every other SOCKS proxy expects, so on a machine that also runs
	// a panel or an xray inbound on 1080, whichever service boots first wins
	// and the other silently loses its port. Backpack has no business holding
	// 1080 unless one of its own tunnels, written before the port was derived
	// from the token, is genuinely still using it.
	if manage.LegacySocksInUse(tunnels) {
		ports[app.SocksInternalPort] = "legacy"
	}
	for _, t := range tunnels {
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

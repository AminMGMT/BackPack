package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/backpack/backpack/cmd"
	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/manage"
	"github.com/backpack/backpack/internal/menu"
	"github.com/backpack/backpack/internal/telegram"
	"github.com/backpack/backpack/internal/utils"
	"github.com/backpack/backpack/internal/webui"
)

var logger = utils.NewLogger("info")

// main has two modes:
//
//   - Engine mode:  `backpack -c /etc/backpack/<name>.toml`
//     Runs a single tunnel (server or client). This is what the systemd
//     units execute. Behaviour is identical to the original engine.
//
//   - Menu mode:    `backpack`  (no arguments)
//     Opens the interactive management CLI on the VPS.
func main() {
	configPath := flag.String("c", "", "path to a tunnel configuration file (TOML) — runs in engine mode")
	showVersion := flag.Bool("v", false, "print the version and exit")
	restartAll := flag.Bool("restart-all", false, "restart every configured tunnel and exit (used by the auto-refresh job)")
	tgReport := flag.Bool("telegram-report", false, "send a Telegram status report and exit (used by the scheduled job)")
	webPanel := flag.Bool("webui", false, "run the web panel (used by the backpack-webui service)")
	flag.Parse()

	switch {
	case *showVersion:
		fmt.Println(app.Version)
		return
	case *restartAll:
		ok, failed := manage.RestartAll()
		fmt.Printf("restarted %d tunnels, %d failed\n", ok, failed)
		return
	case *tgReport:
		if err := telegram.SendStatusNow(); err != nil {
			logger.Errorf("telegram report failed: %v", err)
			os.Exit(1)
		}
		return
	case *webPanel:
		if err := webui.Serve(); err != nil {
			logger.Fatalf("web panel failed: %v", err)
		}
		return
	}

	// No config file -> interactive menu.
	if *configPath == "" {
		menu.Run()
		return
	}

	runEngine(*configPath)
}

// runEngine starts a single tunnel from a TOML config and blocks until a
// termination signal arrives, then shuts down gracefully.
func runEngine(configPath string) {
	ctx, cancel := context.WithCancel(context.Background())

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go cmd.Run(configPath, ctx)

	<-sigChan
	cancel()
	time.Sleep(1 * time.Second)
	logger.Info("backpack engine stopped")
}

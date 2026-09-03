package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/node"
)

// `backpack node ...` — the managed side of the panel-to-node channel.
//
// It is a subcommand rather than another flag on main because it is the one
// thing on this binary an operator types on a server they are not otherwise
// setting up: the whole point of the feature is that the foreign machine is
// touched once, with one line, and never again.

const nodeUsage = `backpack node — connect this server to a Backpack panel

  backpack node setup --panel <host:port> --key <setup-key>
        Register this server with a panel and start the agent.
        The panel shows this line ready to paste, on Nodes → Add server.

  backpack node status
        Show whether this server is managed, and by which panel.

  backpack node run
        Run the agent in the foreground. This is what the service executes;
        there is no reason to run it by hand.

  backpack node remove
        Stop being managed. Tunnels already on this server keep running.
`

func runNode(args []string) {
	if len(args) == 0 {
		fmt.Print(nodeUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "setup":
		nodeSetup(args[1:])
	case "run":
		nodeRun()
	case "status":
		nodeStatus()
	case "remove":
		if err := node.Uninstall(); err != nil {
			fmt.Fprintln(os.Stderr, "could not remove the node agent:", err)
			os.Exit(1)
		}
		fmt.Println("This server is no longer managed. Its tunnels were left running.")
	case "-h", "--help", "help":
		fmt.Print(nodeUsage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], nodeUsage)
		os.Exit(2)
	}
}

func nodeSetup(args []string) {
	fs := flag.NewFlagSet("node setup", flag.ExitOnError)
	panelAddr := fs.String("panel", "", "the panel's node address, host:port")
	key := fs.String("key", "", "the setup key the panel generated")
	fs.Parse(args)

	if *panelAddr == "" || *key == "" {
		fmt.Fprint(os.Stderr, "both --panel and --key are required\n\n"+nodeUsage)
		os.Exit(2)
	}
	hub, enroll, err := node.ParseSetupKey(*key)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "run this as root — it installs a service and writes to "+app.ConfigDir)
		os.Exit(1)
	}

	if err := node.Install(node.AgentConfig{Server: *panelAddr, HubKey: hub, Enroll: enroll}); err != nil {
		fmt.Fprintln(os.Stderr, "could not install the node agent:", err)
		os.Exit(1)
	}

	// Installed is not the same as working, and the difference is exactly what
	// the operator is standing there to find out. Wait for the agent to be
	// issued a credential, which only happens once it has reached the panel and
	// been accepted.
	fmt.Printf("Registering with %s ...\n", *panelAddr)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if node.IsManaged() {
			c, _ := node.LoadAgent()
			fmt.Printf("\nThis server is now managed as %q.\n", c.Name)
			fmt.Println("Create tunnels for it from the panel — nothing else is needed here.")
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, `
The agent is installed but has not been accepted yet.

That usually means one of:
  - the panel cannot be reached at %s (a firewall in front of the port)
  - the setup key was already used, or has expired
  - the panel's node listener is turned off

It keeps retrying, so fixing any of those is enough — nothing to re-run here.
Watch it with:  journalctl -u %s -f
`, *panelAddr, app.NodeService)
	os.Exit(1)
}

func nodeRun() {
	cfg, err := node.LoadAgent()
	if err != nil {
		fmt.Fprintln(os.Stderr, "this server is not set up as a node — run `backpack node setup` first")
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	done := make(chan struct{})
	go func() { node.NewAgent(cfg, func(m string) { logger.Info(m) }).Run(ctx); close(done) }()

	<-sig
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	logger.Info("backpack node agent stopped")
}

func nodeStatus() {
	cfg, err := node.LoadAgent()
	if err != nil {
		fmt.Println("This server is not managed by a panel.")
		return
	}
	fmt.Println("Panel:    ", cfg.Server)
	if cfg.Name != "" {
		fmt.Println("Known as: ", cfg.Name)
	}
	switch {
	case cfg.NodeKey == "":
		fmt.Println("State:     waiting to be accepted")
	case node.Running():
		fmt.Println("State:     managed, agent running")
	default:
		fmt.Println("State:     managed, but the agent is not running")
		fmt.Println("           start it with: systemctl start " + app.NodeService)
	}
}

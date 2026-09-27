package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A file that parses but does not validate must leave the running tunnel
// alone, exactly as a file that does not parse does.
//
// The reload path caught only parse errors. A parsed file went through
// applyDefaults, whose checks end in logger.Fatalf — os.Exit — so a typo in a
// value (a proxy URL, a pck flag, an interface name) took the working tunnel
// down with the process, and systemd's restart read the same file and died
// again.
//
// The check runs in a child process: what is being tested is that the process
// survives, and a Fatalf in the test binary itself would end every other test
// with it.
func TestAnInvalidEditDoesNotKillTheRunningTunnel(t *testing.T) {
	if os.Getenv("BACKPACK_RELOAD_CHILD") == "1" {
		reloadChild()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAnInvalidEditDoesNotKillTheRunningTunnel$")
	cmd.Env = append(os.Environ(), "BACKPACK_RELOAD_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the process exited while reloading a file that parses but is invalid: %v\n%s", err, out)
	}
}

// reloadChild edits a running tunnel's file into one that parses but names an
// impossible proxy, and waits past two polls. Returning at all is the pass.
func reloadChild() {
	dir, err := os.MkdirTemp("", "reload-child")
	if err != nil {
		os.Exit(3)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "tunnel.toml")

	good := "\n[client]\nremote_addr = \"127.0.0.1:3080\"\ntransport = \"tcp\"\ntoken = \"a-token\"\n"
	bad := good + "proxy = \"bogus://nowhere\"\n"

	if os.WriteFile(path, []byte(good), 0o644) != nil {
		os.Exit(3)
	}
	current, err := loadConfig(path)
	if err != nil {
		os.Exit(3)
	}
	applyDefaults(current)

	go func() {
		time.Sleep(configPollInterval / 2)
		_ = os.WriteFile(path, []byte(bad), 0o644)
		future := time.Now().Add(time.Second)
		_ = os.Chtimes(path, future, future)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*configPollInterval)
	defer cancel()
	if next, why := awaitConfigChange(ctx, context.Background(), path, current); next != nil || why != wakeShutdown {
		// It must not be handed on as a change to restart into either.
		os.Exit(4)
	}
}

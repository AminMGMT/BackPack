package manage

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/backpack/backpack/internal/app"
)

// systemctl runs a systemctl subcommand and returns combined output.
func systemctl(args ...string) (string, error) {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// DaemonReload reloads the systemd manager configuration.
func DaemonReload() error {
	_, err := systemctl("daemon-reload")
	return err
}

// unitStateTTL is how long "is this unit running" is reused for.
//
// It is the answer the panel asks for most, and the one it used to pay the most
// for: GatherSystem asks it per tunnel every four seconds, GatherTunnels asks
// it again through AllHealth every six, and each ask forked systemctl and made
// a round trip to systemd over D-Bus. On a host with several tunnels and a
// panel open, that is a process every few hundred milliseconds doing nothing
// but confirming what it confirmed a moment ago.
//
// Two seconds is well inside the poll intervals it serves, so the display is no
// less live than before; what changes is that the two pollers and every panel
// share one answer instead of each forking their own. Anything that changes a
// unit's state clears this on the way out, so a Start or Stop from the panel is
// reflected at once rather than after the window.
const unitStateTTL = 2 * time.Second

// unitStatePrune is how long an untouched unit is remembered.
const unitStatePrune = 5 * time.Minute

var unitCache = newTTLCache[bool](unitStateTTL, unitStatePrune)

// IsActive reports whether a unit is currently running.
func IsActive(service string) bool {
	return unitCache.get("is-active\x00"+service, func() bool {
		out, _ := systemctl("is-active", service)
		return out == "active"
	})
}

// IsEnabled reports whether a unit is enabled at boot.
func IsEnabled(service string) bool {
	out, _ := systemctl("is-enabled", service)
	return out == "enabled"
}

// StartService starts and enables a unit.
func StartService(service string) error {
	_, err := systemctl("enable", "--now", service)
	unitCache.forget()
	return err
}

// StopService stops a unit (leaves it enabled).
func StopService(service string) error {
	_, err := systemctl("stop", service)
	unitCache.forget()
	return err
}

// RestartService restarts a unit.
func RestartService(service string) error {
	_, err := systemctl("restart", service)
	unitCache.forget()
	return err
}

// DisableService stops and disables a unit.
func DisableService(service string) error {
	_, err := systemctl("disable", "--now", service)
	unitCache.forget()
	return err
}

// EnsureUnits brings every tunnel's unit file up to the current template.
//
// A unit is written when a tunnel is created or edited and not otherwise, so a
// tunnel set up by an older version keeps whatever that version wrote for as
// long as it exists. That is how servers ended up running tunnels on systemd's
// default ceiling of 1024 open files long after the template had been raised
// to a million — and the health check told those operators to run Optimize and
// reboot, which cannot touch a unit file and so changed nothing, ever.
//
// Rewriting is safe to do on the way in: it neither reloads nor restarts
// anything, so the new unit takes effect the next time each tunnel starts. A
// drop-in under <unit>.d/ is a separate file and is left alone, which is where
// a local change belongs anyway.
//
// It returns how many were brought up to date.
func EnsureUnits() int {
	n := 0
	for _, t := range List() {
		path := app.ServiceDir + "/" + app.ServiceName(t.Name)
		want := unitFor(t.Name)
		if have, err := os.ReadFile(path); err == nil && string(have) == want {
			continue
		}
		if os.WriteFile(path, []byte(want), 0644) == nil {
			n++
		}
	}
	if n > 0 {
		_ = DaemonReload()
	}
	return n
}

// writeUnit writes a systemd unit file for a tunnel that runs the backpack
// binary in engine mode against its config.
func writeUnit(name string) error {
	path := app.ServiceDir + "/" + app.ServiceName(name)
	return os.WriteFile(path, []byte(unitFor(name)), 0644)
}

// unitFor is the unit a tunnel should have. One definition, so EnsureUnits is
// comparing against the same thing writeUnit would produce rather than a copy
// of it that will drift.
func unitFor(name string) string {
	return fmt.Sprintf(`[Unit]
Description=Backpack Tunnel (%s)
After=network.target

[Service]
Type=simple
ExecStart=%s -c %s
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`, name, app.BinPath, app.ConfigPath(name))
}

// removeUnit deletes a tunnel unit file if present.
func removeUnit(name string) {
	os.Remove(app.ServiceDir + "/" + app.ServiceName(name))
}

// FollowLog streams live journal logs for a service until the user presses
// Ctrl+C. The child runs in its own process group so the interrupt only
// stops the log viewer, not the backpack menu.
func FollowLog(service string) error {
	cmd := exec.Command("journalctl", "-u", service, "-n", "200", "-f", "--no-pager")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return err
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			_ = cmd.Process.Kill()
		case <-done:
		}
	}()

	err := cmd.Wait()
	close(done)
	signal.Stop(sig)
	return err
}

package manage

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

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

// IsActive reports whether a unit is currently running.
func IsActive(service string) bool {
	out, _ := systemctl("is-active", service)
	return out == "active"
}

// IsEnabled reports whether a unit is enabled at boot.
func IsEnabled(service string) bool {
	out, _ := systemctl("is-enabled", service)
	return out == "enabled"
}

// StartService starts and enables a unit.
func StartService(service string) error {
	_, err := systemctl("enable", "--now", service)
	return err
}

// StopService stops a unit (leaves it enabled).
func StopService(service string) error {
	_, err := systemctl("stop", service)
	return err
}

// RestartService restarts a unit.
func RestartService(service string) error {
	_, err := systemctl("restart", service)
	return err
}

// DisableService stops and disables a unit.
func DisableService(service string) error {
	_, err := systemctl("disable", "--now", service)
	return err
}

// writeUnit writes a systemd unit file for a tunnel that runs the backpack
// binary in engine mode against its config.
func writeUnit(name string) error {
	unit := fmt.Sprintf(`[Unit]
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

	path := app.ServiceDir + "/" + app.ServiceName(name)
	return os.WriteFile(path, []byte(unit), 0644)
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

//go:build linux

package naive

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}
func terminateProcess(p *os.Process) error { return syscall.Kill(-p.Pid, syscall.SIGTERM) }
func killProcess(p *os.Process) error      { return syscall.Kill(-p.Pid, syscall.SIGKILL) }

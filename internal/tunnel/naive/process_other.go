//go:build !linux

package naive

import (
	"os"
	"os/exec"
)

func configureProcess(cmd *exec.Cmd)       {}
func terminateProcess(p *os.Process) error { return p.Kill() }
func killProcess(p *os.Process) error      { return p.Kill() }

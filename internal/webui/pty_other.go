//go:build !linux

package webui

import (
	"errors"
	"os"
	"os/exec"
)

// The panel is served on Linux; elsewhere there is no terminal to offer.
func startPTY(*exec.Cmd, uint16, uint16) (*os.File, error) {
	return nil, errors.New("the terminal is only available on Linux")
}

func resizePTY(*os.File, uint16, uint16) error { return nil }

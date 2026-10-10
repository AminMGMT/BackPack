package tui

import (
	"os"

	"golang.org/x/sys/unix"
)

func beginQuickInput(f *os.File) (func(), bool) {
	fd := int(f.Fd())
	previous, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, false
	}
	current := *previous
	current.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG
	current.Cc[unix.VMIN], current.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &current); err != nil {
		return nil, false
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, previous) }, true
}

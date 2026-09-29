//go:build linux

package webui

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// startPTY runs cmd on a new pseudo-terminal and returns its master side.
//
// The terminal section needs a real terminal and not a pipe: a shell on a pipe
// has no line editing, no job control, and every full-screen program — top,
// nano, less — refuses to start. This is the few ioctls openpty(3) makes,
// written out so the panel takes no dependency for them.
func startPTY(cmd *exec.Cmd, rows, cols uint16) (*os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("opening a pseudo-terminal: %w", err)
	}
	fd := int(master.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		return nil, fmt.Errorf("unlocking the pseudo-terminal: %w", err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		master.Close()
		return nil, fmt.Errorf("naming the pseudo-terminal: %w", err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, fmt.Errorf("opening the terminal side: %w", err)
	}
	defer slave.Close()
	_ = resizePTY(master, rows, cols)

	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// A session of its own with the terminal as its controlling one, which is
	// what makes Ctrl+C reach the program in front rather than the panel, and
	// what lets a hang-up end the whole tree when the page closes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, err
	}
	return master, nil
}

// resizePTY tells the terminal how big the browser's window onto it is.
func resizePTY(master *os.File, rows, cols uint16) error {
	if rows == 0 || cols == 0 {
		return nil
	}
	return unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ,
		&unix.Winsize{Row: rows, Col: cols})
}

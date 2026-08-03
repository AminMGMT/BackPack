//go:build linux

package handlers

import (
	"io"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// Moving bytes between two sockets without copying them into user space.
//
// A relay's cost is not arithmetic, it is touching the data. The buffered path
// reads a chunk out of the kernel into a Go slice and immediately writes the
// same bytes back into the kernel — two syscalls and two copies for every
// chunk, to hand over bytes it never looks at. splice(2) says the same thing to
// the kernel directly: move this socket's data to that socket. The bytes never
// leave kernel memory, and on the small VPS this tunnel usually runs on, where
// the CPU is what runs out before the link does, that is the difference between
// saturating an uplink and not.
//
// Why this is written out rather than left to io.Copy, which would splice by
// itself: io.Copy hands the kernel the whole stream and does not come back
// until it ends. The tunnel would then be unable to say how much it had carried
// until each connection closed, and the panel would show a running download as
// an idle tunnel — which is exactly the failure the live counters were added to
// prevent. Driving the loop here means the transfer is still one uninterrupted
// splice as far as latency is concerned, one chunk moved as soon as it arrives,
// while every chunk is still counted as it goes.
//
// It fails safe in the strongest sense available: until the first byte moves,
// any refusal — no pipe, a kernel that will not splice this socket type, a
// sandbox that forbids it — is reported as "not handled" with nothing
// consumed, and the caller silently does the buffered copy instead. The worst
// case is the performance we had before, never a broken tunnel.

// spliceChunk is the most to move in one go. The kernel caps a single splice at
// the pipe's capacity, so the pipe is grown to match; a request larger than the
// pipe simply moves less, which the loop handles anyway.
const spliceChunk = 1 << 20 // 1 MiB

// spliceRelay moves everything readable from src into dst until src ends,
// reporting each chunk through onChunk as it moves.
//
// handled is false only when splice never got started and nothing was
// consumed, which is the caller's signal to fall back to a buffered copy.
func spliceRelay(dst, src *net.TCPConn, onChunk func(n int)) (handled bool, err error) {
	srcRaw, err := src.SyscallConn()
	if err != nil {
		return false, nil
	}
	dstRaw, err := dst.SyscallConn()
	if err != nil {
		return false, nil
	}

	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		return false, nil
	}
	pr, pw := fds[0], fds[1]
	defer unix.Close(pr)
	defer unix.Close(pw)

	// Best effort: a pipe that cannot be grown just moves smaller chunks.
	_, _ = unix.FcntlInt(uintptr(pw), unix.F_SETPIPE_SZ, spliceChunk)

	for {
		inPipe, err := spliceDrain(srcRaw, pw)
		if err != nil {
			// A failed splice consumes nothing, so while the transfer has not
			// started there is always a way back: hand the stream to the
			// buffered copy and let it deal with whatever this was. EINVAL —
			// the kernel refusing this socket type — is the expected case, but
			// treating every early failure the same way means an unforeseen
			// one degrades to the old path instead of dropping a connection.
			if !handled {
				return false, nil
			}
			return true, err
		}
		if inPipe == 0 {
			// src is at EOF, which is how a relay normally ends.
			return true, nil
		}
		handled = true

		// One drain can put more into the pipe than one write to dst will
		// take, so pump until the pipe is empty. Leaving bytes behind would
		// lose them: the next drain overwrites nothing, but the pipe would
		// grow until it blocked forever.
		for inPipe > 0 {
			n, err := splicePump(dstRaw, pr, inPipe)
			if n > 0 {
				inPipe -= n
				onChunk(int(n))
			}
			if err != nil {
				return true, err
			}
			if n == 0 {
				return true, io.ErrUnexpectedEOF
			}
		}
	}
}

// spliceDrain moves what is readable on the socket into the pipe, waiting on
// the runtime's poller — not on the thread — when there is nothing yet.
//
// Returning false from the callback is what asks RawConn.Read to wait for
// readability and try again, which is the only way to block here without
// pinning an OS thread for the life of an idle connection.
func spliceDrain(raw syscall.RawConn, pw int) (int64, error) {
	var moved int64
	var spliceErr error

	rawErr := raw.Read(func(fd uintptr) bool {
		for {
			n, err := unix.Splice(int(fd), nil, pw, nil, spliceChunk, unix.SPLICE_F_MOVE|unix.SPLICE_F_NONBLOCK)
			if err == unix.EINTR {
				continue
			}
			if err == unix.EAGAIN {
				return false // nothing readable yet; wait and be called again
			}
			moved, spliceErr = int64(n), err
			return true
		}
	})
	if rawErr != nil {
		return 0, rawErr
	}
	return moved, spliceErr
}

// splicePump moves up to max bytes from the pipe out to the socket, waiting on
// the poller when the socket's send buffer is full.
func splicePump(raw syscall.RawConn, pr int, max int64) (int64, error) {
	var moved int64
	var spliceErr error

	rawErr := raw.Write(func(fd uintptr) bool {
		for {
			n, err := unix.Splice(pr, nil, int(fd), nil, int(max), unix.SPLICE_F_MOVE|unix.SPLICE_F_NONBLOCK)
			if err == unix.EINTR {
				continue
			}
			if err == unix.EAGAIN {
				return false // send buffer full; wait and be called again
			}
			moved, spliceErr = int64(n), err
			return true
		}
	})
	if rawErr != nil {
		return 0, rawErr
	}
	return moved, spliceErr
}

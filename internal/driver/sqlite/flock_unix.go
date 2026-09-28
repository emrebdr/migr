//go:build unix

package sqlite

import (
	"errors"
	"os"
	"syscall"
)

// takeExclusive asks for the whole-file lock without waiting, reporting whether it was granted.
func takeExclusive(file *os.File) (bool, error) {
	err := syscall.Flock(descriptor(file), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}

	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}

	return false, err
}

// releaseExclusive gives the whole-file lock back, which closing the descriptor also does.
func releaseExclusive(file *os.File) error {
	return syscall.Flock(descriptor(file), syscall.LOCK_UN)
}

// descriptor is the file descriptor flock takes, which the os package hands back as a uintptr.
func descriptor(file *os.File) int {
	return int(file.Fd()) //nolint:gosec // a descriptor is a small non-negative int, widened by os
}

// alive reports whether a process exists, reading a signal it may not send as existing.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}

	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

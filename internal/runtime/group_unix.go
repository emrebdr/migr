//go:build unix

package runtime

import (
	"os/exec"
	"syscall"
)

// The signals the close ladder climbs, named here so process.go needs no build tag of its own.
const (
	syscallTerm = syscall.SIGTERM
	syscallKill = syscall.SIGKILL
)

// group puts the child in a process group of its own, so the engine can end everything it started.
func group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup signals the whole group, which is what makes a runtime's own children die with it.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return syscall.ESRCH
	}

	return syscall.Kill(-cmd.Process.Pid, sig)
}
